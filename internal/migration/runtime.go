package migration

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"goaster/internal/diagnostics"
	"goaster/internal/protectedfile"
	"goaster/internal/scheduler"
)

const schedulerMessagePrefix = "goaster_migration_v1:"

type scheduledMigration struct {
	Version   string  `json:"version"`
	JobID     string  `json:"job_id"`
	Request   Request `json:"request"`
	Recipient int64   `json:"recipient"`
	CreatedAt int64   `json:"created_at,omitempty"`
}

// MonitorScheduler is the small existing-scheduler surface needed by the
// migration monitor. The concrete scheduler remains owned by the agent.
type MonitorScheduler interface {
	Add(scheduler.Job) (scheduler.Job, error)
	AddIf(scheduler.Job, func([]scheduler.Job) error) (scheduler.Job, error)
	Get(string) (scheduler.Job, bool)
	List() []scheduler.Job
	SetEnabled(string, bool) (scheduler.Job, error)
}

// monitorExecutionCoordinator is implemented by the scheduler used by the
// agent. Keeping it separate from MonitorScheduler makes an older or test
// scheduler fail closed instead of silently falling back to a non-atomic
// disable-before-create sequence.
type monitorExecutionCoordinator interface {
	ClaimExecutionMatching(context.Context, scheduler.Job) (scheduler.Job, func(error) (scheduler.Job, error), error)
	StopIfNotRunning(string) (scheduler.Job, error)
}

var errStartNotAdded = errors.New("migration start was not added")

const (
	startOutcomeActive       = "active"
	startOutcomeConfirmation = "confirmation"
)

type migrationAttempt struct {
	Job           scheduler.Job
	Scheduled     scheduledMigration
	CreatedAt     int64
	Diagnostic    Diagnostic
	CreationState string
}

type migrationStartHistory struct {
	Attempts     []migrationAttempt
	Latest       migrationAttempt
	HasLatest    bool
	Active       migrationAttempt
	HasActive    bool
	Created      migrationAttempt
	HasCreated   bool
	Unknown      migrationAttempt
	HasUnknown   bool
	Failure      migrationAttempt
	FailureError error
}

type migrationStartOutcome struct {
	Allowed bool
	Kind    string
	Attempt migrationAttempt
}

func monitorIDForOperation(state operationState) string {
	return "migration-" + state.Terms.OperationID
}

func newerMigrationAttempt(candidate, current migrationAttempt) bool {
	if candidate.CreatedAt != current.CreatedAt {
		if current.CreatedAt == 0 {
			return candidate.CreatedAt > 0
		}
		if candidate.CreatedAt == 0 {
			return false
		}
		return candidate.CreatedAt > current.CreatedAt
	}
	return false
}

func ambiguousNewestMigrationAttempts(attempts []migrationAttempt) bool {
	newest := int64(0)
	count := 0
	for _, attempt := range attempts {
		if attempt.CreatedAt <= 0 {
			continue
		}
		if attempt.CreatedAt > newest {
			newest = attempt.CreatedAt
			count = 1
			continue
		}
		if attempt.CreatedAt == newest {
			count++
		}
	}
	return count > 1
}

func migrationHistoryOrderAmbiguousError() error {
	return errors.Join(ErrPersistedStateInvalid, errors.New("migration attempt order is ambiguous"))
}

func nextAttemptCreatedAt(history migrationStartHistory, options Options) int64 {
	createdAt := currentTime(options.Now).UnixNano()
	if history.HasLatest && createdAt <= history.Latest.CreatedAt && history.Latest.CreatedAt < int64(^uint64(0)>>1) {
		createdAt = history.Latest.CreatedAt + 1
	}
	return createdAt
}

func migrationHistoryJob(job scheduler.Job) bool {
	return strings.HasPrefix(job.ID, "migration-") || strings.HasPrefix(job.Message, schedulerMessagePrefix)
}

func migrationAttemptHistoryError(monitorID string, cause error) error {
	if cause == nil {
		cause = errors.New("migration attempt evidence is unavailable")
	}
	if errors.Is(cause, ErrProtectedStorage) || errors.Is(cause, ErrPersistedStateInvalid) {
		return errors.Join(cause, fmt.Errorf("inspect migration attempt %s", monitorID))
	}
	return errors.Join(ErrPersistedStateInvalid, fmt.Errorf("inspect migration attempt %s: %w", monitorID, cause))
}

func historyFailureDiagnostic(monitorID string) Diagnostic {
	diagnostic := diagnosticTemplate(stageStorage, codePersistedStateInvalid)
	diagnostic.MonitorID = monitorID
	diagnostic.ResultState = resultManualReview
	diagnostic.CreationState = creationUnknown
	diagnostic.MonitoringState = monitoringStopped
	diagnostic.MonitoringStopped = true
	return normalizeDiagnostic(diagnostic, stageStorage, Result{MonitorID: monitorID, Status: resultManualReview, Diagnostic: diagnostic})
}

