package migration

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"goaster/internal/scheduler"
)

func retryTestOptions(t *testing.T, fixture *machineFixture, jobsPath, operationPath string) (*scheduler.Scheduler, Options) {
	t.Helper()
	jobs, err := scheduler.New(scheduler.Options{Path: jobsPath})
	if err != nil {
		t.Fatal(err)
	}
	options := fixture.options(operationPath)
	options.DeliverHandoff = func(context.Context, []byte) error { return nil }
	return jobs, options
}

func operationIDForTest(t *testing.T, directory, monitorID string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(directory, monitorID+".json"))
	if err != nil {
		t.Fatal(err)
	}
	var state operationState
	if err := json.Unmarshal(data, &state); err != nil {
		t.Fatal(err)
	}
	return state.Terms.OperationID
}

func TestMissingTokenStartCanRetryWithFreshAttemptAndRetainedHistory(t *testing.T) {
	fixture := newMachineFixture(t)
	fixture.available.Store(true)
	jobsPath := filepath.Join(t.TempDir(), "jobs.json")
	operationPath := t.TempDir()
	jobs, options := retryTestOptions(t, fixture, jobsPath, operationPath)
	var configured atomic.Bool
	options.LoadHetznerToken = func() (string, error) {
		if !configured.Load() {
			return "", ErrHetznerTokenMissing
		}
		return fixture.token, nil
	}
	request := Request{ServerType: "cx22", Locations: []string{"fsn1"}}
	first, err := StartMonitor(t.Context(), request, options, jobs)
	if !errors.Is(err, ErrHetznerTokenMissing) || first.MonitorID == "" || fixture.creates.Load() != 0 {
		t.Fatalf("initial missing-token attempt = %+v, %v creates=%d", first, err, fixture.creates.Load())
	}
	configured.Store(true)
	retry, err := StartMonitor(t.Context(), request, options, jobs)
	if err != nil || retry.Status != resultCreated || retry.MonitorID == first.MonitorID || fixture.creates.Load() != 1 {
		t.Fatalf("corrected-token retry = %+v, %v creates=%d", retry, err, fixture.creates.Load())
	}
	if oldID, newID := operationIDForTest(t, operationPath, first.MonitorID), operationIDForTest(t, operationPath, retry.MonitorID); oldID == newID || oldID == "" || newID == "" {
		t.Fatalf("operation IDs were not fresh: old=%q new=%q", oldID, newID)
	}
	if jobsFound := jobs.List(); len(jobsFound) != 2 {
		t.Fatalf("scheduler history length = %d, jobs=%+v", len(jobsFound), jobsFound)
	}
	oldStatus, oldErr := MigrationStatusWithOptions(first.MonitorID, jobs, options)
	if oldErr != nil || oldStatus.Diagnostic.Code != codeMissingToken || oldStatus.Diagnostic.CreationState != creationNotCreated {
		t.Fatalf("retained old status = %+v, %v", oldStatus.Diagnostic, oldErr)
	}
	newStatus, newErr := MigrationStatusWithOptions(retry.MonitorID, jobs, options)
	if newErr != nil || newStatus.Diagnostic.Code != codeCreated || newStatus.Diagnostic.CreationState != creationCreated {
		t.Fatalf("new status = %+v, %v", newStatus.Diagnostic, newErr)
	}
	restarted, err := scheduler.New(scheduler.Options{Path: jobsPath})
	if err != nil {
		t.Fatal(err)
	}
	if status, err := MigrationStatusWithOptions(retry.MonitorID, restarted, options); err != nil || status.Diagnostic.Code != codeCreated {
		t.Fatalf("restarted status = %+v, %v", status.Diagnostic, err)
	}
}

