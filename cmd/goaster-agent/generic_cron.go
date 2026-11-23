package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"goaster/internal/attachment"
	"goaster/internal/chat"
	"goaster/internal/diagnostics"
	"goaster/internal/migration"
	"goaster/internal/scheduler"
)

type agentSession interface {
	Reply(context.Context, string) (string, error)
	ReplyScheduled(context.Context, string) (string, error)
	ReplyWithAttachments(context.Context, string, []attachment.Metadata) (string, error)
	PendingModelRecoveryNotice() string
}

func newAgentSessionFactory(create func(int64) agentSession) func(int64, int64) agentSession {
	var sessionsMu sync.Mutex
	sessions := make(map[int64]agentSession)
	return func(chatID, _ int64) agentSession {
		sessionsMu.Lock()
		defer sessionsMu.Unlock()
		if session := sessions[chatID]; session != nil {
			return session
		}
		session := create(chatID)
		sessions[chatID] = session
		return session
	}
}

type cronSchedulerRuntime interface {
	chat.CronScheduler
	cronExecutionCoordinator
	Path() string
	Start(context.Context) error
	Stop()
	RunDue(context.Context) int
}

type cronExecutionCoordinator interface {
	ClaimExecutionMatching(context.Context, scheduler.Job) (scheduler.Job, func(error) (scheduler.Job, error), error)
}

type scheduledMigrationCreate func(context.Context, migration.Request, int64) migration.OneShotResult

func newCronScheduler(sessionFor func(int64, int64) agentSession, enqueue func(scheduler.Job, string) error) (cronSchedulerRuntime, error) {
	return newCronSchedulerWithMigration(sessionFor, enqueue, nil)
}

func newCronSchedulerWithMigration(sessionFor func(int64, int64) agentSession, enqueue func(scheduler.Job, string) error, create scheduledMigrationCreate) (cronSchedulerRuntime, error) {
	return newCronSchedulerWithMigrationAt(sessionFor, enqueue, create, nil)
}

func newCronSchedulerWithMigrationAt(sessionFor func(int64, int64) agentSession, enqueue func(scheduler.Job, string) error, create scheduledMigrationCreate, now func() time.Time) (cronSchedulerRuntime, error) {
	var jobs cronSchedulerRuntime
	created, err := scheduler.New(scheduler.Options{
		Now: now,
		JobExecutor: func(ctx context.Context, job scheduler.Job) error {
			if !restartWorkMu.TryRLock() {
				return scheduler.ErrRetry
			}
			defer restartWorkMu.RUnlock()
			return executeAgentCronJob(ctx, job, jobs, sessionFor, enqueue, create)
		},
	})
	if err != nil {
		return nil, fmt.Errorf("initialize generic cron scheduler: %w", err)
	}
	jobs = created
	return jobs, nil
}