func inspectMigrationHistory(jobs []scheduler.Job, options Options) (migrationStartHistory, error) {
	history := migrationStartHistory{}
	operationDir := ""
	for _, job := range jobs {
		if !migrationHistoryJob(job) {
			continue
		}
		attempt := migrationAttempt{Job: job}
		scheduled, err := parseSchedulerMessage(job.Message)
		if err != nil || scheduled.JobID != job.ID || job.Kind != scheduler.ScheduleEvery || job.EverySeconds != MonitorIntervalSeconds {
			attempt.Diagnostic = historyFailureDiagnostic(job.ID)
			history.Failure = attempt
			history.FailureError = migrationAttemptHistoryError(job.ID, ErrPersistedStateInvalid)
			return history, history.FailureError
		}
		attempt.Scheduled = scheduled
		attempt.CreatedAt = scheduled.CreatedAt
		history.Attempts = append(history.Attempts, attempt)
		if !history.HasLatest || newerMigrationAttempt(attempt, history.Latest) {
			history.Latest = attempt
			history.HasLatest = true
		}
		if job.Enabled || job.Status == scheduler.StatusRunning || job.ExecutionClaim {
			if !history.HasActive || newerMigrationAttempt(attempt, history.Active) {
				history.Active = attempt
				history.HasActive = true
			}
			continue
		}
		if operationDir == "" {
			operationDir, err = operationDirectory(options)
			if err != nil {
				attempt.Diagnostic = historyFailureDiagnostic(job.ID)
				history.Failure = attempt
				history.FailureError = migrationAttemptHistoryError(job.ID, err)
				return history, history.FailureError
			}
		}
		evidence, evidenceErr := loadMonitorDiagnosticEvidence(options, job.ID)
		if evidenceErr != nil || !evidence.Found {
			attempt.Diagnostic = historyFailureDiagnostic(job.ID)
			history.Failure = attempt
			if evidenceErr == nil {
				evidenceErr = errors.New("migration diagnostic history is missing")
			}
			history.FailureError = migrationAttemptHistoryError(job.ID, evidenceErr)
			return history, history.FailureError
		}
		attempt.Diagnostic = evidence.Diagnostic
		if evidence.HasCreated || evidence.Diagnostic.CreationState == creationCreated {
			attempt.CreationState = creationCreated
			if !history.HasCreated || newerMigrationAttempt(attempt, history.Created) {
				history.Created = attempt
				history.HasCreated = true
			}
			continue
		}
		if _, loadErr := loadOperation(operationDir, job.ID, scheduled.Request, scheduled.Recipient); loadErr != nil {
			attempt.Diagnostic = historyFailureDiagnostic(job.ID)
			history.Failure = attempt
			history.FailureError = migrationAttemptHistoryError(job.ID, loadErr)
			return history, history.FailureError
		}
		dispatching, dispatchErr := createDispatchMarkerExists(operationDir, job.ID)
		if dispatchErr != nil {
			attempt.Diagnostic = historyFailureDiagnostic(job.ID)
			history.Failure = attempt
			history.FailureError = migrationAttemptHistoryError(job.ID, dispatchErr)
			return history, history.FailureError
		}
		if dispatching || evidence.HasUnknown || evidence.Diagnostic.CreationState == creationUnknown {
			attempt.CreationState = creationUnknown
			if !history.HasUnknown || newerMigrationAttempt(attempt, history.Unknown) {
				history.Unknown = attempt
				history.HasUnknown = true
			}
			continue
		}
		if evidence.Diagnostic.CreationState == creationNotCreated {
			attempt.CreationState = creationNotCreated
			continue
		}
		attempt.CreationState = creationUnknown
		if !history.HasUnknown || newerMigrationAttempt(attempt, history.Unknown) {
			history.Unknown = attempt
			history.HasUnknown = true
		}
	}
	if ambiguousNewestMigrationAttempts(history.Attempts) {
		history.Failure = migrationAttempt{Diagnostic: historyFailureDiagnostic("")}
		history.FailureError = migrationHistoryOrderAmbiguousError()
		return history, history.FailureError
	}
	legacyCount := 0
	for _, attempt := range history.Attempts {
		if attempt.CreatedAt == 0 {
			legacyCount++
		}
	}
	if legacyCount > 1 {
		history.Failure = migrationAttempt{Diagnostic: historyFailureDiagnostic("")}
		history.FailureError = migrationHistoryOrderAmbiguousError()
		return history, history.FailureError
	}
	return history, nil
}

func createDispatchMarkerExists(directory, monitorID string) (bool, error) {
	path := filepath.Join(directory, monitorID+".create_dispatching")
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil || info == nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0600 {
		return false, errors.New("migration create dispatch marker is invalid")
	}
	content, err := protectedfile.Read(path, 64)
	if err != nil {
		return false, err
	}
	defer clear(content.Data)
	if string(content.Data) != "create_dispatching\n" {
		return false, errors.New("migration create dispatch marker is invalid")
	}
	return true, nil
}

func diagnosticWithDispatchEvidence(options Options, monitorID string, diagnostic Diagnostic) (Diagnostic, error) {
	directory, err := operationDirectory(options)
	if err != nil {
		return diagnostic, err
	}
	dispatching, err := createDispatchMarkerExists(directory, monitorID)
	if err != nil {
		return diagnostic, errors.Join(ErrPersistedStateInvalid, err)
	}
	if dispatching && diagnostic.CreationState != creationCreated {
		diagnostic = diagnosticTemplate(stageCreate, codeManualReview)
		diagnostic.MonitorID = monitorID
		diagnostic.CreationState = creationUnknown
	}
	return diagnostic, nil
}

func startHistoryOutcome(history migrationStartHistory, request Request) (migrationStartOutcome, error) {
	if history.HasActive {
		return migrationStartOutcome{Kind: startOutcomeActive, Attempt: history.Active}, nil
	}
	if history.HasCreated && (!history.HasLatest || !newerMigrationAttempt(history.Latest, history.Created)) {
		target := history.Created
		if request.RetryAttempt != target.Job.ID {
			return migrationStartOutcome{Kind: startOutcomeConfirmation, Attempt: target}, nil
		}
		return migrationStartOutcome{Allowed: true}, nil
	}
	if history.HasUnknown && (!history.HasLatest || !newerMigrationAttempt(history.Latest, history.Unknown)) {
		target := history.Unknown
		if request.RetryAttempt != target.Job.ID {
			return migrationStartOutcome{Kind: startOutcomeConfirmation, Attempt: target}, nil
		}
		return migrationStartOutcome{Allowed: true}, nil
	}
	if request.RetryAttempt != "" {
		return migrationStartOutcome{}, errors.New("migration retry confirmation does not match an uncertain attempt")
	}
	return migrationStartOutcome{Allowed: true}, nil
}

func historyFailureResult(history migrationStartHistory, err error) (Result, error) {
	monitorID := history.Failure.Job.ID
	if !safeJobID(monitorID) {
		monitorID = ""
	}
	diagnostic := history.Failure.Diagnostic
	if diagnostic.MonitorID != monitorID {
		diagnostic = historyFailureDiagnostic(monitorID)
	}
	return failureResult(stageStorage, err, Result{Status: resultManualReview, MonitorID: monitorID, ServerType: history.Failure.Scheduled.Request.ServerType, ManualReview: true, Diagnostic: diagnostic})
}

func currentOwnerMigrationMonitorID(history migrationStartHistory, options Options) (string, error) {
	if options.MigrationRecipient == nil {
		return "", errors.Join(ErrRecipientUnavailable, errors.New("current migration monitor owner is unavailable"))
	}
	recipient, err := options.MigrationRecipient()
	if err != nil || recipient <= 0 {
		return "", errors.Join(ErrRecipientUnavailable, err)
	}
	var latest migrationAttempt
	found := false
	for _, attempt := range history.Attempts {
		if attempt.Scheduled.Recipient != recipient {
			continue
		}
		if !found || newerMigrationAttempt(attempt, latest) {
			latest = attempt
			found = true
		}
	}
	if !found || !safeJobID(latest.Job.ID) {
		return "", errors.Join(ErrMonitorNotFound, errors.New("current migration monitor is unavailable"))
	}
	return latest.Job.ID, nil
}