func TestUnknownRetryRequiresExactCurrentConfirmationAndFreshRecords(t *testing.T) {
	fixture := newMachineFixture(t)
	fixture.available.Store(true)
	fixture.lostCreate.Store(true)
	jobs, options := retryTestOptions(t, fixture, filepath.Join(t.TempDir(), "jobs.json"), t.TempDir())
	request := Request{ServerType: "cx22", Locations: []string{"fsn1"}}
	first, err := StartMonitor(t.Context(), request, options, jobs)
	if err == nil || first.MonitorID == "" || first.Diagnostic.CreationState != creationUnknown || fixture.creates.Load() != 1 {
		t.Fatalf("unknown initial attempt = %+v, %v creates=%d", first, err, fixture.creates.Load())
	}
	withoutConfirmation, err := StartMonitor(t.Context(), request, options, jobs)
	if !errors.Is(err, ErrRetryConfirmationRequired) || withoutConfirmation.MonitorID != first.MonitorID || fixture.creates.Load() != 1 {
		t.Fatalf("unconfirmed retry = %+v, %v creates=%d", withoutConfirmation, err, fixture.creates.Load())
	}
	wrong, err := StartMonitor(t.Context(), Request{ServerType: request.ServerType, Locations: request.Locations, RetryAttempt: "migration-not-the-prior-attempt"}, options, jobs)
	if !errors.Is(err, ErrRetryConfirmationRequired) || wrong.MonitorID != first.MonitorID || fixture.creates.Load() != 1 {
		t.Fatalf("wrong confirmation = %+v, %v creates=%d", wrong, err, fixture.creates.Load())
	}
	second, err := StartMonitor(t.Context(), Request{ServerType: request.ServerType, Locations: request.Locations, RetryAttempt: first.MonitorID}, options, jobs)
	if err == nil || second.MonitorID == first.MonitorID || second.Diagnostic.CreationState != creationUnknown || fixture.creates.Load() != 2 {
		t.Fatalf("confirmed retry = %+v, %v creates=%d", second, err, fixture.creates.Load())
	}
	stale, err := StartMonitor(t.Context(), Request{ServerType: request.ServerType, Locations: request.Locations, RetryAttempt: first.MonitorID}, options, jobs)
	if !errors.Is(err, ErrRetryConfirmationRequired) || stale.MonitorID != second.MonitorID || fixture.creates.Load() != 2 {
		t.Fatalf("stale confirmation = %+v, %v creates=%d", stale, err, fixture.creates.Load())
	}
	oldOperation := operationIDForTest(t, options.TempDirectory, first.MonitorID)
	newOperation := operationIDForTest(t, options.TempDirectory, second.MonitorID)
	if oldOperation == newOperation {
		t.Fatalf("unknown retry reused operation ID %q", oldOperation)
	}
	if jobsFound := jobs.List(); len(jobsFound) != 2 {
		t.Fatalf("unknown retry did not retain scheduler records: %+v", jobsFound)
	}
}

func TestConfirmedCreatedAttemptRequiresExactFreshConfirmation(t *testing.T) {
	fixture := newMachineFixture(t)
	fixture.available.Store(true)
	jobs, options := retryTestOptions(t, fixture, filepath.Join(t.TempDir(), "jobs.json"), t.TempDir())
	first, err := StartMonitor(t.Context(), Request{ServerType: "cx22", Locations: []string{"fsn1"}}, options, jobs)
	if err != nil || first.Diagnostic.CreationState != creationCreated || fixture.creates.Load() != 1 {
		t.Fatalf("created attempt = %+v, %v creates=%d", first, err, fixture.creates.Load())
	}
	var tokenLoads atomic.Int32
	options.LoadHetznerToken = func() (string, error) {
		tokenLoads.Add(1)
		return fixture.token, nil
	}
	blocked, err := StartMonitor(t.Context(), Request{ServerType: "cx22"}, options, jobs)
	if !errors.Is(err, ErrRetryConfirmationRequired) || blocked.MonitorID != first.MonitorID || blocked.Diagnostic.Code != codeRetryConfirmation || blocked.Diagnostic.CreationState != creationCreated || fixture.creates.Load() != 1 || tokenLoads.Load() != 0 {
		t.Fatalf("unconfirmed created retry = %+v, %v creates=%d tokenLoads=%d", blocked, err, fixture.creates.Load(), tokenLoads.Load())
	}
	confirmed, err := StartMonitor(t.Context(), Request{ServerType: "cx22", Locations: []string{"fsn1"}, RetryAttempt: first.MonitorID}, options, jobs)
	if err != nil || confirmed.MonitorID == first.MonitorID || confirmed.Diagnostic.CreationState != creationCreated || fixture.creates.Load() != 2 {
		t.Fatalf("confirmed created retry = %+v, %v creates=%d", confirmed, err, fixture.creates.Load())
	}
	stale, err := StartMonitor(t.Context(), Request{ServerType: "cx22", Locations: []string{"fsn1"}, RetryAttempt: first.MonitorID}, options, jobs)
	if !errors.Is(err, ErrRetryConfirmationRequired) || stale.MonitorID != confirmed.MonitorID || stale.Diagnostic.CreationState != creationCreated || fixture.creates.Load() != 2 {
		t.Fatalf("stale created confirmation = %+v, %v creates=%d", stale, err, fixture.creates.Load())
	}
	if oldOperation, newOperation := operationIDForTest(t, options.TempDirectory, first.MonitorID), operationIDForTest(t, options.TempDirectory, confirmed.MonitorID); oldOperation == newOperation {
		t.Fatalf("created retry reused operation ID %q", oldOperation)
	}
	if jobsFound := jobs.List(); len(jobsFound) != 2 {
		t.Fatalf("created retry did not retain scheduler records: %+v", jobsFound)
	}
}

