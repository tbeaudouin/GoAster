package chat

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"goaster/internal/diagnostics"
	"goaster/internal/scheduler"
)

const (
	cronToolName            = "cron"
	cronSelfDisableToolName = "cron_disable_self"
)

// CronScheduler is the narrow scheduler surface exposed to the model tool.
// Keeping the registrar independent of the concrete scheduler also makes the
// production conversation scope explicit at its call site.
type CronScheduler interface {
	Add(scheduler.Job) (scheduler.Job, error)
	List() []scheduler.Job
	Get(string) (scheduler.Job, bool)
	Update(string, scheduler.Patch) (scheduler.Job, error)
	Remove(string) error
	SetEnabled(string, bool) (scheduler.Job, error)
}

// CronSelfDisabler uses the scheduler's private execution owner instead of a
// model-supplied job ID. The expected origin contains only transport identity
// and is checked atomically with the disable operation.
type CronSelfDisabler interface {
	DisableOwnExecution(context.Context, scheduler.Job) (scheduler.Job, error)
}

type scheduledCronJobIDKey struct{}

// WithScheduledCronJobID carries the runtime's claimed job ID into the
// scheduled session. It is informational context; authorization remains bound
// to the scheduler execution owner.
func WithScheduledCronJobID(ctx context.Context, id string) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, scheduledCronJobIDKey{}, strings.TrimSpace(id))
}

// ScheduledCronJobID returns the runtime-injected job ID only when it matches
// the scheduler's live internal execution owner.
func ScheduledCronJobID(ctx context.Context) (string, bool) {
	if ctx == nil {
		return "", false
	}
	bound, ok := scheduler.ScheduledExecutionJobID(ctx)
	if !ok {
		return "", false
	}
	injected, ok := ctx.Value(scheduledCronJobIDKey{}).(string)
	injected = strings.TrimSpace(injected)
	if !ok || injected == "" || injected != bound {
		return "", false
	}
	return bound, true
}

func genuineScheduledCronContext(ctx context.Context) bool {
	if ctx == nil {
		return false
	}
	scheduled, ok := ctx.Value(scheduledTurnKey{}).(bool)
	if !ok || !scheduled {
		return false
	}
	_, ok = ScheduledCronJobID(ctx)
	return ok
}

// ScheduledDelivery is a completed generic cron reply waiting for the
// Telegram adapter. Its destination is copied from the transport-captured job
// origin; it is never supplied by the model.
type ScheduledDelivery struct {
	ID       string `json:"id"`
	ChatID   int64  `json:"chat_id"`
	ThreadID int64  `json:"thread_id,omitempty"`
	Text     string `json:"text"`
}

// RegisterCronTool exposes local chat-turn scheduling through the existing
// provider tool registrar. The scheduler owns persistence and execution; the
// tool only translates model arguments into scheduler operations.
func RegisterCronTool(registrar ToolRegistrar, jobs CronScheduler) error {
	if registrar == nil {
		return fmt.Errorf("cron tool registrar is nil")
	}
	if jobs == nil {
		return fmt.Errorf("cron scheduler is nil")
	}
	return registrar.RegisterTool(Tool{
		Name:                cronToolName,
		Description:         "Schedule chat messages for a later time or recurring interval in this conversation only. Manage only local GoAster chat turns; it does not schedule shell commands or route to channels. A running job cannot be removed until its run finishes. If a job requires review, reconcile external state and authentication before deciding whether to enable it. If max_runs is reached, update it to 0 or above run_count, then explicitly enable the job; editing the limit does not clear review. Enabling schedules a new run. Scheduled turns can inspect and manage existing jobs but cannot add another job.",
		Parameters:          cronToolParameters(false),
		ScheduledParameters: cronToolParameters(true),
		Handler: func(ctx context.Context, arguments string) (string, error) {
			return executeCronTool(ctx, arguments, jobs)
		},
	})
}