func executeAgentCronJob(ctx context.Context, job scheduler.Job, coordinator cronExecutionCoordinator, sessionFor func(int64, int64) agentSession, enqueue func(scheduler.Job, string) error, create scheduledMigrationCreate) error {
	if coordinator == nil {
		cause := errors.New("generic cron execution coordinator is unavailable")
		diagnostics.Error("agent.cron.execute", cause, diagnostics.Field{Key: "reason", Value: "execution coordinator is unavailable"})
		return scheduler.ReviewRequiredError{Message: "scheduled job execution is unavailable", Cause: cause}
	}
	if job.OriginChatID == 0 {
		cause := errors.New("generic cron job has no originating conversation")
		diagnostics.Error("agent.cron.execute", cause, diagnostics.Field{Key: "reason", Value: "origin is missing"})
		return scheduler.ReviewRequiredError{Message: "scheduled job has no originating conversation", Cause: cause}
	}
	if sessionFor == nil {
		cause := errors.New("generic cron session factory is unavailable")
		diagnostics.Error("agent.cron.execute", cause, diagnostics.Field{Key: "reason", Value: "session factory is unavailable"})
		return scheduler.ReviewRequiredError{Message: "scheduled chat turn is unavailable", Cause: cause}
	}
	session := sessionFor(job.OriginChatID, job.OriginThreadID)
	if session == nil {
		cause := errors.New("generic cron session is unavailable")
		diagnostics.Error("agent.cron.execute", cause, diagnostics.Field{Key: "reason", Value: "session is unavailable"})
		return scheduler.ReviewRequiredError{Message: "scheduled chat turn is unavailable", Cause: cause}
	}
	requestContext := chat.WithConversationType(ctx, job.OriginChatType)
	requestContext = chat.WithConversationID(requestContext, job.OriginChatID)
	requestContext = chat.WithConversationThreadID(requestContext, job.OriginThreadID)
	requestContext = chat.WithScheduledCronJobID(requestContext, job.ID)
	requestContext = chat.WithRuntimeFailureOperation(requestContext, "agent.cron")
	var resultMu sync.Mutex
	called, retry, review, quiet := false, false, false, false
	unrelatedTool := false
	requestContext = chat.AddToolCallObserver(requestContext, func(name, _ string) {
		if name != "goaster_migration" && name != "load_skill" {
			resultMu.Lock()
			unrelatedTool = true
			resultMu.Unlock()
		}
	})
	requestContext = chat.WithScheduledMigrationDisableGuard(requestContext, func() bool {
		resultMu.Lock()
		defer resultMu.Unlock()
		return called && retry && !review
	})
	requestContext = chat.WithScheduledMigrationStarted(requestContext, func() bool {
		resultMu.Lock()
		defer resultMu.Unlock()
		return called
	})
	requestContext = chat.WithScheduledMigration(requestContext, func(callContext context.Context, request migration.Request, rejection string) migration.OneShotResult {
		resultMu.Lock()
		defer resultMu.Unlock()
		if called {
			diagnostics.Error("agent.cron.migration", errors.New("repeated scheduled migration invocation"))
			return migration.OneShotResult{CreationState: "not_created", Code: "already_attempted"}
		}
		called = true
		if rejection != "" {
			review = true
			if rejection != "request_cancelled" {
				diagnostics.Error("agent.cron.migration", errors.New("scheduled migration invocation rejected"), diagnostics.Field{Key: "reason", Value: rejection})
			}
			return migration.OneShotResult{CreationState: "not_created", Code: rejection}
		}
		if !migration.ValidMigrationServerName(request.ServerName) {
			diagnostics.Error("agent.cron.migration", errors.New("scheduled migration target name is missing or invalid"))
			review = true
			return migration.OneShotResult{CreationState: "not_created", Code: "invalid_arguments"}
		}
		if _, _, err := coordinator.ClaimExecutionMatching(callContext, job); err != nil {
			cause := fmt.Errorf("claim generic cron execution: %w", err)
			diagnostics.Error("agent.cron.claim", cause, diagnostics.Field{Key: "reason", Value: "execution claim failed"})
			review = true
			return migration.OneShotResult{CreationState: "not_created", Code: "claim_failed"}
		}
		result := migration.OneShotResult{CreationState: "not_created", Code: "capability_unavailable", ServerName: request.ServerName}
		if create != nil {
			result = create(callContext, request, job.OriginChatID)
		} else {
			diagnostics.Error("agent.cron.migration", errors.New("scheduled migration capability unavailable"), diagnostics.Field{Key: "migration_reason", Value: "create_unavailable"})
		}
		quiet = result.CreationState == "not_created" && result.Code == "no_capacity"
		retry = !scheduledMigrationTerminal(result, request.ServerName)
		return result
	})
	values, environmentErr := runtimeEnvironment()
	if environmentErr != nil {
		diagnostics.Error("agent.cron.runtime_environment", environmentErr)
		requestContext = chat.WithShellEnvironmentUnavailable(requestContext)
	} else if len(values) > 0 {
		requestContext = chat.WithShellEnvironment(requestContext, values)
	}
	reply, err := session.ReplyScheduled(requestContext, job.Message)
	if err != nil {
		resultMu.Lock()
		attempted := called
		canRetry := retry && !review && !unrelatedTool
		resultMu.Unlock()
		if attempted && canRetry && chat.IsCompletedToolModelFailure(err) {
			return scheduler.ErrRetry
		}
		if attempted {
			if review := chat.ScheduledActionReviewMessage(err); review != "" {
				return scheduler.ReviewRequiredError{Message: review, Cause: err}
			}
			return scheduler.ReviewRequiredError{Message: "scheduled migration attempt requires review", Cause: err}
		}
		if errors.Is(err, chat.ErrSessionBusy) && chat.ScheduledActionReviewMessage(err) == "" {
			return scheduler.ErrRetry
		}
		if review := chat.ScheduledActionReviewMessage(err); review != "" {
			return scheduler.ReviewRequiredError{Message: review, Cause: err}
		}
		return err
	}
	resultMu.Lock()
	shouldRetry := retry
	isQuiet := quiet
	terminal := called && !retry && !review
	resultMu.Unlock()
	if shouldRetry && isQuiet {
		return scheduler.ErrRetry
	}
	if terminal {
		disabler, ok := coordinator.(chat.CronSelfDisabler)
		if !ok {
			cause := errors.New("scheduled migration self-disable unavailable")
			diagnostics.Error("agent.cron.migration", cause)
			return scheduler.ReviewRequiredError{Message: "scheduled migration result requires review", Cause: cause}
		}
		if _, err := disabler.DisableOwnExecution(ctx, job); err != nil {
			cause := fmt.Errorf("disable completed scheduled migration: %w", err)
			diagnostics.Error("agent.cron.migration", cause)
			return scheduler.ReviewRequiredError{Message: "scheduled migration result requires review", Cause: cause}
		}
	}
	if enqueue == nil {
		cause := errors.New("generic cron delivery queue is unavailable")
		diagnostics.Error("agent.cron.delivery", cause, diagnostics.Field{Key: "reason", Value: "delivery queue is unavailable"})
		return scheduler.ReviewRequiredError{Message: "scheduled reply could not be delivered", Cause: cause}
	}
	if err := enqueue(job, reply); err != nil {
		diagnostics.Error("agent.cron.delivery", err, diagnostics.Field{Key: "reason", Value: "persist scheduled reply"})
		return scheduler.ReviewRequiredError{Message: "scheduled reply could not be delivered", Cause: err}
	}
	resultMu.Lock()
	defer resultMu.Unlock()
	if retry && !review {
		return scheduler.ErrRetry
	}
	if review {
		cause := errors.New("scheduled migration result requires review")
		return scheduler.ReviewRequiredError{Message: "scheduled migration result requires review", Cause: cause}
	}
	return nil
}