func TestCreatedHandoffFailureRequiresConfirmationBeforeFreshAttempt(t *testing.T) {
	fixture := newMachineFixture(t)
	fixture.available.Store(true)
	jobs, options := retryTestOptions(t, fixture, filepath.Join(t.TempDir(), "jobs.json"), t.TempDir())
	var failDelivery atomic.Bool
	failDelivery.Store(true)
	options.PrepareRecipientHandoff = func(int64) (func(context.Context, []byte) error, error) {
		return func(context.Context, []byte) error {
			if failDelivery.Load() {
				return errors.New("synthetic handoff delivery failure")
			}
			return nil
		}, nil
	}
	request := Request{ServerType: "cx22", Locations: []string{"fsn1"}}
	first, err := StartMonitor(t.Context(), request, options, jobs)
	if !errors.Is(err, ErrHandoffFailure) || first.Diagnostic.Code != codeHandoffFailure || first.Diagnostic.CreationState != creationCreated || fixture.creates.Load() != 1 {
		t.Fatalf("created handoff failure = %+v, %v creates=%d", first, err, fixture.creates.Load())
	}
	status, statusErr := MigrationStatusWithOptions(first.MonitorID, jobs, options)
	if statusErr != nil || status.Diagnostic.Code != codeHandoffFailure || status.Diagnostic.CreationState != creationCreated || status.Migration.Delivered {
		t.Fatalf("handoff-failure status = %+v, %v", status, statusErr)
	}
	failDelivery.Store(false)
	blocked, err := StartMonitor(t.Context(), request, options, jobs)
	if !errors.Is(err, ErrRetryConfirmationRequired) || blocked.MonitorID != first.MonitorID || blocked.Diagnostic.CreationState != creationCreated || fixture.creates.Load() != 1 {
		t.Fatalf("unconfirmed handoff-failure retry = %+v, %v creates=%d", blocked, err, fixture.creates.Load())
	}
	confirmed, err := StartMonitor(t.Context(), Request{ServerType: request.ServerType, Locations: request.Locations, RetryAttempt: first.MonitorID}, options, jobs)
	if err != nil || confirmed.MonitorID == first.MonitorID || confirmed.Diagnostic.CreationState != creationCreated || !confirmed.Delivered || fixture.creates.Load() != 2 {
		t.Fatalf("confirmed handoff-failure retry = %+v, %v creates=%d", confirmed, err, fixture.creates.Load())
	}
}