func cronToolParameters(scheduled bool) map[string]any {
	actions := []string{"list", "get", "update", "remove", "enable", "disable"}
	if !scheduled {
		actions = append([]string{"add"}, actions...)
	}
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"action": map[string]any{
				"type": "string",
				"enum": actions,
			},
			"id": map[string]any{
				"type":        []string{"string", "null"},
				"description": "Job ID returned by add or list; null when unused",
			},
			"at_seconds": map[string]any{
				"type":        []string{"integer", "null"},
				"minimum":     1,
				"description": "Choose one schedule; use null for unused fields. Example: 60 for a one-time turn",
			},
			"every_seconds": map[string]any{
				"type":        []string{"integer", "null"},
				"minimum":     1,
				"description": "Recurring example: every_seconds: 1800, at_seconds: null, cron_expr: null; use null for other unused fields",
			},
			"cron_expr": map[string]any{
				"type":        []string{"string", "null"},
				"description": "Choose one schedule; use null for unused fields. Example: 0 9 * * *",
			},
			"message": map[string]any{
				"type":        []string{"string", "null"},
				"description": "Message GoAster should send through the existing chat session; null when unused or unchanged",
			},
			"max_runs": map[string]any{
				"type":        []string{"integer", "null"},
				"minimum":     0,
				"description": "Maximum executions; null leaves unchanged, zero means unlimited. During a run, a positive limit must exceed the current run_count.",
			},
			"delete_after_run": map[string]any{
				"type":        []string{"boolean", "null"},
				"description": "Delete the job after its next execution; null leaves unchanged; at jobs always have this behavior",
			},
		},
		"required":             []string{"action", "id", "at_seconds", "every_seconds", "cron_expr", "message", "max_runs", "delete_after_run"},
		"additionalProperties": false,
	}
}

// RegisterCronSelfDisableTool exposes the self-only scheduled capability. It
// is registered with the provider once, but the provider hides it from normal
// turns and rejects forged calls outside a genuine scheduler context.
func RegisterCronSelfDisableTool(registrar ToolRegistrar, jobs CronScheduler) error {
	if registrar == nil {
		return fmt.Errorf("cron self-disable tool registrar is nil")
	}
	if jobs == nil {
		return fmt.Errorf("cron scheduler is nil")
	}
	if _, ok := jobs.(CronSelfDisabler); !ok {
		return fmt.Errorf("cron scheduler does not support self-disable")
	}
	return registrar.RegisterTool(Tool{
		Name:          cronSelfDisableToolName,
		Description:   "Disable only the currently executing scheduled cron job. The job identity and conversation/topic are bound internally; do not provide a job ID or target another job.",
		Parameters:    map[string]any{"type": "object", "properties": map[string]any{}, "additionalProperties": false},
		ScheduledOnly: true,
		Handler: func(ctx context.Context, arguments string) (string, error) {
			return executeCronSelfDisableTool(ctx, arguments, jobs)
		},
	})
}

func executeCronSelfDisableTool(ctx context.Context, arguments string, jobs CronScheduler) (result string, err error) {
	defer func() {
		if err != nil {
			if _, ok := err.(cronMutationError); !ok {
				err = cronPrecheckError{action: "disable_self", cause: err}
			}
		}
	}()
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if !genuineScheduledCronContext(ctx) {
		return "", fmt.Errorf("cron self-disable is available only during the currently executing scheduled job")
	}
	if err := parseCronSelfDisableArguments(arguments); err != nil {
		return "", err
	}
	origin, scoped, err := cronOrigin(ctx)
	if err != nil {
		return "", err
	}
	if !scoped || origin.OriginChatType == "" || origin.OriginChatType == "unknown" {
		return "", fmt.Errorf("cron self-disable requires an authenticated conversation identity")
	}
	if guard, ok := ctx.Value(scheduledMigrationDisableGuardKey{}).(func() bool); ok && guard() {
		return `{"disabled":false,"reason":"migration_retry_protected"}`, nil
	}
	disabler, ok := jobs.(CronSelfDisabler)
	if !ok {
		return "", fmt.Errorf("cron scheduler does not support self-disable")
	}
	job, err := disabler.DisableOwnExecution(ctx, origin)
	if err != nil {
		return "", cronMutationError{action: "disable_self", cause: err}
	}
	return encodeCronToolResult(struct {
		Disabled bool   `json:"disabled"`
		ID       string `json:"id"`
	}{Disabled: !job.Enabled, ID: job.ID})
}

