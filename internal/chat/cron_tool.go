package chat

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"goaster/internal/scheduler"
)

const cronToolName = "cron"

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
		Name:        cronToolName,
		Description: "Schedule chat messages for a later time or recurring interval in this conversation only. Manage only local GoAster chat turns; it does not schedule shell commands or route to channels. A running job cannot be removed until its run finishes. If a job requires review, reconcile external state and authentication before deciding whether to enable it. If max_runs is reached, update it to 0 or above run_count, then explicitly enable the job; editing the limit does not clear review. Enabling schedules a new run.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"action": map[string]any{
					"type": "string",
					"enum": []string{"add", "list", "get", "update", "remove", "enable", "disable"},
				},
				"id": map[string]any{
					"type":        "string",
					"description": "Job ID returned by add or list",
				},
				"at_seconds": map[string]any{
					"type":        "integer",
					"minimum":     1,
					"description": "Positive number of seconds from now for a one-time chat turn",
				},
				"every_seconds": map[string]any{
					"type":        "integer",
					"minimum":     1,
					"description": "Positive interval in seconds for a recurring chat turn",
				},
				"cron_expr": map[string]any{
					"type":        "string",
					"description": "Five-field cron expression, or six fields including seconds",
				},
				"message": map[string]any{
					"type":        "string",
					"description": "Message GoAster should send through the existing chat session",
				},
				"max_runs": map[string]any{
					"type":        "integer",
					"minimum":     0,
					"description": "Maximum executions; zero means unlimited. During a run, a positive limit must exceed the current run_count.",
				},
				"delete_after_run": map[string]any{
					"type":        "boolean",
					"description": "Delete the job after its next execution; at jobs always have this behavior",
				},
			},
			"required":             []string{"action"},
			"additionalProperties": false,
		},
		Handler: func(ctx context.Context, arguments string) (string, error) {
			return executeCronTool(ctx, arguments, jobs)
		},
	})
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

func executeCronTool(ctx context.Context, arguments string, jobs CronScheduler) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	parsed, err := parseCronToolArguments(arguments)
	if err != nil {
		return "", err
	}
	origin, scoped, err := cronOrigin(ctx)
	if err != nil {
		return "", err
	}
	switch strings.ToLower(strings.TrimSpace(parsed.Action)) {
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
		if strings.TrimSpace(parsed.ID) == "" {
			return "", fmt.Errorf("cron update requires id")
		}
		if parsed.AtSeconds == nil && parsed.EverySeconds == nil && parsed.CronExpr == nil && parsed.Message == nil && parsed.MaxRuns == nil && parsed.DeleteAfterRun == nil {
			return "", fmt.Errorf("cron update requires at least one field")
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
			return "", err
		}
		return encodeCronToolResult(publicCronJob(job))
	case "remove":
		if strings.TrimSpace(parsed.ID) == "" {
			return "", fmt.Errorf("cron remove requires id")
		}
		if _, err := requireCronJob(parsed.ID, jobs, origin, scoped); err != nil {
			return "", err
		}
		if err := jobs.Remove(parsed.ID); err != nil {
			return "", err
		}
		return encodeCronToolResult(struct {
			Removed bool   `json:"removed"`
			ID      string `json:"id"`
		}{Removed: true, ID: strings.TrimSpace(parsed.ID)})
	case "enable", "disable":
		if strings.TrimSpace(parsed.ID) == "" {
			return "", fmt.Errorf("cron %s requires id", strings.ToLower(strings.TrimSpace(parsed.Action)))
		}
		if _, err := requireCronJob(parsed.ID, jobs, origin, scoped); err != nil {
			return "", err
		}
		job, err := jobs.SetEnabled(parsed.ID, strings.EqualFold(strings.TrimSpace(parsed.Action), "enable"))
		if err != nil {
			return "", err
		}
		return encodeCronToolResult(publicCronJob(job))
	default:
		return "", fmt.Errorf("unsupported cron action %q", parsed.Action)
	}
}

func parseCronToolArguments(arguments string) (cronToolArguments, error) {
	decoder := json.NewDecoder(strings.NewReader(arguments))
	decoder.DisallowUnknownFields()
	var parsed cronToolArguments
	if err := decoder.Decode(&parsed); err != nil {
		return cronToolArguments{}, fmt.Errorf("cron arguments are not valid JSON: %w", err)
	}
	var extra json.RawMessage
	if err := decoder.Decode(&extra); err != io.EOF {
		return cronToolArguments{}, fmt.Errorf("cron arguments contain more than one JSON value")
	}
	return parsed, nil
}

func cronToolAdd(arguments cronToolArguments, jobs CronScheduler, origin scheduler.Job, scoped bool) (string, error) {
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
		return "", fmt.Errorf("cron add requires exactly one of at_seconds, every_seconds, or cron_expr")
	}
	if arguments.Message == nil || strings.TrimSpace(*arguments.Message) == "" {
		return "", fmt.Errorf("cron add requires message")
	}
	if arguments.MaxRuns != nil && *arguments.MaxRuns < 0 {
		return "", fmt.Errorf("max_runs must not be negative")
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
		return "", err
	}
	return encodeCronToolResult(publicCronJob(created))
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