func TestConcurrentStartsShareOneActiveAttemptAcrossHistory(t *testing.T) {
	fixture := newMachineFixture(t)
	fixture.available.Store(true)
	fixture.availabilityStarted = make(chan struct{})
	fixture.availabilityRelease = make(chan struct{})
	jobs, options := retryTestOptions(t, fixture, filepath.Join(t.TempDir(), "jobs.json"), t.TempDir())
	firstDone := make(chan error, 1)
	go func() {
		_, err := StartMonitor(t.Context(), Request{ServerType: "cx22", Locations: []string{"fsn1"}}, options, jobs)
		firstDone <- err
	}()
	select {
	case <-fixture.availabilityStarted:
	case <-timeAfterForRetryTest():
		t.Fatal("first start did not reach availability")
	}
	const concurrent = 6
	errorsSeen := make(chan error, concurrent)
	var wait sync.WaitGroup
	for i := 0; i < concurrent; i++ {
		wait.Add(1)
		go func() {
			defer wait.Done()
			_, err := StartMonitor(t.Context(), Request{ServerType: "cx22", Locations: []string{"fsn1"}}, options, jobs)
			errorsSeen <- err
		}()
	}
	wait.Wait()
	close(fixture.availabilityRelease)
	if err := <-firstDone; err != nil {
		t.Fatalf("first start = %v", err)
	}
	for i := 0; i < concurrent; i++ {
		if err := <-errorsSeen; !errors.Is(err, scheduler.ErrExecutionInProgress) {
			t.Fatalf("concurrent start error = %v", err)
		}
	}
	if fixture.creates.Load() != 1 || len(jobs.List()) != 1 {
		t.Fatalf("concurrent starts overlapped: creates=%d jobs=%+v", fixture.creates.Load(), jobs.List())
	}
}

func TestConcurrentConfirmedRetriesShareOneActiveAttempt(t *testing.T) {
	fixture := newMachineFixture(t)
	fixture.available.Store(true)
	fixture.lostCreate.Store(true)
	jbs, options := retryTestOptions(t, fixture, filepath.Join(t.TempDir(), "jobs.json"), t.TempDir())
	first, err := StartMonitor(t.Context(), Request{ServerType: "cx22", Locations: []string{"fsn1"}}, options, jbs)
	if err == nil || first.Diagnostic.CreationState != creationUnknown || fixture.creates.Load() != 1 {
		t.Fatalf("initial uncertain attempt = %+v, %v creates=%d", first, err, fixture.creates.Load())
	}
	fixture.availabilityStarted = make(chan struct{})
	fixture.availabilityRelease = make(chan struct{})
	fixture.availabilityOnce = sync.Once{}
	firstDone := make(chan error, 1)
	go func() {
		_, retryErr := StartMonitor(t.Context(), Request{ServerType: "cx22", Locations: []string{"fsn1"}, RetryAttempt: first.MonitorID}, options, jbs)
		firstDone <- retryErr
	}()
	select {
	case <-fixture.availabilityStarted:
	case <-timeAfterForRetryTest():
		t.Fatal("confirmed retry did not reach availability")
	}
	const concurrent = 6
	errorsSeen := make(chan error, concurrent)
	var wait sync.WaitGroup
	for i := 0; i < concurrent; i++ {
		wait.Add(1)
		go func() {
			defer wait.Done()
			_, retryErr := StartMonitor(t.Context(), Request{ServerType: "cx22", Locations: []string{"fsn1"}, RetryAttempt: first.MonitorID}, options, jbs)
			errorsSeen <- retryErr
		}()
	}
	wait.Wait()
	close(fixture.availabilityRelease)
	if retryErr := <-firstDone; retryErr == nil {
		t.Fatal("uncertain confirmed retry was swallowed")
	}
	for i := 0; i < concurrent; i++ {
		if retryErr := <-errorsSeen; !errors.Is(retryErr, scheduler.ErrExecutionInProgress) {
			t.Fatalf("concurrent confirmed retry error = %v", retryErr)
		}
	}
	if fixture.creates.Load() != 2 || len(jbs.List()) != 2 {
		t.Fatalf("confirmed retries overlapped: creates=%d jobs=%+v", fixture.creates.Load(), jbs.List())
	}
}