func parseCronSelfDisableArguments(arguments string) error {
	decoder := json.NewDecoder(strings.NewReader(arguments))
	decoder.DisallowUnknownFields()
	var fields map[string]json.RawMessage
	if err := decoder.Decode(&fields); err != nil || fields == nil {
		return fmt.Errorf("cron self-disable arguments must be an empty JSON object")
	}
	if len(fields) != 0 {
		return fmt.Errorf("cron self-disable does not accept arguments")
	}
	var extra json.RawMessage
	if err := decoder.Decode(&extra); err != io.EOF {
		return fmt.Errorf("cron self-disable arguments contain more than one JSON value")
	}
	return nil
}

type cronToolArguments struct {
	Action         string  `json:"action"`
	ID             string  `json:"id,omitempty"`
	AtSeconds      *int64  `json:"at_seconds,omitempty"`
	EverySeconds   *int64  `json:"every_seconds,omitempty"`
	CronExpr       *string `json:"cron_expr,omitempty"`
	Message        *string `json:"message,omitempty"`
	MaxRuns        *int    `json:"max_runs,omitempty"`
	DeleteAfterRun *bool   `json:"delete_after_run,omitempty"`
}

func executeCronTool(ctx context.Context, arguments string, jobs CronScheduler) (result string, err error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	parsed, err := parseCronToolArguments(arguments)
	if err != nil {
		return "", err
	}
	action := strings.ToLower(strings.TrimSpace(parsed.Action))
	defer func() {
		if err != nil {
			switch err.(type) {
			case cronArgumentError, cronMutationError:
			default:
				err = cronPrecheckError{action: action, cause: err}
			}
		}
	}()
	origin, scoped, err := cronOrigin(ctx)
	if err != nil {
		return "", err
	}
	switch action {
	case "add", "list", "get", "update", "remove", "enable", "disable":
	default:
		return "", cronInvalid("unknown", "invalid_action", "Cron requires a supported action.")
	}
	if action != "add" && action != "list" && strings.TrimSpace(parsed.ID) == "" {
		return "", cronInvalid(action, "missing_id", "This cron action requires an id.")
	}
	if started, ok := ctx.Value(scheduledMigrationStartedKey{}).(func() bool); ok {
		if action == "add" {
			return "", fmt.Errorf("scheduled migration cannot add cron jobs")
		}
		if started() && action != "list" && action != "get" {
			return "", fmt.Errorf("scheduled migration cannot change cron jobs after an attempt")
		}
	}
	if action == "add" && scheduler.IsScheduledExecution(ctx) {
		return "", fmt.Errorf("scheduled cron turns cannot add cron jobs")
	}
	switch action {
	case "add":
		return cronToolAdd(parsed, jobs, origin, scoped)
	case "list":
		jobs := jobs.List()
		if scoped {
			jobs = filterCronJobs(jobs, origin)
		}
		return encodeCronToolResult(struct {
			Jobs []cronToolJob `json:"jobs"`
		}{Jobs: publicCronJobs(jobs)})
	case "get":
		job, err := requireCronJob(parsed.ID, jobs, origin, scoped)
		if err != nil {
			return "", err
		}
		return encodeCronToolResult(publicCronJob(job))
	case "update":
		if parsed.AtSeconds == nil && parsed.EverySeconds == nil && parsed.CronExpr == nil && parsed.Message == nil && parsed.MaxRuns == nil && parsed.DeleteAfterRun == nil {
			return "", cronInvalid(action, "missing_update", "Cron update requires at least one field.")
		}
		if err := validateCronUpdate(parsed); err != nil {
			return "", err
		}
		patch := scheduler.Patch{
			AtSeconds:      parsed.AtSeconds,
			EverySeconds:   parsed.EverySeconds,
			CronExpr:       parsed.CronExpr,
			Message:        parsed.Message,
			MaxRuns:        parsed.MaxRuns,
			DeleteAfterRun: parsed.DeleteAfterRun,
		}
		if _, err := requireCronJob(parsed.ID, jobs, origin, scoped); err != nil {
			return "", err
		}
		job, err := jobs.Update(parsed.ID, patch)
		if err != nil {
			return "", cronMutationError{action: action, cause: err}
		}
		return encodeCronToolResult(publicCronJob(job))
	case "remove":
		if _, err := requireCronJob(parsed.ID, jobs, origin, scoped); err != nil {
			return "", err
		}
		if err := jobs.Remove(parsed.ID); err != nil {
			return "", cronMutationError{action: action, cause: err}
		}
		return encodeCronToolResult(struct {
			Removed bool   `json:"removed"`
			ID      string `json:"id"`
		}{Removed: true, ID: strings.TrimSpace(parsed.ID)})
	case "enable", "disable":
		if _, err := requireCronJob(parsed.ID, jobs, origin, scoped); err != nil {
			return "", err
		}
		job, err := jobs.SetEnabled(parsed.ID, strings.EqualFold(strings.TrimSpace(parsed.Action), "enable"))
		if err != nil {
			return "", cronMutationError{action: action, cause: err}
		}
		return encodeCronToolResult(publicCronJob(job))
	default:
		return "", fmt.Errorf("unsupported cron action %q", parsed.Action)
	}
}

