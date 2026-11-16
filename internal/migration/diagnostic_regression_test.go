package migration

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"goaster/internal/diagnostics"
	"goaster/internal/scheduler"
)

type diagnosticHookScheduler struct {
	MonitorScheduler
	coordinator    monitorExecutionCoordinator
	beforeStop     func()
	beforeComplete func()
}

func (s diagnosticHookScheduler) StopIfNotRunning(id string) (scheduler.Job, error) {
	if s.beforeStop != nil {
		s.beforeStop()
	}
	return s.coordinator.StopIfNotRunning(id)
}
func (s diagnosticHookScheduler) ClaimExecutionMatching(ctx context.Context, job scheduler.Job) (scheduler.Job, func(error) (scheduler.Job, error), error) {
	claimed, complete, err := s.coordinator.ClaimExecutionMatching(ctx, job)
	if complete != nil {
		original := complete
		complete = func(err error) (scheduler.Job, error) {
			if s.beforeComplete != nil {
				s.beforeComplete()
			}
			return original(err)
		}
	}
	return claimed, complete, err
}

func TestTerminalPublishedBeforeClaimReleaseAndAllowsFreshStart(t *testing.T) {
	jobs, err := scheduler.New(scheduler.Options{Path: filepath.Join(t.TempDir(), "jobs.json")})
	if err != nil {
		t.Fatal(err)
	}
	options := newMachineFixture(t).options(t.TempDir())
	options.LoadHetznerToken = func() (string, error) { return "", ErrHetznerTokenMissing }
	options.DeliverHandoff = func(context.Context, []byte) error { return nil }
	checked := false
	wrapped := diagnosticHookScheduler{MonitorScheduler: jobs, coordinator: jobs, beforeComplete: func() {
		d, found, err := loadMonitorDiagnostic(options, "migration-singleton")
		if err != nil || !found || d.Code != codeMissingToken {
			t.Fatalf("before completion: %+v %v %v", d, found, err)
		}
		checked = true
	}}
	first, err := StartMonitor(t.Context(), Request{ServerType: "cx22", Locations: []string{"fsn1"}}, options, wrapped)
	if !checked || !errors.Is(err, ErrHetznerTokenMissing) {
		t.Fatalf("first: %+v %v", first, err)
	}
	again, err := StartMonitor(t.Context(), Request{ServerType: "cx32"}, options, jobs)
	if !errors.Is(err, ErrHetznerTokenMissing) || again.MonitorID == first.MonitorID || again.ServerType != "cx32" || len(again.Locations) != 0 || again.Diagnostic.Code != codeMissingToken {
		t.Fatalf("again: %+v %v", again, err)
	}
}

func TestStopReloadsHistoryAndIsIdempotent(t *testing.T) {
	jobs, err := scheduler.New(scheduler.Options{Path: filepath.Join(t.TempDir(), "jobs.json")})
	if err != nil {
		t.Fatal(err)
	}
	options := newMachineFixture(t).options(t.TempDir())
	req := Request{ServerType: "cx22", JobID: "migration-stop-reload"}
	msg, err := schedulerMessage(req, 42)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = jobs.Add(scheduler.Job{ID: req.JobID, Message: msg, Kind: scheduler.ScheduleEvery, EverySeconds: MonitorIntervalSeconds}); err != nil {
		t.Fatal(err)
	}
	if err = storeMonitorDiagnostic(options, initialMonitorDiagnostic(req.JobID)); err != nil {
		t.Fatal(err)
	}
	wrapped := diagnosticHookScheduler{MonitorScheduler: jobs, coordinator: jobs, beforeStop: func() {
		d := diagnosticTemplate(stageCreate, codeCreated)
		d.MonitorID = req.JobID
		d.CreationState = creationCreated
		d.MonitoringState = monitoringUnknown
		d.MonitoringStopped = false
		if err := storeMonitorDiagnostic(options, d); err != nil {
			t.Fatal(err)
		}
	}}
	stopped, err := StopMonitorWithOptions(req.JobID, wrapped, options)
	if err != nil || stopped.Diagnostic.CreationState != creationCreated {
		t.Fatalf("stop: %+v %v", stopped, err)
	}
	dir, err := operationDirectory(options)
	if err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		stopped, err = StopMonitorWithOptions(req.JobID, jobs, options)
		if err != nil || stopped.Diagnostic.CreationState != creationCreated {
			t.Fatalf("repeat: %+v %v", stopped, err)
		}
	}
	after, err := os.ReadDir(dir)
	if err != nil || len(before) != len(after) {
		t.Fatalf("history grew: %d %d %v", len(before), len(after), err)
	}
}

