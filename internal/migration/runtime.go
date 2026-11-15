package migration

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"goaster/internal/diagnostics"
	"goaster/internal/scheduler"
)

const schedulerMessagePrefix = "goaster_migration_v1:"

type scheduledMigration struct {
	Version   string  `json:"version"`
	JobID     string  `json:"job_id"`
	Request   Request `json:"request"`
	Recipient int64   `json:"recipient"`
}

// MonitorScheduler is the small existing-scheduler surface needed by the
// migration monitor. The concrete scheduler remains owned by the agent.
type MonitorScheduler interface {
	Add(scheduler.Job) (scheduler.Job, error)
	Get(string) (scheduler.Job, bool)
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
// one create request. Any post-add failure is returned as manual review and is
// never eligible for automatic retry.
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
	if request.JobID == "" {
		// A single durable slot makes repeated model start calls converge.
		request.JobID = "migration-singleton"
	}
	if !safeJobID(request.JobID) {
		return failureResult(stageInput, errors.New("migration monitor ID is invalid"), Result{Status: resultRejected, ServerType: request.ServerType, MonitorID: request.JobID})
	}
	if job, exists := jobs.Get(request.JobID); exists {
		if job.Status == scheduler.StatusRunning {
			return failureResult(stageMonitor, scheduler.ErrExecutionInProgress, Result{Status: resultInProgress, MonitorID: request.JobID, ServerType: request.ServerType, Diagnostic: diagnosticTemplate(stageMonitor, codeMonitorInProgress)})
		}
		if !job.Enabled || job.ReviewRequired {
			status, err := MigrationStatusWithOptions(request.JobID, jobs, options)
			status.Migration.Locations = status.Request.Locations
			return status.Migration, err
		}
	}
	if options.MigrationRecipient == nil || options.PrepareRecipientHandoff == nil {
		return failureResult(stageRecipient, ErrRecipientUnavailable, Result{Status: resultManualReview, ServerType: request.ServerType, MonitorID: request.JobID, ManualReview: true})
	}
	recipient, err := options.MigrationRecipient()
	if err != nil || recipient <= 0 {
		return failureResult(stageRecipient, errors.Join(ErrRecipientUnavailable, err), Result{Status: resultManualReview, ServerType: request.ServerType, MonitorID: request.JobID, ManualReview: true})
	}
	message, err := schedulerMessage(request, recipient)
	if err != nil {
		return failureResult(stageMonitor, err, Result{Status: resultManualReview, ServerType: request.ServerType, MonitorID: request.JobID, ManualReview: true})
	}
	job, exists := jobs.Get(request.JobID)
	if exists {
		if job.Status == scheduler.StatusRunning {
			return failureResult(stageMonitor, scheduler.ErrExecutionInProgress, Result{Status: resultInProgress, MonitorID: request.JobID, ServerType: request.ServerType, Diagnostic: diagnosticTemplate(stageMonitor, codeMonitorInProgress)})
		}
		if !job.Enabled || job.ReviewRequired {
			status, err := MigrationStatusWithOptions(request.JobID, jobs, options)
			status.Migration.Locations = status.Request.Locations
			return status.Migration, err
		}
		if job.Message != message || job.Kind != scheduler.ScheduleEvery || job.EverySeconds != MonitorIntervalSeconds {
			return failureResult(stageMonitor, errors.New("migration monitor ID is already in use"), Result{Status: resultManualReview, ServerType: request.ServerType, MonitorID: request.JobID, ManualReview: true})
		}
		return NormalizeResult(Result{Status: resultMonitoring, ServerType: request.ServerType, MonitorID: request.JobID, Diagnostic: initialMonitorDiagnostic(request.JobID)}), nil
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
	if err = storeOperation(dir, request.JobID, state); err != nil {
		return failureResult(stageStorage, errors.Join(ErrProtectedStorage, fmt.Errorf("store migration operation: %w", err)), Result{Status: resultManualReview, ServerType: request.ServerType, MonitorID: request.JobID, ManualReview: true})
	}
	if _, err := jobs.Add(scheduler.Job{
		ID:           request.JobID,
		Kind:         scheduler.ScheduleEvery,
		EverySeconds: MonitorIntervalSeconds,
		Message:      message,
	}); err != nil {
		return failureResult(stageMonitor, fmt.Errorf("add migration monitor: %w", err), Result{Status: resultManualReview, ServerType: request.ServerType, MonitorID: request.JobID, ManualReview: true})
	}
	if job, ok := jobs.Get(request.JobID); !ok || job.Message != message || !job.Enabled || job.Kind != scheduler.ScheduleEvery || job.EverySeconds != MonitorIntervalSeconds {
		verifyErr := errors.New("migration monitor could not be verified")
		if stopErr := stopAndVerifyMonitor(coordinator, request.JobID); stopErr != nil {
			failed := Result{Status: resultStopUnconfirmed, MonitorID: request.JobID, ServerType: request.ServerType, Diagnostic: unconfirmedStopDiagnostic(Diagnostic{}, request.JobID)}
			return failed, failureWithDiagnostic(failed.Diagnostic, errors.Join(ErrMonitorStopUnconfirmed, verifyErr, stopErr))
		}
		failed := Result{Status: resultManualReview, MonitorID: request.JobID, ServerType: request.ServerType, ManualReview: true, Diagnostic: stoppedMonitorDiagnostic(initialMonitorDiagnostic(request.JobID), request.JobID)}
		return failed, failureWithDiagnostic(failed.Diagnostic, verifyErr)
	}
	initial := initialMonitorDiagnostic(request.JobID)
	if err := storeMonitorDiagnostic(options, initial); err != nil {
		failed := Result{Status: resultManualReview, MonitorID: request.JobID, ServerType: request.ServerType, ManualReview: true, Diagnostic: diagnosticForFailure(stageStorage, err, Result{Status: resultManualReview, MonitorID: request.JobID})}
		if stopErr := stopAndVerifyMonitor(coordinator, request.JobID); stopErr != nil {
			failed.Status = resultStopUnconfirmed
			failed.ManualReview = false
			failed.Diagnostic = unconfirmedStopDiagnostic(failed.Diagnostic, request.JobID)
			return failed, failureWithDiagnostic(failed.Diagnostic, errors.Join(ErrMonitorStopUnconfirmed, err, stopErr))
		}
		failed.Diagnostic.MonitoringState = monitoringStopped
		failed.Diagnostic.MonitoringStopped = true
		return failed, failureWithDiagnostic(failed.Diagnostic, err)
	}

	result, runErr := executeMonitor(ctx, request, recipient, options, coordinator, request.JobID)
	result.MonitorID = request.JobID
	if runErr == nil {
		return NormalizeResult(result), nil
	}
	if err := stopAndVerifyMonitor(coordinator, request.JobID); err != nil {
		result.Status = resultStopUnconfirmed
		result.Diagnostic = unconfirmedStopDiagnostic(result.Diagnostic, request.JobID)
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
	result = Result{Status: resultManualReview, ServerType: request.ServerType, MonitorID: jobID, Diagnostic: diagnosticTemplate(stageMonitor, codeMonitorInProgress)}
	defer func() {
		if result.Status != resultMonitoring {
			result.Diagnostic = DiagnosticFor(runErr, result)
			result.Diagnostic.MonitorID = jobID
			result.Diagnostic.MonitoringState = monitoringUnknown
			result.Diagnostic.MonitoringStopped = false
			if err := storeMonitorDiagnostic(options, result.Diagnostic); err != nil {
				runErr = errors.Join(runErr, err)
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
	dir, err := operationDirectory(options)
	if err != nil {
		return failureResult(stageStorage, errors.Join(ErrProtectedStorage, err), result)
	}
	state, err := loadOperation(dir, jobID, request, recipient)
	if err != nil {
		return failureResult(stageStorage, errors.Join(ErrProtectedStorage, fmt.Errorf("load migration operation: %w", err)), result)
	}
	availability, err := availabilityWithToken(ctx, state.Terms, token, options)
	if err != nil {
		return failureResult(stageAvailability, fmt.Errorf("check migration availability: %w", err), result)
	}
	if len(availability.Locations) == 0 {
		result.Status = resultMonitoring
		result.Diagnostic = initialMonitorDiagnostic(jobID)
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
	if jobs == nil || !safeJobID(jobID) {
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
	data, err := json.Marshal(scheduledMigration{Version: "v1", JobID: request.JobID, Request: request, Recipient: id})
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
	if scheduled.Version != "v1" || scheduled.Recipient <= 0 || !safeJobID(scheduled.JobID) || scheduled.Request.JobID != scheduled.JobID || validateMachineRequest(scheduled.Request.ServerType, scheduled.Request.Locations) != nil {
		return scheduledMigration{}, errors.New("migration scheduler message is invalid")
	}
	if err := rejectLegacyPriceApproval(scheduled.Request); err != nil {
		return scheduledMigration{}, err
	}
	return scheduled, nil
}

func safeJobID(value string) bool {
	if !strings.HasPrefix(value, "migration-") || len(value) > 96 || !safeName(value, 96) {
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