// cronArgumentError is safe to report to the model: no scheduler mutation began.
type cronArgumentError struct {
	reason  string
	message string
	action  string
	cause   error
}

func (e cronArgumentError) Error() string { return e.message }
func (e cronArgumentError) Unwrap() error { return e.cause }
func (e cronArgumentError) DiagnosticFields() []diagnostics.Field {
	return []diagnostics.Field{{Key: "cron_action", Value: e.action}, {Key: "cron_stage", Value: "validate"}, {Key: "cron_reason", Value: e.reason}}
}
func cronInvalid(action, reason, message string) error {
	return cronArgumentError{action: action, reason: reason, message: message}
}

type cronPrecheckError struct {
	action string
	cause  error
}

func (e cronPrecheckError) Error() string { return "cron precheck failed" }
func (e cronPrecheckError) Unwrap() error { return e.cause }
func (e cronPrecheckError) DiagnosticFields() []diagnostics.Field {
	reason := "precheck_failed"
	if errors.Is(e.cause, scheduler.ErrJobNotFound) {
		reason = "job_not_found"
	}
	return []diagnostics.Field{{Key: "cron_action", Value: e.action}, {Key: "cron_stage", Value: "precheck"}, {Key: "cron_reason", Value: reason}}
}

type cronMutationError struct {
	action string
	cause  error
}

func (e cronMutationError) Error() string { return "cron scheduler mutation failed; outcome uncertain" }
func (e cronMutationError) Unwrap() error { return e.cause }
func (e cronMutationError) DiagnosticFields() []diagnostics.Field {
	reason := toolCauseReason(e.cause)
	if reason == "unrecognized" {
		reason = "unknown"
	}
	switch {
	case errors.Is(e.cause, scheduler.ErrJobNotFound):
		reason = "job_not_found"
	case errors.Is(e.cause, scheduler.ErrExecutionInProgress):
		reason = "execution_in_progress"
	case errors.Is(e.cause, scheduler.ErrExecutionClaimUnavailable):
		reason = "claim_unavailable"
	case errors.Is(e.cause, scheduler.ErrReviewRequired):
		reason = "review_required"
	}
	return []diagnostics.Field{{Key: "cron_action", Value: e.action}, {Key: "cron_stage", Value: "scheduler_mutation"}, {Key: "cron_reason", Value: reason}}
}
func parseCronToolArguments(arguments string) (cronToolArguments, error) {
	invalid := func(reason, message string, cause ...error) (cronToolArguments, error) {
		var underlying error
		if len(cause) > 0 {
			underlying = cause[0]
		}
		return cronToolArguments{}, cronArgumentError{action: "unknown", reason: reason, message: message, cause: underlying}
	}
	decoder := json.NewDecoder(strings.NewReader(arguments))
	token, err := decoder.Token()
	if err != nil || token != json.Delim('{') {
		return invalid("invalid_object", "cron arguments must be a JSON object", err)
	}
	fields := map[string]json.RawMessage{}
	allowed := map[string]bool{"action": true, "id": true, "at_seconds": true, "every_seconds": true, "cron_expr": true, "message": true, "max_runs": true, "delete_after_run": true}
	for decoder.More() {
		keyToken, err := decoder.Token()
		if err != nil {
			return invalid("invalid_json", "cron arguments contain invalid JSON", err)
		}
		key, ok := keyToken.(string)
		if !ok {
			return invalid("invalid_json", "cron arguments contain invalid JSON", err)
		}
		if !allowed[key] {
			return invalid("unknown_field", "cron arguments contain an unknown field")
		}
		if _, exists := fields[key]; exists {
			return invalid("duplicate_field", "cron arguments contain a duplicate field")
		}
		var value json.RawMessage
		if err := decoder.Decode(&value); err != nil {
			return invalid("invalid_json", "cron arguments contain invalid JSON", err)
		}
		fields[key] = value
	}
	if _, err := decoder.Token(); err != nil {
		return invalid("invalid_json", "cron arguments contain invalid JSON", err)
	}
	var extra json.RawMessage
	if err := decoder.Decode(&extra); err != io.EOF {
		return invalid("trailing_data", "cron arguments contain trailing data", err)
	}
	for key, value := range fields {
		if string(value) == "null" && key == "action" {
			return invalid("invalid_type", "cron arguments contain an invalid field type")
		}
	}
	data, _ := json.Marshal(fields)
	var parsed cronToolArguments
	if err := json.Unmarshal(data, &parsed); err != nil {
		return invalid("invalid_type", "cron arguments contain an invalid field type", err)
	}
	return parsed, nil
}