func startOutcomeResult(outcome migrationStartOutcome, jobs MonitorScheduler, options Options) (Result, error) {
	attempt := outcome.Attempt
	serverType := attempt.Scheduled.Request.ServerType
	if outcome.Kind == startOutcomeConfirmation {
		diagnostic := diagnosticTemplate(stageMonitor, codeRetryConfirmation)
		diagnostic.MonitorID = attempt.Job.ID
		if knownCreationState(attempt.CreationState) {
			diagnostic.CreationState = attempt.CreationState
		}
		diagnostic.MonitoringState = monitoringStopped
		diagnostic.MonitoringStopped = true
		return failureResult(stageMonitor, ErrRetryConfirmationRequired, Result{Status: resultManualReview, ServerType: serverType, MonitorID: attempt.Job.ID, ManualReview: true, Diagnostic: diagnostic})
	}
	if outcome.Kind == startOutcomeActive && (attempt.Job.Status == scheduler.StatusRunning || attempt.Job.ExecutionClaim) {
		diagnostic := diagnosticTemplate(stageMonitor, codeMonitorInProgress)
		diagnostic.MonitorID = attempt.Job.ID
		return failureResult(stageMonitor, scheduler.ErrExecutionInProgress, Result{Status: resultInProgress, ServerType: serverType, MonitorID: attempt.Job.ID, Diagnostic: diagnostic})
	}
	status, err := MigrationStatusWithOptions(attempt.Job.ID, jobs, options)
	status.Migration.Locations = status.Request.Locations
	return status.Migration, err
}

// SchedulerPath returns the private scheduler file used only for migration
// monitor jobs. It is scheduler state, not a migration attempt ledger.
func SchedulerPath() (string, error) {
	root, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("find migration scheduler root: %w", err)
	}
	directory := filepath.Join(root, ".goaster", "migration-monitor")
	if err := ensurePrivatePath(directory); err != nil {
		return "", fmt.Errorf("protect migration scheduler directory: %w", err)
	}
	return filepath.Join(directory, "jobs.json"), nil
}

