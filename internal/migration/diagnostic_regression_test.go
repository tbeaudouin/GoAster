package migration

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

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