func cronToolAdd(arguments cronToolArguments, jobs CronScheduler, origin scheduler.Job, scoped bool) (string, error) {
	action := "add"
	if arguments.AtSeconds != nil && *arguments.AtSeconds < 0 || arguments.EverySeconds != nil && *arguments.EverySeconds < 0 {
		return "", cronInvalid(action, "invalid_schedule", "Schedule durations must be positive.")
	}
	if arguments.AtSeconds != nil && *arguments.AtSeconds == 0 {
		arguments.AtSeconds = nil
	}
	if arguments.EverySeconds != nil && *arguments.EverySeconds == 0 {
		arguments.EverySeconds = nil
	}
	if arguments.CronExpr != nil && *arguments.CronExpr == "" {
		arguments.CronExpr = nil
	}
	scheduleFields := 0
	if arguments.AtSeconds != nil {
		scheduleFields++
	}
	if arguments.EverySeconds != nil {
		scheduleFields++
	}
	if arguments.CronExpr != nil {
		scheduleFields++
	}
	if scheduleFields != 1 {
		return "", cronInvalid(action, "schedule_count", "Cron add requires exactly one positive schedule: at_seconds, every_seconds, or cron_expr; omit unused fields.")
	}
	if arguments.Message == nil || strings.TrimSpace(*arguments.Message) == "" {
		return "", cronInvalid(action, "missing_message", "Cron add requires a nonempty message.")
	}
	if arguments.MaxRuns != nil && *arguments.MaxRuns < 0 {
		return "", cronInvalid(action, "invalid_max_runs", "max_runs must not be negative.")
	}
	job := scheduler.Job{Message: *arguments.Message}
	switch {
	case arguments.AtSeconds != nil:
		job.Kind = scheduler.ScheduleAt
		job.AtSeconds = *arguments.AtSeconds
		job.DeleteAfterRun = true
	case arguments.EverySeconds != nil:
		job.Kind = scheduler.ScheduleEvery
		job.EverySeconds = *arguments.EverySeconds
	case arguments.CronExpr != nil:
		job.Kind = scheduler.ScheduleCron
		job.CronExpr = strings.TrimSpace(*arguments.CronExpr)
	}
	if err := validateCronSchedule(action, job.Schedule()); err != nil {
		return "", err
	}
	if arguments.MaxRuns != nil {
		job.MaxRuns = *arguments.MaxRuns
	}
	if arguments.DeleteAfterRun != nil {
		job.DeleteAfterRun = *arguments.DeleteAfterRun
	}
	if scoped {
		job.OriginChatID = origin.OriginChatID
		job.OriginChatType = origin.OriginChatType
		job.OriginThreadID = origin.OriginThreadID
	}
	created, err := jobs.Add(job)
	if err != nil {
		return "", cronMutationError{action: "add", cause: err}
	}
	return encodeCronToolResult(publicCronJob(created))
}

func validateCronSchedule(action string, schedule scheduler.Schedule) error {
	if _, err := scheduler.NextRun(schedule, time.Now()); err != nil {
		reason := "invalid_schedule"
		if schedule.Kind == scheduler.ScheduleCron {
			reason = "invalid_cron_expression"
		}
		return cronArgumentError{action: action, reason: reason, message: "Cron schedule is invalid.", cause: err}
	}
	return nil
}