func TestCorruptHistoryDoesNotPreventStop(t *testing.T) {
	jobs, err := scheduler.New(scheduler.Options{Path: filepath.Join(t.TempDir(), "jobs.json")})
	if err != nil {
		t.Fatal(err)
	}
	options := newMachineFixture(t).options(t.TempDir())
	req := Request{ServerType: "cx22", JobID: "migration-corrupt"}
	msg, err := schedulerMessage(req, 42)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = jobs.Add(scheduler.Job{ID: req.JobID, Message: msg, Kind: scheduler.ScheduleEvery, EverySeconds: MonitorIntervalSeconds}); err != nil {
		t.Fatal(err)
	}
	if err = storeMonitorDiagnostic(options, initialMonitorDiagnostic(req.JobID)); err != nil {
		t.Fatal(err)
	}
	dir, err := operationDirectory(options)
	if err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) != 1 {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(dir, entries[0].Name()), []byte(`{"secret":"sensitive-history-token"`), 0600); err != nil {
		t.Fatal(err)
	}
	result, err := StopMonitorWithOptions(req.JobID, jobs, options)
	job, found := jobs.Get(req.JobID)
	if !errors.Is(err, ErrPersistedStateInvalid) || !found || job.Enabled || result.Status != resultStopped || !result.Diagnostic.MonitoringStopped || result.Diagnostic.CreationState != creationUnknown {
		t.Fatalf("stop: %+v job=%+v err=%v", result, job, err)
	}
	diagnostic := DiagnosticFor(err, result)
	if diagnostic.Code != codePersistedStateInvalid || diagnostic.Cause != "the persisted migration diagnostic state is invalid" || diagnostic.NextAction != "reconcile the retained monitor record manually before any new attempt" || diagnostic.ResultState != resultStopped || diagnostic.CreationState != creationUnknown || diagnostic.MonitoringState != monitoringStopped || !diagnostic.MonitoringStopped {
		t.Fatalf("diagnostic: %+v", diagnostic)
	}
	encoded, marshalErr := json.Marshal(diagnostic)
	if marshalErr != nil || strings.Contains(string(encoded), "sensitive-history-token") || strings.Contains(string(encoded), dir) || strings.Contains(string(encoded), "unexpected EOF") {
		t.Fatalf("unsafe diagnostic JSON: %s (error: %v)", encoded, marshalErr)
	}
}

func TestStatusRecoversCurrentMonitorWithoutUserID(t *testing.T) {
	options := Options{TempDirectory: t.TempDir(), MigrationRecipient: func() (int64, error) { return 42, nil }}
	jbs, err := scheduler.New(scheduler.Options{Path: filepath.Join(t.TempDir(), "jobs.json")})
	if err != nil {
		t.Fatal(err)
	}
	request := Request{ServerType: "cx22", JobID: "migration-current"}
	message, err := schedulerMessage(request, 42)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := jbs.Add(scheduler.Job{ID: request.JobID, Message: message, Kind: scheduler.ScheduleEvery, EverySeconds: MonitorIntervalSeconds}); err != nil {
		t.Fatal(err)
	}
	if err := storeMonitorDiagnostic(options, initialMonitorDiagnostic(request.JobID)); err != nil {
		t.Fatal(err)
	}

	status, err := MigrationStatusWithOptions("", jbs, options)
	if err != nil || status.MonitorID != request.JobID || status.Migration.MonitorID != request.JobID || status.Migration.Diagnostic.MonitorID != request.JobID || status.Diagnostic.MonitorID != request.JobID || status.Request.JobID != request.JobID {
		t.Fatalf("status without ID = %+v, %v", status, err)
	}
}

func TestStatusWithoutIDDoesNotCrossPairedOwner(t *testing.T) {
	options := Options{TempDirectory: t.TempDir(), MigrationRecipient: func() (int64, error) { return 42, nil }}
	jbs, err := scheduler.New(scheduler.Options{Path: filepath.Join(t.TempDir(), "jobs.json")})
	if err != nil {
		t.Fatal(err)
	}
	request := Request{ServerType: "cx22", JobID: "migration-other-owner"}
	message, err := schedulerMessage(request, 99)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := jbs.Add(scheduler.Job{ID: request.JobID, Message: message, Kind: scheduler.ScheduleEvery, EverySeconds: MonitorIntervalSeconds}); err != nil {
		t.Fatal(err)
	}
	if err := storeMonitorDiagnostic(options, initialMonitorDiagnostic(request.JobID)); err != nil {
		t.Fatal(err)
	}

	status, err := MigrationStatusWithOptions("", jbs, options)
	if !errors.Is(err, ErrMonitorNotFound) || status.MonitorID != "" || status.Migration.MonitorID != "" {
		t.Fatalf("cross-owner status without ID = %+v, %v", status, err)
	}
}

func TestStatusHistoryFailureRetainsValidMonitorIDInResultAndLog(t *testing.T) {
	const monitorID = "migration-history-error"
	jbs, err := scheduler.New(scheduler.Options{Path: filepath.Join(t.TempDir(), "jobs.json")})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := jbs.Add(scheduler.Job{
		ID:           monitorID,
		Message:      schedulerMessagePrefix + `{"private":"history-secret"}`,
		Kind:         scheduler.ScheduleEvery,
		EverySeconds: MonitorIntervalSeconds,
	}); err != nil {
		t.Fatal(err)
	}

	var output bytes.Buffer
	restore := diagnostics.SetOutput(&output)
	defer restore()
	status, err := MigrationStatusWithOptions("", jbs, Options{})
	if !errors.Is(err, ErrPersistedStateInvalid) {
		t.Fatalf("history failure error = %v", err)
	}
	if status.MonitorID != monitorID || status.Migration.MonitorID != monitorID || status.Migration.Diagnostic.MonitorID != monitorID || status.Diagnostic.MonitorID != monitorID {
		t.Fatalf("history failure monitor ID was not retained: %+v", status)
	}
	if status.Migration.Status != resultManualReview || status.Migration.Diagnostic.Code != codePersistedStateInvalid {
		t.Fatalf("history failure result = %+v", status.Migration)
	}
	log := output.String()
	if !strings.Contains(log, `monitor_id="`+monitorID+`"`) {
		t.Fatalf("history failure log omitted monitor ID: %q", log)
	}
	if strings.Contains(log, "history-secret") {
		t.Fatalf("history failure log leaked scheduler content: %q", log)
	}
}