func scheduledMigrationTerminal(result migration.OneShotResult, name string) bool {
	return result.CreationState == "created" && result.Handoff == "delivered" ||
		result.CreationState == "not_created" && result.Code == "target_exists" && result.ServerName == name
}

const (
	cronDeliveryFileVersion = 1
	maxCronDeliveryBytes    = 4 << 20
	maxCronDeliveryCount    = 256
	maxCronDeliveryTextSize = 1 << 20
)

var cronDeliveryCounter atomic.Uint64

type cronDeliveryFile struct {
	Version    int                      `json:"version"`
	Deliveries []chat.ScheduledDelivery `json:"deliveries"`
}

type cronDeliveryQueue struct {
	Enqueue func(scheduler.Job, string) error
	Pending func() ([]chat.ScheduledDelivery, error)
	Ack     func([]string) error
}

func newCronDeliveryQueue(path string) (cronDeliveryQueue, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return cronDeliveryQueue{}, errors.New("generic cron delivery path is empty")
	}
	if err := ensureCronDeliveryDirectory(filepath.Dir(path)); err != nil {
		return cronDeliveryQueue{}, err
	}
	deliveries, err := loadCronDeliveries(path)
	if err != nil {
		return cronDeliveryQueue{}, err
	}
	var mu sync.Mutex

	enqueue := func(job scheduler.Job, text string) error {
		if job.OriginChatID == 0 {
			return errors.New("scheduled reply has no originating chat")
		}
		text = strings.TrimSpace(text)
		if text == "" {
			return errors.New("scheduled reply is empty")
		}
		if len(text) > maxCronDeliveryTextSize {
			return errors.New("scheduled reply exceeds the delivery limit")
		}
		entry := chat.ScheduledDelivery{
			ID:       fmt.Sprintf("delivery-%d-%d", time.Now().UnixNano(), cronDeliveryCounter.Add(1)),
			ChatID:   job.OriginChatID,
			ThreadID: job.OriginThreadID,
			Text:     text,
		}
		mu.Lock()
		defer mu.Unlock()
		if len(deliveries) >= maxCronDeliveryCount {
			return errors.New("scheduled delivery queue is full")
		}
		previous := append([]chat.ScheduledDelivery(nil), deliveries...)
		deliveries = append(deliveries, entry)
		if err := saveCronDeliveries(path, deliveries); err != nil {
			deliveries = previous
			return err
		}
		return nil
	}

	pending := func() ([]chat.ScheduledDelivery, error) {
		mu.Lock()
		defer mu.Unlock()
		return append([]chat.ScheduledDelivery(nil), deliveries...), nil
	}

	ack := func(ids []string) error {
		if len(ids) == 0 {
			return nil
		}
		if len(ids) > maxCronDeliveryCount {
			return errors.New("too many scheduled delivery acknowledgements")
		}
		wanted := make(map[string]struct{}, len(ids))
		for _, id := range ids {
			if !validCronDeliveryID(id) {
				return errors.New("scheduled delivery acknowledgement is invalid")
			}
			wanted[id] = struct{}{}
		}
		mu.Lock()
		defer mu.Unlock()
		remaining := make([]chat.ScheduledDelivery, 0, len(deliveries))
		changed := false
		for _, delivery := range deliveries {
			if _, ok := wanted[delivery.ID]; ok {
				changed = true
				continue
			}
			remaining = append(remaining, delivery)
		}
		if !changed {
			return nil
		}
		previous := deliveries
		deliveries = remaining
		if err := saveCronDeliveries(path, deliveries); err != nil {
			deliveries = previous
			return err
		}
		return nil
	}

	return cronDeliveryQueue{Enqueue: enqueue, Pending: pending, Ack: ack}, nil
}