func TestConcurrentConfirmedCreatedRetriesShareOneActiveAttempt(t *testing.T) {
	fixture := newMachineFixture(t)
	fixture.available.Store(true)
	jobs, options := retryTestOptions(t, fixture, filepath.Join(t.TempDir(), "jobs.json"), t.TempDir())
	first, err := StartMonitor(t.Context(), Request{ServerType: "cx22", Locations: []string{"fsn1"}}, options, jobs)
	if err != nil || first.Diagnostic.CreationState != creationCreated || fixture.creates.Load() != 1 {
		t.Fatalf("initial created attempt = %+v, %v creates=%d", first, err, fixture.creates.Load())
	}
	fixture.availabilityStarted = make(chan struct{})
	fixture.availabilityRelease = make(chan struct{})
	fixture.availabilityOnce = sync.Once{}
	firstDone := make(chan error, 1)
	go func() {
		_, retryErr := StartMonitor(t.Context(), Request{ServerType: "cx22", Locations: []string{"fsn1"}, RetryAttempt: first.MonitorID}, options, jobs)
		firstDone <- retryErr
	}()
	select {
	case <-fixture.availabilityStarted:
	case <-timeAfterForRetryTest():
		t.Fatal("confirmed created retry did not reach availability")
	}
	const concurrent = 6
	errorsSeen := make(chan error, concurrent)
	var wait sync.WaitGroup
	for i := 0; i < concurrent; i++ {
		wait.Add(1)
		go func() {
			defer wait.Done()
			_, retryErr := StartMonitor(t.Context(), Request{ServerType: "cx22", Locations: []string{"fsn1"}, RetryAttempt: first.MonitorID}, options, jobs)
			errorsSeen <- retryErr
		}()
	}
	wait.Wait()
	close(fixture.availabilityRelease)
	if retryErr := <-firstDone; retryErr != nil {
		t.Fatalf("confirmed created retry failed: %v", retryErr)
	}
	for i := 0; i < concurrent; i++ {
		if retryErr := <-errorsSeen; !errors.Is(retryErr, scheduler.ErrExecutionInProgress) {
			t.Fatalf("concurrent confirmed created retry error = %v", retryErr)
		}
	}
	if fixture.creates.Load() != 2 || len(jobs.List()) != 2 {
		t.Fatalf("confirmed created retries overlapped: creates=%d jobs=%+v", fixture.creates.Load(), jobs.List())
	}
}

func TestMigrationStatusIsReadOnlyDuringRetryHistory(t *testing.T) {
	fixture := newMachineFixture(t)
	jbs, options := retryTestOptions(t, fixture, filepath.Join(t.TempDir(), "jobs.json"), t.TempDir())
	var tokenLoads atomic.Int32
	options.LoadHetznerToken = func() (string, error) {
		tokenLoads.Add(1)
		return "", ErrHetznerTokenMissing
	}
	first, err := StartMonitor(t.Context(), Request{ServerType: "cx22", Locations: []string{"fsn1"}}, options, jbs)
	if !errors.Is(err, ErrHetznerTokenMissing) {
		t.Fatalf("initial status fixture = %+v, %v", first, err)
	}
	before, err := retryDirectorySnapshot(t, options.TempDirectory)
	if err != nil {
		t.Fatal(err)
	}
	availability, creates, loads := fixture.availability.Load(), fixture.creates.Load(), tokenLoads.Load()
	status, err := MigrationStatusWithOptions(first.MonitorID, jbs, options)
	if err != nil || status.Diagnostic.Code != codeMissingToken {
		t.Fatalf("status = %+v, %v", status, err)
	}
	after, err := retryDirectorySnapshot(t, options.TempDirectory)
	if err != nil {
		t.Fatal(err)
	}
	if fixture.availability.Load() != availability || fixture.creates.Load() != creates || tokenLoads.Load() != loads {
		t.Fatalf("status contacted mutable/provider state: availability=%d creates=%d tokenLoads=%d", fixture.availability.Load(), fixture.creates.Load(), tokenLoads.Load())
	}
	if len(before) != len(after) {
		t.Fatalf("status changed retained files: before=%v after=%v", before, after)
	}
	for name, content := range before {
		if after[name] != content {
			t.Fatalf("status changed retained file %q", name)
		}
	}
}