func validateCronUpdate(a cronToolArguments) error {
	if a.Message != nil && strings.TrimSpace(*a.Message) == "" {
		return cronInvalid("update", "missing_message", "Cron update requires a nonempty message.")
	}
	if a.MaxRuns != nil && *a.MaxRuns < 0 {
		return cronInvalid("update", "invalid_max_runs", "max_runs must not be negative.")
	}
	count := 0
	schedule := scheduler.Schedule{}
	if a.AtSeconds != nil {
		count++
		schedule.Kind = scheduler.ScheduleAt
		schedule.AtSeconds = *a.AtSeconds
	}
	if a.EverySeconds != nil {
		count++
		schedule.Kind = scheduler.ScheduleEvery
		schedule.EverySeconds = *a.EverySeconds
	}
	if a.CronExpr != nil {
		count++
		schedule.Kind = scheduler.ScheduleCron
		schedule.CronExpr = *a.CronExpr
	}
	if count > 1 {
		return cronInvalid("update", "schedule_count", "Cron update accepts at most one schedule.")
	}
	if count == 1 {
		return validateCronSchedule("update", schedule)
	}
	return nil
}

func requireCronJob(id string, jobs CronScheduler, origin scheduler.Job, scoped bool) (scheduler.Job, error) {
	id = strings.TrimSpace(id)
	if id == "" {
		return scheduler.Job{}, fmt.Errorf("cron get requires id")
	}
	job, ok := jobs.Get(id)
	if !ok {
		return scheduler.Job{}, fmt.Errorf("%w: %s", scheduler.ErrJobNotFound, id)
	}
	if scoped && !sameCronOrigin(job, origin) {
		return scheduler.Job{}, fmt.Errorf("%w: %s", scheduler.ErrJobNotFound, id)
	}
	return job, nil
}

func cronOrigin(ctx context.Context) (scheduler.Job, bool, error) {
	chatID, present := ConversationID(ctx)
	if !present {
		// Terminal callers predate transport-scoped cron jobs. They retain the
		// existing local scheduler behavior; the production agent always sets
		// this value from its authenticated transport request.
		return scheduler.Job{}, false, nil
	}
	if chatID == 0 {
		return scheduler.Job{}, false, fmt.Errorf("cron conversation identity is invalid")
	}
	origin := scheduler.Job{OriginChatID: chatID, OriginChatType: ConversationType(ctx)}
	if threadID, ok := ConversationThreadID(ctx); ok {
		if threadID < 0 {
			return scheduler.Job{}, false, fmt.Errorf("cron conversation thread identity is invalid")
		}
		origin.OriginThreadID = threadID
	}
	return origin, true, nil
}

func sameCronOrigin(left, right scheduler.Job) bool {
	return left.OriginChatID == right.OriginChatID &&
		left.OriginChatType == right.OriginChatType &&
		left.OriginThreadID == right.OriginThreadID
}

func filterCronJobs(jobs []scheduler.Job, origin scheduler.Job) []scheduler.Job {
	filtered := make([]scheduler.Job, 0, len(jobs))
	for _, job := range jobs {
		if sameCronOrigin(job, origin) {
			filtered = append(filtered, job)
		}
	}
	return filtered
}

type cronToolJob struct {
	job scheduler.Job
}

func publicCronJob(job scheduler.Job) cronToolJob {
	return cronToolJob{job: job}
}

func publicCronJobs(jobs []scheduler.Job) []cronToolJob {
	public := make([]cronToolJob, 0, len(jobs))
	for _, job := range jobs {
		public = append(public, publicCronJob(job))
	}
	return public
}

// Marshal without the transport-captured origin. The origin remains in the
// private scheduler file and is used only by the production executor.
func (job cronToolJob) MarshalJSON() ([]byte, error) {
	data, err := json.Marshal(job.job)
	if err != nil {
		return nil, fmt.Errorf("encode cron job: %w", err)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		return nil, fmt.Errorf("prepare cron job result: %w", err)
	}
	delete(fields, "origin_chat_id")
	delete(fields, "origin_chat_type")
	delete(fields, "origin_thread_id")
	return json.Marshal(fields)
}

func encodeCronToolResult(value any) (string, error) {
	var output bytes.Buffer
	encoder := json.NewEncoder(&output)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(value); err != nil {
		return "", fmt.Errorf("encode cron result: %w", err)
	}
	return strings.TrimSpace(output.String()), nil
}