func ensureCronDeliveryDirectory(directory string) error {
	if err := os.MkdirAll(directory, 0700); err != nil {
		return fmt.Errorf("create generic cron delivery directory: %w", err)
	}
	info, err := os.Lstat(directory)
	if err != nil {
		return fmt.Errorf("inspect generic cron delivery directory: %w", err)
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return errors.New("generic cron delivery directory must be a real directory")
	}
	if err := os.Chmod(directory, 0700); err != nil {
		return fmt.Errorf("restrict generic cron delivery directory: %w", err)
	}
	return nil
}

func loadCronDeliveries(path string) ([]chat.ScheduledDelivery, error) {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return []chat.ScheduledDelivery{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("inspect generic cron deliveries: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return nil, errors.New("generic cron deliveries must be a regular file")
	}
	if info.Mode().Perm()&0077 != 0 {
		return nil, errors.New("generic cron deliveries permissions are too broad")
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("read generic cron deliveries: %w", err)
	}
	defer file.Close()
	openedInfo, err := file.Stat()
	if err != nil {
		return nil, fmt.Errorf("inspect opened generic cron deliveries: %w", err)
	}
	pathInfo, err := os.Lstat(path)
	if err != nil {
		return nil, fmt.Errorf("inspect generic cron deliveries: %w", err)
	}
	if pathInfo.Mode()&os.ModeSymlink != 0 || !os.SameFile(openedInfo, pathInfo) {
		return nil, errors.New("generic cron deliveries changed while they were opened")
	}
	data, err := io.ReadAll(io.LimitReader(file, maxCronDeliveryBytes+1))
	if err != nil {
		return nil, fmt.Errorf("read generic cron deliveries: %w", err)
	}
	if len(data) > maxCronDeliveryBytes {
		return nil, errors.New("generic cron deliveries exceed the size limit")
	}
	decoder := json.NewDecoder(strings.NewReader(string(data)))
	decoder.DisallowUnknownFields()
	var disk cronDeliveryFile
	if err := decoder.Decode(&disk); err != nil {
		return nil, fmt.Errorf("parse generic cron deliveries: %w", err)
	}
	var extra json.RawMessage
	if err := decoder.Decode(&extra); err != io.EOF {
		return nil, errors.New("parse generic cron deliveries: trailing data")
	}
	if disk.Version != cronDeliveryFileVersion || len(disk.Deliveries) > maxCronDeliveryCount {
		return nil, errors.New("unsupported generic cron deliveries file")
	}
	for _, delivery := range disk.Deliveries {
		if err := validateCronDelivery(delivery); err != nil {
			return nil, err
		}
	}
	return disk.Deliveries, nil
}

func saveCronDeliveries(path string, deliveries []chat.ScheduledDelivery) error {
	if len(deliveries) > maxCronDeliveryCount {
		return errors.New("too many generic cron deliveries")
	}
	for _, delivery := range deliveries {
		if err := validateCronDelivery(delivery); err != nil {
			return err
		}
	}
	data, err := json.Marshal(cronDeliveryFile{Version: cronDeliveryFileVersion, Deliveries: deliveries})
	if err != nil {
		return fmt.Errorf("encode generic cron deliveries: %w", err)
	}
	if len(data) > maxCronDeliveryBytes {
		return errors.New("generic cron deliveries exceed the size limit")
	}
	directory := filepath.Dir(path)
	temporary, err := os.CreateTemp(directory, ".cron-deliveries-*.tmp")
	if err != nil {
		return fmt.Errorf("create generic cron deliveries file: %w", err)
	}
	temporaryName := temporary.Name()
	defer os.Remove(temporaryName)
	if err := temporary.Chmod(0600); err != nil {
		_ = temporary.Close()
		return fmt.Errorf("restrict generic cron deliveries file: %w", err)
	}
	if n, err := temporary.Write(data); err != nil {
		_ = temporary.Close()
		return fmt.Errorf("write generic cron deliveries: %w", err)
	} else if n != len(data) {
		_ = temporary.Close()
		return errors.New("write generic cron deliveries: incomplete write")
	}
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()
		return fmt.Errorf("sync generic cron deliveries: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return fmt.Errorf("close generic cron deliveries: %w", err)
	}
	if info, err := os.Lstat(path); err == nil && info.Mode()&os.ModeSymlink != 0 {
		return errors.New("generic cron deliveries must not be a symbolic link")
	} else if err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("inspect generic cron deliveries before replacement: %w", err)
	}
	if err := os.Rename(temporaryName, path); err != nil {
		return fmt.Errorf("replace generic cron deliveries: %w", err)
	}
	if err := os.Chmod(path, 0600); err != nil {
		return fmt.Errorf("restrict generic cron deliveries: %w", err)
	}
	return syncCronDeliveryDirectory(directory)
}

func syncCronDeliveryDirectory(directory string) error {
	file, err := os.Open(directory)
	if err != nil {
		return fmt.Errorf("open generic cron delivery directory: %w", err)
	}
	syncErr := file.Sync()
	closeErr := file.Close()
	return errors.Join(syncErr, closeErr)
}

func validateCronDelivery(delivery chat.ScheduledDelivery) error {
	if !validCronDeliveryID(delivery.ID) || delivery.ChatID == 0 || strings.TrimSpace(delivery.Text) == "" || len(delivery.Text) > maxCronDeliveryTextSize {
		return errors.New("generic cron delivery is invalid")
	}
	if delivery.ThreadID < 0 {
		return errors.New("generic cron delivery thread is invalid")
	}
	return nil
}

func validCronDeliveryID(id string) bool {
	if len(id) < len("delivery-3-1") || len(id) > 96 || !strings.HasPrefix(id, "delivery-") {
		return false
	}
	parts := strings.Split(strings.TrimPrefix(id, "delivery-"), "-")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return false
	}
	for _, part := range parts {
		for _, character := range part {
			if character < '0' || character > '9' {
				return false
			}
		}
	}
	return true
}