func TestMissingPriorOperationEvidenceCannotAuthorizeRetry(t *testing.T) {
	fixture := newMachineFixture(t)
	jobs, options := retryTestOptions(t, fixture, filepath.Join(t.TempDir(), "jobs.json"), t.TempDir())
	options.LoadHetznerToken = func() (string, error) { return "", ErrHetznerTokenMissing }
	first, err := StartMonitor(t.Context(), Request{ServerType: "cx22", Locations: []string{"fsn1"}}, options, jobs)
	if !errors.Is(err, ErrHetznerTokenMissing) {
		t.Fatalf("initial missing-token attempt = %+v, %v", first, err)
	}
	operationPath := filepath.Join(options.TempDirectory, first.MonitorID+".json")
	if err := os.Remove(operationPath); err != nil {
		t.Fatal(err)
	}
	options.LoadHetznerToken = func() (string, error) { return fixture.token, nil }
	retry, err := StartMonitor(t.Context(), Request{ServerType: "cx22", Locations: []string{"fsn1"}}, options, jobs)
	if !errors.Is(err, ErrPersistedStateInvalid) || retry.Diagnostic.CreationState != creationUnknown || len(jobs.List()) != 1 || fixture.creates.Load() != 0 {
		t.Fatalf("retry with missing prior evidence = %+v, %v jobs=%d creates=%d", retry, err, len(jobs.List()), fixture.creates.Load())
	}
}

func TestCorruptPriorDiagnosticCannotAuthorizeRetry(t *testing.T) {
	fixture := newMachineFixture(t)
	jbs, options := retryTestOptions(t, fixture, filepath.Join(t.TempDir(), "jobs.json"), t.TempDir())
	options.LoadHetznerToken = func() (string, error) { return "", ErrHetznerTokenMissing }
	first, err := StartMonitor(t.Context(), Request{ServerType: "cx22", Locations: []string{"fsn1"}}, options, jbs)
	if !errors.Is(err, ErrHetznerTokenMissing) {
		t.Fatalf("initial missing-token attempt = %+v, %v", first, err)
	}
	entries, err := os.ReadDir(options.TempDirectory)
	if err != nil {
		t.Fatal(err)
	}
	corrupted := ""
	for _, entry := range entries {
		if entry.Name() != "" && len(entry.Name()) > len(first.MonitorID)+len(diagnosticFileSeparator) && entry.Name()[:len(first.MonitorID)+len(diagnosticFileSeparator)] == first.MonitorID+diagnosticFileSeparator {
			corrupted = filepath.Join(options.TempDirectory, entry.Name())
			break
		}
	}
	if corrupted == "" {
		t.Fatal("missing diagnostic record to corrupt")
	}
	if err := os.WriteFile(corrupted, []byte(`{"corrupt":true`), 0600); err != nil {
		t.Fatal(err)
	}
	options.LoadHetznerToken = func() (string, error) { return fixture.token, nil }
	retry, err := StartMonitor(t.Context(), Request{ServerType: "cx22", Locations: []string{"fsn1"}}, options, jbs)
	if !errors.Is(err, ErrPersistedStateInvalid) || retry.Diagnostic.CreationState != creationUnknown || len(jbs.List()) != 1 || fixture.creates.Load() != 0 {
		t.Fatalf("retry with corrupt prior diagnostic = %+v, %v jobs=%d creates=%d", retry, err, len(jbs.List()), fixture.creates.Load())
	}
}

func retryDirectorySnapshot(t *testing.T, directory string) (map[string]string, error) {
	t.Helper()
	entries, err := os.ReadDir(directory)
	if err != nil {
		return nil, err
	}
	snapshot := make(map[string]string, len(entries))
	for _, entry := range entries {
		if entry.IsDir() {
			snapshot[entry.Name()] = "<directory>"
			continue
		}
		data, err := os.ReadFile(filepath.Join(directory, entry.Name()))
		if err != nil {
			return nil, err
		}
		snapshot[entry.Name()] = string(data)
	}
	return snapshot, nil
}

func timeAfterForRetryTest() <-chan time.Time {
	return time.After(2 * time.Second)
}