// StartMonitor adds one non-secret 30-minute monitor and performs its first
// availability check immediately. A ready result disables the exact job before
// one create request. Only the bounded pre-create availability failures handled
// by executeMonitor keep the exact job enabled.
func StartMonitor(ctx context.Context, request Request, options Options, jobs MonitorScheduler) (result Result, returnErr error) {
	defer func() {
		if returnErr != nil {
			diagnostic := diagnosticForFailure(stageMonitor, returnErr, result)
			logMigrationFailure("migration.monitor.start", returnErr, diagnostic)
		}
	}()
	if jobs == nil {
		return failureResult(stageMonitor, errors.New("migration scheduler is unavailable"), Result{})
	}
	if err := validateBillableRequest(request); err != nil {
		return failureResult(stageInput, err, Result{Status: resultRejected, ServerType: request.ServerType})
	}
	options = completeOptions(options)
	if request.JobID != "" && !safeJobID(request.JobID) {
		return failureResult(stageInput, errors.New("migration monitor ID is invalid"), Result{Status: resultRejected, ServerType: request.ServerType, MonitorID: request.JobID})
	}
	if request.RetryAttempt != "" && !safeJobID(request.RetryAttempt) {
		return failureResult(stageInput, errors.New("migration retry attempt ID is invalid"), Result{Status: resultRejected, ServerType: request.ServerType})
	}
	history, err := inspectMigrationHistory(jobs.List(), options)
	if err != nil {
		return historyFailureResult(history, err)
	}
	outcome, err := startHistoryOutcome(history, request)
	if err != nil {
		return failureResult(stageInput, err, Result{Status: resultRejected, ServerType: request.ServerType})
	}
	if !outcome.Allowed {
		return startOutcomeResult(outcome, jobs, options)
	}
	if options.MigrationRecipient == nil || options.PrepareRecipientHandoff == nil {
		return failureResult(stageRecipient, ErrRecipientUnavailable, Result{Status: resultManualReview, ServerType: request.ServerType, ManualReview: true})
	}
	recipient, err := options.MigrationRecipient()
	if err != nil || recipient <= 0 {
		return failureResult(stageRecipient, errors.Join(ErrRecipientUnavailable, err), Result{Status: resultManualReview, ServerType: request.ServerType, ManualReview: true})
	}
	if options.DeliverHandoff == nil && options.PrepareHandoff == nil {
		return failureResult(stageHandoff, ErrHandoffFailure, Result{Status: resultManualReview, ServerType: request.ServerType, MonitorID: request.JobID, ManualReview: true})
	}
	if err := checkHandoffDelivery(options); err != nil {
		return failureResult(stageHandoff, errors.Join(errManualReview, err), Result{Status: resultManualReview, ServerType: request.ServerType, MonitorID: request.JobID, ManualReview: true})
	}
	coordinator, err := monitorExecutionCoordinatorFor(jobs)
	if err != nil {
		return failureResult(stageMonitor, err, Result{Status: resultManualReview, ServerType: request.ServerType, MonitorID: request.JobID, ManualReview: true})
	}
	dir, err := operationDirectory(options)
	if err != nil {
		return failureResult(stageStorage, errors.Join(ErrProtectedStorage, err), Result{Status: resultManualReview, ServerType: request.ServerType, MonitorID: request.JobID, ManualReview: true})
	}
	state, err := newOperation(request, recipient)
	if err != nil {
		return failureResult(stageStorage, err, Result{Status: resultManualReview, ServerType: request.ServerType, MonitorID: request.JobID, ManualReview: true})
	}
	initialID := request.JobID
	if initialID == "" {
		initialID = "migration-singleton"
	}
	candidateID := initialID
	if len(history.Attempts) > 0 {
		candidateID = monitorIDForOperation(state)
	}
	candidateRequest := request
	candidateRequest.JobID = candidateID
	candidateRequest.RetryAttempt = ""
	candidateRequest.AttemptCreatedAt = nextAttemptCreatedAt(history, options)
	message, err := schedulerMessage(candidateRequest, recipient)
	if err != nil {
		clear(state.PrivateKey)
		state.OwnerSecret = ""
		return failureResult(stageMonitor, err, Result{Status: resultManualReview, ServerType: request.ServerType, MonitorID: candidateID, ManualReview: true})
	}
	jobToAdd := scheduler.Job{ID: candidateID, Kind: scheduler.ScheduleEvery, EverySeconds: MonitorIntervalSeconds, Message: message}
	var concurrentHistory migrationStartHistory
	var concurrentOutcome migrationStartOutcome
	var concurrentInputErr error
	added, err := jobs.AddIf(jobToAdd, func(saved []scheduler.Job) error {
		current, inspectErr := inspectMigrationHistory(saved, options)
		if inspectErr != nil {
			concurrentHistory = current
			return errStartNotAdded
		}
		currentOutcome, outcomeErr := startHistoryOutcome(current, request)
		if outcomeErr != nil {
			concurrentInputErr = outcomeErr
			return errStartNotAdded
		}
		if !currentOutcome.Allowed {
			concurrentHistory = current
			concurrentOutcome = currentOutcome
			return errStartNotAdded
		}
		if current.HasLatest != history.HasLatest || (current.HasLatest && current.Latest.Job.ID != history.Latest.Job.ID) {
			concurrentInputErr = errors.New("migration attempt changed while starting")
			return errStartNotAdded
		}
		return nil
	})
	if err != nil {
		clear(state.PrivateKey)
		state.OwnerSecret = ""
		if errors.Is(err, errStartNotAdded) {
			if concurrentInputErr != nil {
				return failureResult(stageInput, concurrentInputErr, Result{Status: resultRejected, ServerType: request.ServerType})
			}
			if concurrentHistory.FailureError != nil {
				return historyFailureResult(concurrentHistory, concurrentHistory.FailureError)
			}
			if !concurrentOutcome.Allowed {
				return startOutcomeResult(concurrentOutcome, jobs, options)
			}
			return failureResult(stageMonitor, errors.New("migration attempt changed while starting"), Result{Status: resultManualReview, ServerType: request.ServerType, ManualReview: true})
		}
		return failureResult(stageMonitor, fmt.Errorf("add migration monitor: %w", err), Result{Status: resultManualReview, ServerType: request.ServerType, MonitorID: candidateID, ManualReview: true})
	}
	if added.ID != candidateID || added.Message != message || !added.Enabled || added.Kind != scheduler.ScheduleEvery || added.EverySeconds != MonitorIntervalSeconds {
		verifyErr := errors.New("migration monitor could not be verified")
		if stopErr := stopAndVerifyMonitor(coordinator, candidateID); stopErr != nil {
			failed := Result{Status: resultStopUnconfirmed, MonitorID: candidateID, ServerType: request.ServerType, Diagnostic: unconfirmedStopDiagnostic(Diagnostic{}, candidateID)}
			return failed, failureWithDiagnostic(failed.Diagnostic, errors.Join(ErrMonitorStopUnconfirmed, verifyErr, stopErr))
		}
		failed := Result{Status: resultManualReview, MonitorID: candidateID, ServerType: request.ServerType, ManualReview: true, Diagnostic: stoppedMonitorDiagnostic(initialMonitorDiagnostic(candidateID), candidateID)}
		return failed, failureWithDiagnostic(failed.Diagnostic, verifyErr)
	}
	if verified, ok := jobs.Get(candidateID); !ok || verified.Message != message || !verified.Enabled || verified.Kind != scheduler.ScheduleEvery || verified.EverySeconds != MonitorIntervalSeconds {
		verifyErr := errors.New("migration monitor could not be verified")
		if stopErr := stopAndVerifyMonitor(coordinator, candidateID); stopErr != nil {
			failed := Result{Status: resultStopUnconfirmed, MonitorID: candidateID, ServerType: request.ServerType, Diagnostic: unconfirmedStopDiagnostic(Diagnostic{}, candidateID)}
			return failed, failureWithDiagnostic(failed.Diagnostic, errors.Join(ErrMonitorStopUnconfirmed, verifyErr, stopErr))
		}
		failed := Result{Status: resultManualReview, MonitorID: candidateID, ServerType: request.ServerType, ManualReview: true, Diagnostic: stoppedMonitorDiagnostic(initialMonitorDiagnostic(candidateID), candidateID)}
		return failed, failureWithDiagnostic(failed.Diagnostic, verifyErr)
	}
	if err := storeOperation(dir, candidateID, state); err != nil {
		failed := Result{Status: resultManualReview, MonitorID: candidateID, ServerType: request.ServerType, ManualReview: true, Diagnostic: diagnosticTemplate(stageStorage, codeProtectedStorage)}
		failed.Diagnostic.MonitorID = candidateID
		if stopErr := stopAndVerifyMonitor(coordinator, candidateID); stopErr != nil {
			failed.Status = resultStopUnconfirmed
			failed.ManualReview = false
			failed.Diagnostic = unconfirmedStopDiagnostic(failed.Diagnostic, candidateID)
			return failed, failureWithDiagnostic(failed.Diagnostic, errors.Join(ErrMonitorStopUnconfirmed, ErrProtectedStorage, err))
		}
		failed.Diagnostic.MonitoringState = monitoringStopped
		failed.Diagnostic.MonitoringStopped = true
		return failed, failureWithDiagnostic(failed.Diagnostic, errors.Join(ErrProtectedStorage, fmt.Errorf("store migration operation: %w", err)))
	}
	initial := initialMonitorDiagnostic(candidateID)
	if err := storeMonitorDiagnostic(options, initial); err != nil {
		failed := Result{Status: resultManualReview, MonitorID: candidateID, ServerType: request.ServerType, ManualReview: true, Diagnostic: diagnosticForFailure(stageStorage, err, Result{Status: resultManualReview, MonitorID: candidateID})}
		if stopErr := stopAndVerifyMonitor(coordinator, candidateID); stopErr != nil {
			failed.Status = resultStopUnconfirmed
			failed.ManualReview = false
			failed.Diagnostic = unconfirmedStopDiagnostic(failed.Diagnostic, candidateID)
			return failed, failureWithDiagnostic(failed.Diagnostic, errors.Join(ErrMonitorStopUnconfirmed, err, stopErr))
		}
		failed.Diagnostic.MonitoringState = monitoringStopped
		failed.Diagnostic.MonitoringStopped = true
		return failed, failureWithDiagnostic(failed.Diagnostic, err)
	}

	result, runErr := executeMonitor(ctx, candidateRequest, recipient, options, coordinator, candidateID)
	result.MonitorID = candidateID
	if runErr == nil {
		return NormalizeResult(result), nil
	}
	if err := stopAndVerifyMonitor(coordinator, candidateID); err != nil {
		result.Status = resultStopUnconfirmed
		result.Diagnostic = unconfirmedStopDiagnostic(result.Diagnostic, candidateID)
		return result, failureWithDiagnostic(result.Diagnostic, errors.Join(ErrMonitorStopUnconfirmed, runErr, err))
	}
	result.ManualReview = true
	if result.Status != resultCreated {
		result.Status = resultManualReview
	}
	result.Diagnostic.MonitoringState = monitoringStopped
	result.Diagnostic.MonitoringStopped = true
	return NormalizeResult(result), failureWithDiagnostic(result.Diagnostic, runErr)
}