func TestAcceptedUnknownDoesNotBlockAfterLaterNotCreated(t *testing.T) {
	fixture := newMachineFixture(t)
	fixture.available.Store(true)
	fixture.lostCreate.Store(true)
	jobs, options := retryTestOptions(t, fixture, filepath.Join(t.TempDir(), "jobs.json"), t.TempDir())
	request := Request{ServerType: "cx22", Locations: []string{"fsn1"}}
	first, err := StartMonitor(t.Context(), request, options, jobs)
	if err == nil || first.Diagnostic.CreationState != creationUnknown {
		t.Fatalf("first: %+v %v", first, err)
	}
	options.LoadHetznerToken = func() (string, error) { return "", ErrHetznerTokenMissing }
	request.RetryAttempt = first.MonitorID
	second, err := StartMonitor(t.Context(), request, options, jobs)
	if !errors.Is(err, ErrHetznerTokenMissing) || second.Diagnostic.CreationState != creationNotCreated {
		t.Fatalf("second: %+v %v", second, err)
	}
	request.RetryAttempt = ""
	third, err := StartMonitor(t.Context(), request, options, jobs)
	if !errors.Is(err, ErrHetznerTokenMissing) || third.MonitorID == second.MonitorID {
		t.Fatalf("third: %+v %v", third, err)
	}
}

func TestDispatchMarkerMakesStatusAndStopUncertain(t *testing.T) {
	fixture := newMachineFixture(t)
	jobs, options := retryTestOptions(t, fixture, filepath.Join(t.TempDir(), "jobs.json"), t.TempDir())
	options.LoadHetznerToken = func() (string, error) { return "", ErrHetznerTokenMissing }
	first, err := StartMonitor(t.Context(), Request{ServerType: "cx22"}, options, jobs)
	if !errors.Is(err, ErrHetznerTokenMissing) {
		t.Fatal(err)
	}
	if err := markDispatch(options.TempDirectory, first.MonitorID); err != nil {
		t.Fatal(err)
	}
	status, err := MigrationStatusWithOptions(first.MonitorID, jobs, options)
	if err != nil || status.Diagnostic.CreationState != creationUnknown {
		t.Fatalf("status: %+v %v", status.Diagnostic, err)
	}
	stopped, err := StopMonitorWithOptions(first.MonitorID, jobs, options)
	if err != nil || stopped.Diagnostic.CreationState != creationUnknown {
		t.Fatalf("stop: %+v %v", stopped.Diagnostic, err)
	}
}

func TestRetryHistoryUsesLatestAttemptAfterCreated(t *testing.T) {
	old := migrationAttempt{Job: scheduler.Job{ID: "migration-old"}, CreatedAt: 1, CreationState: creationCreated}
	latest := migrationAttempt{Job: scheduler.Job{ID: "migration-latest"}, CreatedAt: 2, CreationState: creationUnknown}
	history := migrationStartHistory{HasCreated: true, Created: old, HasUnknown: true, Unknown: latest, HasLatest: true, Latest: latest}
	outcome, err := startHistoryOutcome(history, Request{RetryAttempt: old.Job.ID})
	if err != nil || outcome.Kind != startOutcomeConfirmation || outcome.Attempt.Job.ID != latest.Job.ID {
		t.Fatalf("old confirmation with newer unknown = %+v, %v", outcome, err)
	}
	outcome, err = startHistoryOutcome(history, Request{RetryAttempt: latest.Job.ID})
	if err != nil || !outcome.Allowed {
		t.Fatalf("latest confirmation = %+v, %v", outcome, err)
	}
	history.HasUnknown = false
	history.Latest.CreationState = creationNotCreated
	outcome, err = startHistoryOutcome(history, Request{})
	if err != nil || !outcome.Allowed {
		t.Fatalf("newer not-created = %+v, %v", outcome, err)
	}
	outcome, err = startHistoryOutcome(history, Request{RetryAttempt: old.Job.ID})
	if err == nil || outcome.Allowed {
		t.Fatalf("stale confirmation after not-created = %+v, %v", outcome, err)
	}
	history.HasActive = true
	history.Active = latest
	outcome, err = startHistoryOutcome(history, Request{RetryAttempt: old.Job.ID})
	if err != nil || outcome.Kind != startOutcomeActive || outcome.Attempt.Job.ID != latest.Job.ID {
		t.Fatalf("active attempt = %+v, %v", outcome, err)
	}
}