func availabilityFailureMayRetry(err error) bool {
	if err == nil || errors.Is(err, context.Canceled) {
		return false
	}
	if findDiagnosticCarrier(err).Code != "" {
		return false
	}
	for _, terminal := range []error{ErrCredentialStorageUnreadable, ErrHetznerTokenMissing, ErrHetznerTokenInvalid, ErrProtectedStorage, ErrInvalidResponse, ErrPersistedStateInvalid, errInvalidRequest, errManualReview} {
		if errors.Is(err, terminal) {
			return false
		}
	}
	var webResponse webResponseError
	if errors.As(err, &webResponse) {
		return false
	}
	var providerResponse providerResponseError
	if errors.As(err, &providerResponse) {
		return false
	}
	var providerStatus providerHTTPError
	if errors.As(err, &providerStatus) {
		return false
	}
	var providerTransport providerTransportError
	if errors.As(err, &providerTransport) {
		return false
	}
	var status webHTTPError
	if errors.As(err, &status) {
		return availabilityHTTPErrorMayRetry(status)
	}
	var transport webTransportError
	if !errors.As(err, &transport) {
		return false
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	var networkError net.Error
	return errors.As(err, &networkError) && (networkError.Timeout() || networkError.Temporary())
}

// availabilityHTTPErrorMayRetry requires a decoded v1 envelope with a known
// transient cause. A status-only fallback has no provenance and stays
// terminal, including plain gateway 502/503/504 responses.
func availabilityHTTPErrorMayRetry(status webHTTPError) bool {
	if status.Code == "" {
		return false
	}
	switch status.Code {
	case "rate_limited":
		return status.Status == http.StatusTooManyRequests &&
			(status.Stage == "service" && status.ProviderCode == "" ||
				status.Stage == "provider" && status.ProviderCode == "rate_limit_exceeded")
	case "unavailable":
		return transientAvailabilityHTTPStatus(status.Status) &&
			(status.Stage == "service" && status.ProviderCode == "" ||
				status.Stage == "provider" && status.ProviderCode == "resource_unavailable")
	case "provider_timeout":
		return transientAvailabilityHTTPStatus(status.Status) && status.Stage == "provider" && status.ProviderCode == ""
	default:
		return false
	}
}

func transientAvailabilityHTTPStatus(status int) bool {
	switch status {
	case http.StatusInternalServerError, http.StatusBadGateway, http.StatusServiceUnavailable, http.StatusGatewayTimeout:
		return true
	default:
		return false
	}
}

func availabilityRetryResult(err error, monitorID, serverType string, count int) Result {
	diagnostic := diagnosticForFailure(stageAvailability, err, Result{
		Status:    resultManualReview,
		MonitorID: monitorID,
		Diagnostic: Diagnostic{
			Stage:         stageAvailability,
			CreationState: creationNotCreated,
		},
	})
	diagnostic.MonitorID = monitorID
	diagnostic.ResultState = resultMonitoring
	diagnostic.CreationState = creationNotCreated
	diagnostic.MonitoringState = monitoringRunning
	diagnostic.MonitoringStopped = false
	diagnostic.RetryCount = count
	diagnostic.RetryState = retryStatePending
	return NormalizeResult(Result{Status: resultMonitoring, ServerType: serverType, MonitorID: monitorID, Diagnostic: diagnostic})
}

func availabilityRetryExhaustedResult(err error, monitorID, serverType string) (Result, error) {
	base := Diagnostic{}
	if err != nil {
		base = diagnosticForFailure(stageAvailability, err, Result{
			Status:     resultManualReview,
			MonitorID:  monitorID,
			Diagnostic: Diagnostic{Stage: stageAvailability, CreationState: creationNotCreated},
		})
	}
	diagnostic := diagnosticTemplate(stageAvailability, codeAvailabilityRetryExhausted)
	diagnostic.MonitorID = monitorID
	diagnostic.HTTPStatus = base.HTTPStatus
	diagnostic.ProviderCode = base.ProviderCode
	diagnostic.ResultState = resultManualReview
	diagnostic.CreationState = creationNotCreated
	diagnostic.MonitoringState = monitoringUnknown
	diagnostic.MonitoringStopped = false
	diagnostic.RetryCount = availabilityRetryLimit
	diagnostic.RetryState = retryStateExhausted
	result := NormalizeResult(Result{Status: resultManualReview, ServerType: serverType, MonitorID: monitorID, ManualReview: true, Diagnostic: diagnostic})
	cause := errAvailabilityRetryBudgetExhausted
	if err != nil {
		cause = errors.Join(errAvailabilityRetryBudgetExhausted, fmt.Errorf("availability check failed: %w", err))
	}
	return result, failureWithDiagnostic(result.Diagnostic, cause)
}

func retainedCreationEvidenceFailure(monitorID, serverType, state string) (Result, error) {
	diagnostic := diagnosticTemplate(stageMonitor, codeManualReview)
	diagnostic.MonitorID = monitorID
	diagnostic.ResultState = resultManualReview
	diagnostic.CreationState = state
	diagnostic.MonitoringState = monitoringUnknown
	diagnostic.MonitoringStopped = false
	result := NormalizeResult(Result{Status: resultManualReview, ServerType: serverType, MonitorID: monitorID, ManualReview: true, Diagnostic: diagnostic})
	return result, failureWithDiagnostic(result.Diagnostic, errors.Join(errManualReview, errors.New("durable migration creation evidence prevents another create")))
}

func executeMonitor(ctx context.Context, request Request, recipient int64, options Options, coordinator monitorExecutionCoordinator, jobID string) (result Result, runErr error) {
	if coordinator == nil {
		return failureResult(stageMonitor, errors.New("migration monitor execution claim capability is unavailable"), Result{Status: resultManualReview, ServerType: request.ServerType, MonitorID: jobID})
	}
	message, err := schedulerMessage(request, recipient)
	if err != nil {
		return failureResult(stageMonitor, err, Result{Status: resultManualReview, ServerType: request.ServerType, MonitorID: jobID})
	}
	_, complete, err := coordinator.ClaimExecutionMatching(ctx, scheduler.Job{ID: jobID, Message: message, Kind: scheduler.ScheduleEvery, EverySeconds: MonitorIntervalSeconds})
	if err != nil {
		return failureResult(stageMonitor, fmt.Errorf("claim migration monitor execution: %w", err), Result{Status: resultInProgress, ServerType: request.ServerType, MonitorID: jobID, Diagnostic: diagnosticTemplate(stageMonitor, codeMonitorInProgress)})
	}
	initialDiagnostic := diagnosticTemplate(stageMonitor, codeMonitorInProgress)
	initialDiagnostic.CreationState = creationNotCreated
	result = Result{Status: resultManualReview, ServerType: request.ServerType, MonitorID: jobID, Diagnostic: initialDiagnostic}
	persistMonitoringDiagnostic := false
	var dir string
	var stored Diagnostic
	preflightResult, preflightErr := func() (Result, error) {
		var err error
		dir, err = operationDirectory(options)
		if err != nil {
			unknown := result
			unknown.Diagnostic.CreationState = creationUnknown
			return failureResult(stageStorage, errors.Join(ErrProtectedStorage, err), unknown)
		}
		dispatching, err := createDispatchMarkerExists(dir, jobID)
		if err != nil {
			unknown := result
			unknown.Diagnostic.CreationState = creationUnknown
			return failureResult(stageStorage, errors.Join(ErrPersistedStateInvalid, err), unknown)
		}
		evidence, err := loadMonitorDiagnosticEvidence(options, jobID)
		if err != nil {
			unknown := result
			unknown.Diagnostic.CreationState = creationUnknown
			return failureResult(stageStorage, err, unknown)
		}
		if evidence.HasCreated || evidence.Diagnostic.CreationState == creationCreated {
			return retainedCreationEvidenceFailure(jobID, request.ServerType, creationCreated)
		}
		if dispatching || evidence.HasUnknown || (evidence.Diagnostic.CreationState == creationUnknown && evidence.Diagnostic.Code != codeMonitorStarted) {
			return retainedCreationEvidenceFailure(jobID, request.ServerType, creationUnknown)
		}
		stored = initialMonitorDiagnostic(jobID)
		if evidence.Found {
			stored = NormalizeResult(Result{Status: evidence.Diagnostic.ResultState, MonitorID: jobID, Diagnostic: evidence.Diagnostic}).Diagnostic
		}
		if stored.RetryState == retryStateExhausted || stored.RetryCount >= availabilityRetryLimit {
			return availabilityRetryExhaustedResult(nil, jobID, request.ServerType)
		}
		return Result{}, nil
	}()
	defer func() {
		if result.Status != resultMonitoring {
			result.Diagnostic = DiagnosticFor(runErr, result)
			result.Diagnostic.MonitorID = jobID
			if result.Diagnostic.CreationState != creationCreated {
				dir, markerErr := operationDirectory(options)
				if markerErr == nil {
					var dispatching bool
					dispatching, markerErr = createDispatchMarkerExists(dir, jobID)
					if dispatching {
						result.Diagnostic.CreationState = creationUnknown
					}
				}
				if markerErr != nil {
					result.Diagnostic.CreationState = creationUnknown
					runErr = errors.Join(runErr, markerErr)
				}
			}
			result.Diagnostic.MonitoringState = monitoringUnknown
			result.Diagnostic.MonitoringStopped = false
			if storeErr := storeMonitorDiagnostic(options, result.Diagnostic); storeErr != nil {
				runErr = errors.Join(runErr, storeErr)
				result.Status = resultManualReview
				result.ManualReview = true
				result.Diagnostic = diagnosticForFailure(stageStorage, storeErr, result)
				result.Diagnostic.MonitorID = jobID
				result.Diagnostic.MonitoringState = monitoringUnknown
				result.Diagnostic.MonitoringStopped = false
			}
		} else if persistMonitoringDiagnostic {
			result.Diagnostic.MonitorID = jobID
			if storeErr := storeMonitorDiagnostic(options, result.Diagnostic); storeErr != nil {
				runErr = errors.Join(runErr, storeErr)
				result.Status = resultManualReview
				result.ManualReview = true
				result.Diagnostic = diagnosticForFailure(stageStorage, storeErr, result)
				result.Diagnostic.MonitorID = jobID
				result.Diagnostic.MonitoringState = monitoringUnknown
				result.Diagnostic.MonitoringStopped = false
			}
		}
		if complete != nil {
			completionErr := runErr
			if runErr != nil {
				completionErr = scheduler.ReviewRequiredError{Message: "migration monitor requires manual review", Cause: runErr}
			}
			if runErr == nil && result.Status == "monitoring" {
				completionErr = scheduler.ErrRetry
			}
			completed, err := complete(completionErr)
			if err != nil {
				result.Status = "manual_review"
				result.ManualReview = true
				runErr = errors.Join(runErr, fmt.Errorf("persist migration monitor execution result: %w", err))
				result.Diagnostic = diagnosticForFailure(stageStorage, runErr, result)
			} else if result.Status != resultMonitoring {
				result.Diagnostic.MonitoringState = monitoringStopped
				result.Diagnostic.MonitoringStopped = completed.ID == jobID && !completed.Enabled && completed.Status != scheduler.StatusRunning
				if !result.Diagnostic.MonitoringStopped {
					result.Diagnostic.MonitoringState = monitoringUnknown
				}
			} else if completed.Enabled && completed.Status != scheduler.StatusRunning {
				result.Diagnostic.MonitoringState = monitoringRunning
				result.Diagnostic.MonitoringStopped = false
			}
		}
	}()
	if preflightErr != nil {
		return preflightResult, preflightErr
	}
	if options.MigrationRecipient == nil || options.PrepareRecipientHandoff == nil {
		return failureResult(stageRecipient, ErrRecipientUnavailable, result)
	}
	current, err := options.MigrationRecipient()
	if err != nil || current <= 0 {
		return failureResult(stageRecipient, errors.Join(ErrRecipientUnavailable, err), result)
	}
	if current != recipient {
		return failureResult(stageRecipient, ErrRecipientChanged, result)
	}
	options.CheckHandoffDelivery = func() error {
		current, err := options.MigrationRecipient()
		if err != nil || current <= 0 {
			return errors.Join(ErrRecipientUnavailable, err)
		}
		if current != recipient {
			return ErrRecipientChanged
		}
		return nil
	}
	options.PrepareHandoff = func() (func(context.Context, []byte) error, error) { return options.PrepareRecipientHandoff(recipient) }
	token, err := options.LoadHetznerToken()
	if err != nil {
		return failureResult(stageConfiguration, fmt.Errorf("load migration provider token: %w", err), result)
	}
	if !validHetznerToken(token) {
		return failureResult(stageConfiguration, ErrHetznerTokenInvalid, result)
	}
	state, err := loadOperation(dir, jobID, request, recipient)
	if err != nil {
		return failureResult(stageStorage, errors.Join(ErrProtectedStorage, fmt.Errorf("load migration operation: %w", err)), result)
	}
	availability, err := availabilityWithToken(ctx, state.Terms, token, options)
	if err != nil {
		if availabilityFailureMayRetry(err) {
			count := 1
			if stored.RetryState == retryStatePending {
				count = stored.RetryCount + 1
			}
			if count < availabilityRetryLimit {
				retry := availabilityRetryResult(err, jobID, request.ServerType, count)
				logMigrationFailure("migration.monitor.availability.retry", err, retry.Diagnostic)
				persistMonitoringDiagnostic = true
				return retry, nil
			}
			return availabilityRetryExhaustedResult(err, jobID, request.ServerType)
		}
		return failureResult(stageAvailability, fmt.Errorf("check migration availability: %w", err), result)
	}
	if len(availability.Locations) == 0 {
		result.Status = resultMonitoring
		result.Diagnostic = initialMonitorDiagnostic(jobID)
		if stored.RetryState == retryStatePending || stored.RetryCount != 0 {
			persistMonitoringDiagnostic = true
		}
		return result, nil
	}
	if err := markDispatch(dir, jobID); err != nil {
		return failureResult(stageStorage, err, result)
	}
	created, createErr := createWithToken(ctx, request, state, token, options)
	if createErr != nil {
		created.ManualReview = true
		created.MonitorID = jobID
		created.Diagnostic.MonitorID = jobID
		created.Diagnostic.MonitoringState = monitoringUnknown
		created.Diagnostic.MonitoringStopped = false
		return created, failureWithDiagnostic(created.Diagnostic, createErr)
	}
	created.MonitorID = jobID
	created.Diagnostic.MonitorID = jobID
	created.Diagnostic.MonitoringState = monitoringUnknown
	created.Diagnostic.MonitoringStopped = false
	return created, nil
}

// ScheduledExecutor runs only a verified migration scheduler message. A
// read-only unavailable result keeps the job enabled; every other result or
// error leaves it disabled and requires manual review.
func ScheduledExecutor(ctx context.Context, message string, jobs MonitorScheduler, options Options) error {
	review := func(cause error) error {
		diagnostics.Error("migration.monitor.scheduled", cause, diagnostics.Field{Key: "reason", Value: "migration monitor requires manual review"})
		return scheduler.ReviewRequiredError{Message: "migration monitor requires manual review", Cause: cause}
	}
	if jobs == nil {
		return review(failureError(stageMonitor, errors.New("migration scheduler is unavailable"), Result{}))
	}
	scheduled, err := parseSchedulerMessage(message)
	if err != nil {
		return review(failureError(stageMonitor, err, Result{}))
	}
	job, ok := jobs.Get(scheduled.JobID)
	if !ok || !job.Enabled || job.ReviewRequired || job.Message != message || job.Kind != scheduler.ScheduleEvery || job.EverySeconds != MonitorIntervalSeconds {
		return review(failureError(stageMonitor, errors.New("migration scheduler job is not the approved monitor"), Result{MonitorID: scheduled.JobID, ServerType: scheduled.Request.ServerType}))
	}
	coordinator, err := monitorExecutionCoordinatorFor(jobs)
	if err != nil {
		return review(failureError(stageMonitor, err, Result{MonitorID: scheduled.JobID, ServerType: scheduled.Request.ServerType}))
	}
	runOptions := completeOptions(options)
	result, runErr := executeMonitor(ctx, scheduled.Request, scheduled.Recipient, runOptions, coordinator, scheduled.JobID)
	result.MonitorID = scheduled.JobID
	if runErr != nil {
		result.Diagnostic = DiagnosticFor(runErr, result)
		result.Diagnostic.MonitorID = scheduled.JobID
		// A scheduled claim is finalized by the owning scheduler after this
		// function returns. Until that transaction is observed, never claim that
		// the monitor is stopped.
		result.Diagnostic.MonitoringState = monitoringUnknown
		result.Diagnostic.MonitoringStopped = false
		return review(failureWithDiagnostic(result.Diagnostic, runErr))
	}
	result = NormalizeResult(result)
	if result.Status == resultMonitoring {
		return scheduler.ErrRetry
	}
	return nil
}

// MigrationStatus reads only scheduler state and its non-secret request.
func MigrationStatus(jobID string, jobs MonitorScheduler) (StatusResult, error) {
	return MigrationStatusWithOptions(jobID, jobs, DefaultOptions())
}

// MigrationStatusWithOptions also reads the secret-free retained diagnostic
// record from the same private operation directory used by the monitor.
func MigrationStatusWithOptions(jobID string, jobs MonitorScheduler, options Options) (result StatusResult, returnErr error) {
	result.MonitorID = jobID
	defer func() {
		if returnErr != nil {
			diagnostic := diagnosticForFailure(stageStatus, returnErr, result.Migration)
			logMigrationFailure("migration.monitor.status", returnErr, diagnostic)
		}
	}()
	fail := func(err error, migration Result) (StatusResult, error) {
		failed, failureErr := failureResult(stageStatus, err, migration)
		result.Migration = failed
		result.Diagnostic = failed.Diagnostic
		result.MonitorID = failed.MonitorID
		return result, failureErr
	}
	if jobs == nil {
		return fail(errors.New("migration status is unavailable"), Result{Status: resultUnavailable, MonitorID: jobID})
	}
	if jobID == "" {
		history, historyErr := inspectMigrationHistory(jobs.List(), options)
		if historyErr != nil {
			failed, failureErr := historyFailureResult(history, historyErr)
			result.Migration = failed
			result.Diagnostic = failed.Diagnostic
			result.MonitorID = failed.MonitorID
			return result, failureErr
		}
		ownerMonitorID, ownerErr := currentOwnerMigrationMonitorID(history, options)
		if ownerErr != nil {
			return fail(ownerErr, Result{Status: resultUnavailable})
		}
		jobID = ownerMonitorID
		result.MonitorID = jobID
	}
	if !safeJobID(jobID) {
		return fail(errors.New("migration status is unavailable"), Result{Status: resultUnavailable, MonitorID: jobID})
	}
	job, ok := jobs.Get(jobID)
	if !ok {
		return fail(errors.Join(ErrMonitorNotFound, errors.New("migration status is unavailable")), Result{Status: resultUnavailable, MonitorID: jobID})
	}
	scheduled, err := parseSchedulerMessage(job.Message)
	if err != nil || scheduled.JobID != jobID {
		return fail(errors.Join(ErrInvalidResponse, errors.New("migration status is unavailable")), Result{Status: resultManualReview, MonitorID: jobID})
	}
	stored, found, err := loadMonitorDiagnostic(options, jobID)
	if err != nil {
		return fail(err, Result{Status: resultManualReview, ServerType: scheduled.Request.ServerType, MonitorID: jobID, ManualReview: true})
	}
	stored, err = diagnosticWithDispatchEvidence(options, jobID, stored)
	if err != nil {
		return fail(err, Result{Status: resultManualReview, ServerType: scheduled.Request.ServerType, MonitorID: jobID, ManualReview: true, Diagnostic: historyFailureDiagnostic(jobID)})
	}
	diagnostic := statusDiagnosticForJob(job.ID, job.Status, job.Enabled, job.ReviewRequired, stored, found)
	migrationResult := NormalizeResult(Result{Status: statusFromJob(job), ServerType: scheduled.Request.ServerType, MonitorID: job.ID, ManualReview: job.ReviewRequired, Diagnostic: diagnostic})
	result = NormalizeStatusResult(StatusResult{
		MonitorID:      job.ID,
		MonitorOn:      job.Enabled,
		SchedulerState: job.Status,
		Migration:      migrationResult,
		Diagnostic:     migrationResult.Diagnostic,
		Request:        scheduled.Request,
	})
	return result, nil
}

// StopMonitor disables only the exact local monitor. It does not contact the
// provider or GoAsterWeb and does not retry or delete infrastructure.
func StopMonitor(jobID string, jobs MonitorScheduler) (Result, error) {
	return StopMonitorWithOptions(jobID, jobs, DefaultOptions())
}

func StopMonitorWithOptions(jobID string, jobs MonitorScheduler, options Options) (result Result, returnErr error) {
	result.MonitorID = jobID
	defer func() {
		if returnErr != nil {
			diagnostic := diagnosticForFailure(stageStop, returnErr, result)
			logMigrationFailure("migration.monitor.stop", returnErr, diagnostic)
		}
	}()
	if jobs == nil || !safeJobID(jobID) {
		return failureResult(stageStop, errors.Join(ErrMonitorNotFound, errors.New("migration monitor is unavailable")), Result{Status: resultUnavailable, MonitorID: jobID})
	}
	job, ok := jobs.Get(jobID)
	if !ok {
		return failureResult(stageStop, errors.Join(ErrMonitorNotFound, errors.New("migration monitor is unavailable")), Result{Status: resultUnavailable, MonitorID: jobID})
	}
	scheduled, err := parseSchedulerMessage(job.Message)
	if err != nil || scheduled.JobID != jobID {
		return failureResult(stageStop, errors.Join(ErrInvalidResponse, errors.New("migration monitor is unavailable")), Result{Status: resultManualReview, MonitorID: jobID, ManualReview: true})
	}
	stored := Diagnostic{CreationState: creationUnknown}
	coordinator, err := monitorExecutionCoordinatorFor(jobs)
	if err != nil {
		return failureResult(stageStop, fmt.Errorf("stop migration monitor: %w", err), Result{Status: resultManualReview, ServerType: scheduled.Request.ServerType, MonitorID: jobID, ManualReview: true, Diagnostic: stored})
	}
	if err := stopAndVerifyMonitor(coordinator, jobID); err != nil {
		if errors.Is(err, scheduler.ErrExecutionInProgress) {
			inProgress := Result{Status: resultInProgress, ServerType: scheduled.Request.ServerType, MonitorID: jobID, Diagnostic: diagnosticTemplate(stageStop, codeMonitorInProgress)}
			inProgress.Diagnostic.MonitorID = jobID
			return inProgress, failureWithDiagnostic(inProgress.Diagnostic, err)
		}
		failed := Result{Status: resultStopUnconfirmed, ServerType: scheduled.Request.ServerType, MonitorID: jobID, ManualReview: true, Diagnostic: unconfirmedStopDiagnostic(stored, jobID)}
		stopErr := errors.Join(ErrMonitorStopUnconfirmed, fmt.Errorf("disable migration monitor: %w", err))
		return failed, failureWithDiagnostic(failed.Diagnostic, stopErr)
	}
	stored, found, historyErr := loadMonitorDiagnostic(options, jobID)
	if historyErr != nil {
		stopped := stoppedMonitorDiagnostic(Diagnostic{CreationState: creationUnknown}, jobID)
		stoppedResult := NormalizeResult(Result{Status: resultStopped, ServerType: scheduled.Request.ServerType, MonitorID: jobID, Diagnostic: stopped})
		return failureResult(stageStop, historyErr, stoppedResult)
	}
	if !found {
		stored = initialMonitorDiagnostic(jobID)
	}
	stored, historyErr = diagnosticWithDispatchEvidence(options, jobID, stored)
	if historyErr != nil {
		return failureResult(stageStorage, historyErr, Result{Status: resultManualReview, ServerType: scheduled.Request.ServerType, MonitorID: jobID, ManualReview: true, Diagnostic: historyFailureDiagnostic(jobID)})
	}
	stopped := stoppedMonitorDiagnostic(stored, jobID)
	stoppedResult := NormalizeResult(Result{Status: resultStopped, ServerType: scheduled.Request.ServerType, MonitorID: jobID, Diagnostic: stopped})
	if stored.Code == codeMonitorStopped && stored.MonitoringStopped && stored.CreationState == stoppedResult.Diagnostic.CreationState {
		return stoppedResult, nil
	}
	if err := storeMonitorDiagnostic(options, stoppedResult.Diagnostic); err != nil {
		return failureResult(stageStorage, err, stoppedResult)
	}
	return stoppedResult, nil
}

func schedulerMessage(request Request, recipient ...int64) (string, error) {
	var id int64
	if len(recipient) > 0 {
		id = recipient[0]
	}
	data, err := json.Marshal(scheduledMigration{Version: "v1", JobID: request.JobID, Request: request, Recipient: id, CreatedAt: request.AttemptCreatedAt})
	if err != nil {
		return "", fmt.Errorf("encode migration monitor message: %w", err)
	}
	return schedulerMessagePrefix + string(data), nil
}

func parseSchedulerMessage(message string) (scheduledMigration, error) {
	if !strings.HasPrefix(message, schedulerMessagePrefix) {
		return scheduledMigration{}, errors.New("migration scheduler message is invalid")
	}
	decoder := json.NewDecoder(strings.NewReader(strings.TrimPrefix(message, schedulerMessagePrefix)))
	decoder.DisallowUnknownFields()
	var scheduled scheduledMigration
	if err := decoder.Decode(&scheduled); err != nil {
		return scheduledMigration{}, errors.New("migration scheduler message is invalid")
	}
	var extra json.RawMessage
	if err := decoder.Decode(&extra); err != io.EOF {
		return scheduledMigration{}, errors.New("migration scheduler message contains trailing data")
	}
	if scheduled.Version != "v1" || scheduled.Recipient <= 0 || scheduled.CreatedAt < 0 || !safeJobID(scheduled.JobID) || scheduled.Request.JobID != scheduled.JobID || validateMachineRequest(scheduled.Request.ServerType, scheduled.Request.Locations) != nil {
		return scheduledMigration{}, errors.New("migration scheduler message is invalid")
	}
	if err := rejectLegacyPriceApproval(scheduled.Request); err != nil {
		return scheduledMigration{}, err
	}
	scheduled.Request.AttemptCreatedAt = scheduled.CreatedAt
	return scheduled, nil
}

func safeJobID(value string) bool {
	if len(value) <= len("migration-") || !strings.HasPrefix(value, "migration-") || len(value) > 96 || !safeName(value, 96) {
		return false
	}
	for _, character := range value {
		if !(character >= 'a' && character <= 'z' || character >= '0' && character <= '9' || character == '-') {
			return false
		}
	}
	return true
}

func safeCurrency(value string) bool {
	if len(value) != 3 {
		return false
	}
	for _, character := range value {
		if character < 'A' || character > 'Z' {
			return false
		}
	}
	return true
}

func monitorExecutionCoordinatorFor(jobs MonitorScheduler) (monitorExecutionCoordinator, error) {
	coordinator, ok := jobs.(monitorExecutionCoordinator)
	if !ok || coordinator == nil {
		return nil, errors.New("migration monitor execution claim capability is unavailable")
	}
	return coordinator, nil
}

func stopAndVerifyMonitor(coordinator monitorExecutionCoordinator, id string) error {
	job, err := coordinator.StopIfNotRunning(id)
	if err != nil {
		return err
	}
	if job.ID != id || job.Enabled || job.Status == scheduler.StatusRunning {
		return errors.New("migration monitor disable could not be verified")
	}
	return nil
}

func statusFromJob(job scheduler.Job) string {
	if job.ReviewRequired || job.Status == scheduler.StatusFailed || job.Status == scheduler.StatusRunning {
		return "manual_review"
	}
	if job.Enabled {
		return "monitoring"
	}
	return "stopped"
}
