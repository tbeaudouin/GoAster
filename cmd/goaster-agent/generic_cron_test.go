package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"goaster/internal/attachment"
	"goaster/internal/chat"
	"goaster/internal/diagnostics"
	"goaster/internal/migration"
	"goaster/internal/scheduler"
	"goaster/internal/telegram"
)

type genericCronRegistrar struct {
	tools chan chat.Tool
}

func (registrar genericCronRegistrar) RegisterTool(tool chat.Tool) error {
	registrar.tools <- tool
	return nil
}

type genericCronSession struct {
	replyScheduled func(context.Context, string) (string, error)
}

func (session genericCronSession) Reply(ctx context.Context, message string) (string, error) {
	return session.replyScheduled(ctx, message)
}

func (session genericCronSession) ReplyScheduled(ctx context.Context, message string) (string, error) {
	return session.replyScheduled(ctx, message)
}

func (session genericCronSession) ReplyWithAttachments(ctx context.Context, message string, _ []attachment.Metadata) (string, error) {
	return session.replyScheduled(ctx, message)
}

func (genericCronSession) PendingModelRecoveryNotice() string { return "" }

func genericCronConversation(chatID, threadID int64) context.Context {
	ctx := chat.WithConversationType(context.Background(), "supergroup")
	ctx = chat.WithConversationID(ctx, chatID)
	return chat.WithConversationThreadID(ctx, threadID)
}

func TestGenericCronProductionLifecyclePersistsExecutionAndDelivery(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	entered := make(chan struct{})
	received := make(chan struct {
		chatID   int64
		threadID int64
		message  string
	}, 1)
	session := genericCronSession{replyScheduled: func(ctx context.Context, message string) (string, error) {
		chatID, _ := chat.ConversationID(ctx)
		threadID, _ := chat.ConversationThreadID(ctx)
		received <- struct {
			chatID   int64
			threadID int64
			message  string
		}{chatID: chatID, threadID: threadID, message: message}
		close(entered)
		return "scheduled reply", nil
	}}
	sessionFor := newAgentSessionFactory(func(int64) agentSession { return session })
	var deliveryQueue cronDeliveryQueue
	jobs, err := newCronScheduler(sessionFor, func(job scheduler.Job, reply string) error {
		return deliveryQueue.Enqueue(job, reply)
	})
	if err != nil {
		t.Fatal(err)
	}
	deliveryQueue, err = newCronDeliveryQueue(filepath.Join(filepath.Dir(jobs.Path()), "deliveries.json"))
	if err != nil {
		t.Fatal(err)
	}
	registrar := genericCronRegistrar{tools: make(chan chat.Tool, 1)}
	if err := chat.RegisterCronTool(registrar, jobs); err != nil {
		t.Fatal(err)
	}
	tool := <-registrar.tools
	if tool.Name != "cron" || tool.Handler == nil {
		t.Fatalf("production cron registration = %+v", tool)
	}
	result, err := tool.Handler(genericCronConversation(700, 33), `{"action":"add","every_seconds":1,"message":"scheduled message","max_runs":1}`)
	if err != nil {
		t.Fatal(err)
	}
	if len(result) == 0 {
		t.Fatal("cron add returned an empty result")
	}
	if err := jobs.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		jobs.Stop()
		t.Fatal("generic cron job did not execute")
	}
	jobs.Stop()
	request := <-received
	if request.message != "scheduled message" || request.chatID != 700 || request.threadID != 33 {
		t.Fatalf("scheduled request = %+v", request)
	}
	deliveries, err := deliveryQueue.Pending()
	if err != nil || len(deliveries) != 1 || deliveries[0].ChatID != 700 || deliveries[0].ThreadID != 33 || deliveries[0].Text != "scheduled reply" {
		t.Fatalf("scheduled deliveries = %+v, %v; jobs = %+v", deliveries, err, jobs.List())
	}
	restarted, err := scheduler.New(scheduler.Options{Path: jobs.Path()})
	if err != nil {
		t.Fatal(err)
	}
	saved, ok := restarted.Get(mustCronJobID(t, result))
	if !ok || saved.Status != scheduler.StatusCompleted || saved.RunCount != 1 || saved.Enabled || saved.OriginChatID != 700 || saved.OriginThreadID != 33 {
		t.Fatalf("persisted generic cron job = %+v", saved)
	}
	restartedQueue, err := newCronDeliveryQueue(filepath.Join(filepath.Dir(jobs.Path()), "deliveries.json"))
	if err != nil {
		t.Fatal(err)
	}
	restartedDeliveries, err := restartedQueue.Pending()
	if err != nil || len(restartedDeliveries) != 1 || restartedDeliveries[0].ID != deliveries[0].ID {
		t.Fatalf("restarted deliveries = %+v, %v", restartedDeliveries, err)
	}
	if err := restartedQueue.Ack([]string{deliveries[0].ID}); err != nil {
		t.Fatal(err)
	}
	if remaining, err := restartedQueue.Pending(); err != nil || len(remaining) != 0 {
		t.Fatalf("acknowledged deliveries = %+v, %v", remaining, err)
	}
}

func mustCronJobID(t *testing.T, result string) string {
	t.Helper()
	var job scheduler.Job
	if err := json.Unmarshal([]byte(result), &job); err != nil {
		t.Fatal(err)
	}
	if job.ID == "" {
		t.Fatal("cron result did not contain a job ID")
	}
	return job.ID
}

func TestGenericCronShutdownRetainsInterruptedExecutionForReview(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	entered := make(chan struct{})
	session := genericCronSession{replyScheduled: func(ctx context.Context, _ string) (string, error) {
		close(entered)
		<-ctx.Done()
		return "", scheduler.ReviewRequiredError{Message: "scheduled turn requires review", Cause: ctx.Err()}
	}}
	sessionFor := newAgentSessionFactory(func(int64) agentSession { return session })
	jobs, err := newCronScheduler(sessionFor, nil)
	if err != nil {
		t.Fatal(err)
	}
	job, err := jobs.Add(scheduler.Job{Kind: scheduler.ScheduleEvery, EverySeconds: 1, Message: "interrupt me", OriginChatID: 701, OriginChatType: "private"})
	if err != nil {
		t.Fatal(err)
	}
	if err := jobs.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		jobs.Stop()
		t.Fatal("generic cron job did not start before shutdown")
	}
	jobs.Stop()
	restarted, err := scheduler.New(scheduler.Options{Path: jobs.Path()})
	if err != nil {
		t.Fatal(err)
	}
	saved, ok := restarted.Get(job.ID)
	if !ok || saved.Enabled || !saved.ReviewRequired || saved.Status != scheduler.StatusFailed || saved.RunCount != 1 {
		t.Fatalf("shutdown state = %+v", saved)
	}
	if saved.Error != "scheduled turn requires review" {
		t.Fatalf("shutdown review error = %q", saved.Error)
	}
}

type scheduledModelProvider struct {
	register         func(chat.Tool) error
	replyWithHistory func(context.Context, []chat.ChatMessage) (string, error)
}

func (provider scheduledModelProvider) Reply(_ context.Context, _ string, _ string) (string, error) {
	return "", errors.New("offline provider requires history")
}

func (provider scheduledModelProvider) ReplyWithHistory(ctx context.Context, _ string, history []chat.ChatMessage) (string, error) {
	return provider.replyWithHistory(ctx, history)
}

func (provider scheduledModelProvider) RegisterTool(tool chat.Tool) error {
	return provider.register(tool)
}

type realCronMigrationHarness struct {
	jobs             cronSchedulerRuntime
	advance          func(time.Duration)
	modelCalls       func() int
	queuedReplies    func() []string
	unexpectedCreate func() bool
	selfDisable      func() chat.Tool
}

const scheduledMigrationToolArguments = `{"action":"create","server_type":"cx22","server_name":"goaster-migration-0123456789abcdef","locations":["fsn1"]}`

func newRealCronMigrationHarness(t *testing.T, model func(context.Context, int, chat.Tool) (string, error), create scheduledMigrationCreate) realCronMigrationHarness {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	var registered chat.Tool
	var selfDisable chat.Tool
	var calls int
	var queued []string
	var unexpected bool
	var mu sync.Mutex
	provider := scheduledModelProvider{
		register: func(tool chat.Tool) error {
			mu.Lock()
			defer mu.Unlock()
			switch tool.Name {
			case "goaster_migration":
				registered = tool
			case "cron_disable_self":
				selfDisable = tool
			}
			return nil
		},
		replyWithHistory: func(ctx context.Context, _ []chat.ChatMessage) (string, error) {
			mu.Lock()
			calls++
			call := calls
			tool := registered
			mu.Unlock()
			if tool.Handler == nil {
				return "", errors.New("offline migration tool was not registered")
			}
			return model(ctx, call, tool)
		},
	}
	if err := chat.RegisterMigrationTool(provider, chat.MigrationToolOperations{
		Create: func(context.Context, migration.Request, int64) migration.OneShotResult {
			mu.Lock()
			unexpected = true
			mu.Unlock()
			return migration.OneShotResult{CreationState: "not_created", Code: "unexpected_immediate_create"}
		},
	}); err != nil {
		t.Fatal(err)
	}
	now := time.Unix(2100000000, 0)
	sessionFor := newAgentSessionFactory(func(int64) agentSession {
		return chat.NewSession(chat.Config{Model: "offline-test"}, provider)
	})
	jobs, err := newCronSchedulerWithMigrationAt(sessionFor, func(_ scheduler.Job, reply string) error {
		mu.Lock()
		queued = append(queued, reply)
		mu.Unlock()
		return nil
	}, create, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	if err := chat.RegisterCronSelfDisableTool(provider, jobs); err != nil {
		t.Fatal(err)
	}
	return realCronMigrationHarness{
		jobs:    jobs,
		advance: func(duration time.Duration) { now = now.Add(duration) },
		modelCalls: func() int {
			mu.Lock()
			defer mu.Unlock()
			return calls
		},
		queuedReplies: func() []string {
			mu.Lock()
			defer mu.Unlock()
			return append([]string(nil), queued...)
		},
		unexpectedCreate: func() bool {
			mu.Lock()
			defer mu.Unlock()
			return unexpected
		},
		selfDisable: func() chat.Tool {
			mu.Lock()
			defer mu.Unlock()
			return selfDisable
		},
	}
}

func addScheduledMigrationJob(t *testing.T, harness realCronMigrationHarness) scheduler.Job {
	t.Helper()
	job, err := harness.jobs.Add(scheduler.Job{
		Kind:           scheduler.ScheduleEvery,
		EverySeconds:   1800,
		Message:        `{"action":"create","server_name":"goaster-migration-0123456789abcdef","server_type":"cx22","locations":["fsn1"]}`,
		MaxRuns:        1,
		OriginChatID:   42,
		OriginChatType: "private",
	})
	if err != nil {
		t.Fatal(err)
	}
	harness.advance(1800 * time.Second)
	return job
}

func singleMigrationToolModel(result func(context.Context, chat.Tool) (string, error)) func(context.Context, int, chat.Tool) (string, error) {
	return func(ctx context.Context, _ int, tool chat.Tool) (string, error) {
		return result(ctx, tool)
	}
}

func TestGenericCronRealSessionMigrationRepeatsOnlyNoCapacity(t *testing.T) {
	var requests []migration.Request
	create := func(_ context.Context, request migration.Request, owner int64) migration.OneShotResult {
		if owner != 42 {
			t.Fatalf("scheduled owner = %d, want 42", owner)
		}
		requests = append(requests, request)
		return migration.OneShotResult{CreationState: "not_created", Code: "no_capacity", ServerName: request.ServerName}
	}
	harness := newRealCronMigrationHarness(t, singleMigrationToolModel(func(ctx context.Context, tool chat.Tool) (string, error) {
		return tool.Handler(ctx, scheduledMigrationToolArguments)
	}), create)
	job := addScheduledMigrationJob(t, harness)
	if runs := harness.jobs.RunDue(context.Background()); runs != 1 {
		t.Fatalf("first no-capacity run count = %d", runs)
	}
	first, ok := harness.jobs.Get(job.ID)
	if !ok || !first.Enabled || first.Status != scheduler.StatusScheduled || first.RunCount != 0 || first.NextRun == nil {
		t.Fatalf("first no-capacity state = %+v", first)
	}
	harness.advance(1800 * time.Second)
	if runs := harness.jobs.RunDue(context.Background()); runs != 1 {
		t.Fatalf("second no-capacity run count = %d", runs)
	}
	second, ok := harness.jobs.Get(job.ID)
	if !ok || !second.Enabled || second.Status != scheduler.StatusScheduled || second.RunCount != 0 {
		t.Fatalf("second no-capacity state = %+v", second)
	}
	if harness.modelCalls() != 2 || len(requests) != 2 || !reflect.DeepEqual(requests[0], requests[1]) {
		t.Fatalf("repeated scheduled request changed: calls=%d requests=%+v", harness.modelCalls(), requests)
	}
	if requests[0].ServerName != "goaster-migration-0123456789abcdef" || requests[0].ServerType != "cx22" || !reflect.DeepEqual(requests[0].Locations, []string{"fsn1"}) {
		t.Fatalf("scheduled request = %+v", requests[0])
	}
	if harness.unexpectedCreate() || len(harness.queuedReplies()) != 0 {
		t.Fatalf("session/tool boundary = immediate=%v queued=%d", harness.unexpectedCreate(), len(harness.queuedReplies()))
	}
}

func TestGenericCronRealSessionMigrationCanDisableItselfAfterCreate(t *testing.T) {
	var harness realCronMigrationHarness
	harness = newRealCronMigrationHarness(t, singleMigrationToolModel(func(ctx context.Context, tool chat.Tool) (string, error) {
		result, err := tool.Handler(ctx, scheduledMigrationToolArguments)
		if err != nil {
			return "", err
		}
		selfDisable := harness.selfDisable()
		if selfDisable.Handler == nil || !selfDisable.ScheduledOnly {
			return "", errors.New("scheduled self-disable tool was not registered")
		}
		if _, err := selfDisable.Handler(ctx, `{}`); err != nil {
			return "", err
		}
		return result, nil
	}), func(_ context.Context, request migration.Request, _ int64) migration.OneShotResult {
		return migration.OneShotResult{CreationState: "created", Code: "created", Handoff: "delivered", ServerName: request.ServerName}
	})
	job := addScheduledMigrationJob(t, harness)
	if runs := harness.jobs.RunDue(context.Background()); runs != 1 {
		t.Fatalf("self-disable run count = %d", runs)
	}
	saved, ok := harness.jobs.Get(job.ID)
	if !ok || saved.Enabled || saved.Status != scheduler.StatusCompleted || saved.RunCount != 1 || saved.NextRun != nil {
		t.Fatalf("self-disabled scheduler state = %+v", saved)
	}
	if len(harness.queuedReplies()) != 1 {
		t.Fatalf("confirmed handoff reply count = %d", len(harness.queuedReplies()))
	}
	restarted, err := scheduler.New(scheduler.Options{Path: harness.jobs.Path()})
	if err != nil {
		t.Fatal(err)
	}
	if restarted.RunDue(context.Background()) != 0 {
		t.Fatal("self-disabled migration replayed after restart")
	}
	restartedJob, ok := restarted.Get(job.ID)
	if !ok || restartedJob.Enabled || restartedJob.Status != scheduler.StatusCompleted || restartedJob.RunCount != 1 {
		t.Fatalf("restarted self-disabled state = %+v", restartedJob)
	}
}

func TestGenericCronRealSessionMigrationTerminalResultsDoNotReplay(t *testing.T) {
	cases := []struct {
		name   string
		result migration.OneShotResult
	}{
		{name: "target_exists", result: migration.OneShotResult{CreationState: "not_created", Code: "target_exists"}},
		{name: "success", result: migration.OneShotResult{CreationState: "created", Code: "created", Handoff: "delivered"}},
		{name: "created_without_handoff", result: migration.OneShotResult{CreationState: "created", Code: "created"}},
		{name: "unknown", result: migration.OneShotResult{CreationState: "unknown", Code: "response_unavailable"}},
		{name: "partial", result: migration.OneShotResult{CreationState: "created", Code: "handoff_failed", Handoff: "failed"}},
		{name: "lookup_error", result: migration.OneShotResult{CreationState: "not_created", Code: "provider_lookup_failure"}},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			calls := 0
			create := func(_ context.Context, request migration.Request, owner int64) migration.OneShotResult {
				calls++
				if owner != 42 || request.ServerName != "goaster-migration-0123456789abcdef" {
					t.Fatalf("callback boundary owner/request = %d/%+v", owner, request)
				}
				result := testCase.result
				result.ServerName = request.ServerName
				return result
			}
			harness := newRealCronMigrationHarness(t, singleMigrationToolModel(func(ctx context.Context, tool chat.Tool) (string, error) {
				result, err := tool.Handler(ctx, scheduledMigrationToolArguments)
				if err != nil {
					return "", err
				}
				return "model observed " + result, nil
			}), create)
			job := addScheduledMigrationJob(t, harness)
			if runs := harness.jobs.RunDue(context.Background()); runs != 1 {
				t.Fatalf("terminal run count = %d", runs)
			}
			saved, ok := harness.jobs.Get(job.ID)
			needsReview := testCase.name == "created_without_handoff" || testCase.name == "unknown" || testCase.name == "partial" || testCase.name == "lookup_error"
			wantStatus := scheduler.StatusCompleted
			if needsReview {
				wantStatus = scheduler.StatusFailed
			}
			if !ok || saved.Enabled || saved.Status != wantStatus || saved.RunCount != 1 || saved.NextRun != nil || saved.ReviewRequired != needsReview {
				t.Fatalf("terminal scheduler state = %+v", saved)
			}
			harness.advance(1800 * time.Second)
			if runs := harness.jobs.RunDue(context.Background()); runs != 0 || calls != 1 {
				t.Fatalf("terminal result replayed: runs=%d creates=%d", runs, calls)
			}
		})
	}
}

func TestGenericCronRealSessionMigrationSecondToolCallDisarmsRetry(t *testing.T) {
	createCalls := 0
	harness := newRealCronMigrationHarness(t, singleMigrationToolModel(func(ctx context.Context, tool chat.Tool) (string, error) {
		first, err := tool.Handler(ctx, scheduledMigrationToolArguments)
		if err != nil {
			return "", err
		}
		second, err := tool.Handler(ctx, scheduledMigrationToolArguments)
		if err != nil {
			return "", err
		}
		return first + " / " + second, nil
	}), func(_ context.Context, request migration.Request, _ int64) migration.OneShotResult {
		createCalls++
		return migration.OneShotResult{CreationState: "not_created", Code: "no_capacity", ServerName: request.ServerName}
	})
	job := addScheduledMigrationJob(t, harness)
	if runs := harness.jobs.RunDue(context.Background()); runs != 1 {
		t.Fatalf("repeated tool run count = %d", runs)
	}
	saved, ok := harness.jobs.Get(job.ID)
	if !ok || saved.Enabled || saved.Status != scheduler.StatusFailed || !saved.ReviewRequired || createCalls != 1 || len(harness.queuedReplies()) != 1 {
		t.Fatalf("repeated tool state = %+v creates=%d queued=%d", saved, createCalls, len(harness.queuedReplies()))
	}
	if harness.jobs.RunDue(context.Background()) != 0 {
		t.Fatal("second migration tool call rearmed the job")
	}
	if !strings.Contains(harness.queuedReplies()[0], "already_attempted") {
		t.Fatalf("second tool result was not visible to the model: %q", harness.queuedReplies()[0])
	}
}

func TestGenericCronRejectedSecondMigrationCallDisarmsRetry(t *testing.T) {
	for _, tc := range []struct {
		name, args, code string
		cancel           bool
	}{
		{name: "malformed", args: `{"action":"create","server_type":7}`, code: "invalid_arguments"},
		{name: "wrong action", args: `{"action":"unsupported"}`, code: "invalid_arguments"},
		{name: "cancelled", args: scheduledMigrationToolArguments, code: "request_cancelled", cancel: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			h := newRealCronMigrationHarness(t, singleMigrationToolModel(func(ctx context.Context, tool chat.Tool) (string, error) {
				if _, err := tool.Handler(ctx, scheduledMigrationToolArguments); err != nil {
					return "", err
				}
				if tc.cancel {
					var cancel context.CancelFunc
					ctx, cancel = context.WithCancel(ctx)
					cancel()
				}
				return tool.Handler(ctx, tc.args)
			}), func(context.Context, migration.Request, int64) migration.OneShotResult {
				calls++
				return migration.OneShotResult{CreationState: "not_created", Code: "no_capacity"}
			})
			job := addScheduledMigrationJob(t, h)
			if h.jobs.RunDue(context.Background()) != 1 {
				t.Fatal("job did not run")
			}
			saved, ok := h.jobs.Get(job.ID)
			if !ok || saved.Enabled || !saved.ReviewRequired || calls != 1 || len(h.queuedReplies()) != 1 || !strings.Contains(h.queuedReplies()[0], tc.code) {
				t.Fatalf("state=%+v calls=%d replies=%v", saved, calls, h.queuedReplies())
			}
			h.advance(1800 * time.Second)
			if h.jobs.RunDue(context.Background()) != 0 || calls != 1 {
				t.Fatal("rejected call rearmed job")
			}
		})
	}
}

func TestGenericCronRealSessionMigrationErrorAfterNoCapacityRequiresReview(t *testing.T) {
	createCalls := 0
	harness := newRealCronMigrationHarness(t, singleMigrationToolModel(func(ctx context.Context, tool chat.Tool) (string, error) {
		if _, err := tool.Handler(ctx, scheduledMigrationToolArguments); err != nil {
			return "", err
		}
		return "", errors.New("offline model failed after migration tool")
	}), func(_ context.Context, request migration.Request, _ int64) migration.OneShotResult {
		createCalls++
		return migration.OneShotResult{CreationState: "not_created", Code: "no_capacity", ServerName: request.ServerName}
	})
	job := addScheduledMigrationJob(t, harness)
	if runs := harness.jobs.RunDue(context.Background()); runs != 1 {
		t.Fatalf("post-tool error run count = %d", runs)
	}
	saved, ok := harness.jobs.Get(job.ID)
	if !ok || saved.Enabled || !saved.ReviewRequired || saved.Status != scheduler.StatusFailed || saved.RunCount != 1 || saved.NextRun != nil || createCalls != 1 {
		t.Fatalf("post-tool error state = %+v creates=%d", saved, createCalls)
	}
	harness.advance(1800 * time.Second)
	if runs := harness.jobs.RunDue(context.Background()); runs != 0 || createCalls != 1 {
		t.Fatalf("post-tool error rearmed: runs=%d creates=%d", runs, createCalls)
	}
}

func TestGenericCronRealSessionMigrationTimeoutStopsWithoutCallback(t *testing.T) {
	createCalls := 0
	harness := newRealCronMigrationHarness(t, singleMigrationToolModel(func(ctx context.Context, _ chat.Tool) (string, error) {
		<-ctx.Done()
		return "", ctx.Err()
	}), func(_ context.Context, request migration.Request, _ int64) migration.OneShotResult {
		createCalls++
		return migration.OneShotResult{CreationState: "not_created", Code: "no_capacity", ServerName: request.ServerName}
	})
	job := addScheduledMigrationJob(t, harness)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if runs := harness.jobs.RunDue(ctx); runs != 1 {
		t.Fatalf("timeout run count = %d", runs)
	}
	saved, ok := harness.jobs.Get(job.ID)
	if !ok || saved.Enabled || !saved.ReviewRequired || saved.Status != scheduler.StatusFailed || saved.RunCount != 1 || createCalls != 0 {
		t.Fatalf("timeout state = %+v creates=%d", saved, createCalls)
	}
	if harness.jobs.RunDue(context.Background()) != 0 {
		t.Fatal("timed-out session was replayed")
	}
}

func TestGenericCronOrdinaryRecurringRunsTwice(t *testing.T) {
	h := newRealCronMigrationHarness(t, func(context.Context, int, chat.Tool) (string, error) { return "reminder", nil }, func(context.Context, migration.Request, int64) migration.OneShotResult {
		t.Error("unexpected create")
		return migration.OneShotResult{}
	})
	job, err := h.jobs.Add(scheduler.Job{Kind: scheduler.ScheduleEvery, EverySeconds: 60, Message: "reminder", OriginChatID: 42, OriginChatType: "private"})
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		h.advance(60 * time.Second)
		if h.jobs.RunDue(context.Background()) != 1 {
			t.Fatal("recurring job did not run")
		}
	}
	saved, _ := h.jobs.Get(job.ID)
	if !saved.Enabled || saved.RunCount != 2 || saved.NextRun == nil || len(h.queuedReplies()) != 2 {
		t.Fatalf("recurring state: %+v", saved)
	}
}

func TestGenericCronSameShapeBusyRetries(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	now := time.Unix(2100000000, 0)
	jobs, err := newCronSchedulerWithMigrationAt(func(int64, int64) agentSession {
		return genericCronSession{replyScheduled: func(context.Context, string) (string, error) { return "", chat.ErrSessionBusy }}
	}, func(scheduler.Job, string) error { return nil }, func(context.Context, migration.Request, int64) migration.OneShotResult {
		t.Error("unexpected create")
		return migration.OneShotResult{}
	}, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	job, err := jobs.Add(scheduler.Job{Kind: scheduler.ScheduleEvery, EverySeconds: 1800, MaxRuns: 1, Message: "reminder", OriginChatID: 42, OriginChatType: "private"})
	if err != nil {
		t.Fatal(err)
	}
	now = now.Add(1800 * time.Second)
	jobs.RunDue(context.Background())
	saved, _ := jobs.Get(job.ID)
	if !saved.Enabled || saved.ReviewRequired || saved.RunCount != 0 || saved.NextRun == nil {
		t.Fatalf("busy state: %+v", saved)
	}
}

func TestGenericCronLazyClaimRejectsChangedOrStoppedJob(t *testing.T) {
	for _, action := range []string{"changed", "stopped"} {
		t.Run(action, func(t *testing.T) {
			var h realCronMigrationHarness
			var id string
			creates := 0
			h = newRealCronMigrationHarness(t, singleMigrationToolModel(func(ctx context.Context, tool chat.Tool) (string, error) {
				if action == "stopped" {
					if _, err := h.jobs.SetEnabled(id, false); err != nil {
						t.Fatal(err)
					}
				} else {
					registrar := genericCronRegistrar{tools: make(chan chat.Tool, 1)}
					if err := chat.RegisterCronTool(registrar, h.jobs); err != nil {
						t.Fatal(err)
					}
					tool := <-registrar.tools
					if _, err := tool.Handler(ctx, `{"action":"update","id":"`+id+`","message":"changed"}`); err != nil {
						t.Fatal(err)
					}
				}
				return tool.Handler(ctx, scheduledMigrationToolArguments)
			}), func(context.Context, migration.Request, int64) migration.OneShotResult {
				creates++
				return migration.OneShotResult{}
			})
			job := addScheduledMigrationJob(t, h)
			id = job.ID
			h.jobs.RunDue(context.Background())
			if creates != 0 {
				t.Fatal("created after job changed")
			}
			saved, _ := h.jobs.Get(id)
			if saved.Enabled || !saved.ReviewRequired {
				t.Fatalf("claim rejection state: %+v", saved)
			}
		})
	}
}

func TestGenericCronMigrationCrashClaimDoesNotReplay(t *testing.T) {
	var h realCronMigrationHarness
	var snapshot []byte
	creates := 0
	h = newRealCronMigrationHarness(t, singleMigrationToolModel(func(ctx context.Context, tool chat.Tool) (string, error) {
		return tool.Handler(ctx, scheduledMigrationToolArguments)
	}), func(context.Context, migration.Request, int64) migration.OneShotResult {
		creates++
		var err error
		snapshot, err = os.ReadFile(h.jobs.Path())
		if err != nil {
			t.Fatal(err)
		}
		return migration.OneShotResult{CreationState: "created", Handoff: "delivered"}
	})
	job := addScheduledMigrationJob(t, h)
	h.jobs.RunDue(context.Background())
	if err := os.WriteFile(h.jobs.Path(), snapshot, 0600); err != nil {
		t.Fatal(err)
	}
	restarted, err := scheduler.New(scheduler.Options{Path: h.jobs.Path()})
	if err != nil {
		t.Fatal(err)
	}
	if restarted.RunDue(context.Background()) != 0 || creates != 1 {
		t.Fatal("interrupted paid attempt replayed")
	}
	saved, ok := restarted.Get(job.ID)
	if !ok || saved.Enabled || !saved.ReviewRequired {
		t.Fatalf("recovered claim: %+v", saved)
	}
}

type alreadyLoggedOutcomeError struct{}

func (alreadyLoggedOutcomeError) Error() string     { return "delivery failed" }
func (alreadyLoggedOutcomeError) DiagnosticLogged() {}

type outcomeLog chan string

func (log outcomeLog) Write(data []byte) (int, error) { log <- string(data); return len(data), nil }

func TestScheduledOutcomeHonorsDiagnosticOwnership(t *testing.T) {
	logs := make(outcomeLog, 4)
	restore := diagnostics.SetOutput(logs)
	result := reportScheduledMigrationOutcome(context.Background(), 42, migration.OneShotResult{}, func(context.Context, int64, migration.OneShotResult) error { return alreadyLoggedOutcomeError{} })
	restore()
	if result.Code != "outcome_failed" || len(logs) != 0 {
		t.Fatalf("result=%+v duplicate logs=%d", result, len(logs))
	}
}

func TestScheduledNoCapacityOutcomeIsSilent(t *testing.T) {
	reports := 0
	want := migration.OneShotResult{CreationState: "not_created", Code: "no_capacity", ServerName: "goaster-migration-0123456789abcdef"}
	got := reportScheduledMigrationOutcome(context.Background(), 42, want, func(context.Context, int64, migration.OneShotResult) error {
		reports++
		return nil
	})
	if !reflect.DeepEqual(got, want) || reports != 0 {
		t.Fatalf("no-capacity outcome = %+v reports=%d", got, reports)
	}
}

func TestScheduledMigrationDiagnosticOwnership(t *testing.T) {
	for _, split := range []bool{false, true} {
		name := "provider lookup"
		if split {
			name = "split handoff EOF"
		}
		t.Run(name, func(t *testing.T) {
			const token = "123456789:abcdefghijklmnopqrstuvwxyzABCDE_12345"
			const providerToken = "private-provider-token"
			dir := t.TempDir()
			paths := telegram.Paths{Token: filepath.Join(dir, "token"), UserID: filepath.Join(dir, "user-id")}
			if err := os.WriteFile(paths.Token, []byte(token), 0600); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(paths.UserID, []byte("42"), 0600); err != nil {
				t.Fatal(err)
			}
			var creates, messages, uploads atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/api/migration/create" {
					creates.Add(1)
					if !split {
						w.WriteHeader(http.StatusBadGateway)
						fmt.Fprint(w, `{"creation_state":"not_created","error":{"code":"provider_lookup_failure","stage":"provider","http_status":502}}`)
						return
					}
					secret := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
					w.WriteHeader(http.StatusCreated)
					fmt.Fprintf(w, `{"creation_state":"created","attempt":"attempt1","server_label":"goaster-migration-0123456789abcdef","server_id":42,"ip":"192.0.2.1","location":"fsn1","expires":2000000000,"setup_url":"http://%s/setup#owner=%s"}`, r.Host, secret)
					return
				}
				io.Copy(io.Discard, r.Body)
				if strings.HasSuffix(r.URL.Path, "/sendMessage") {
					messages.Add(1)
					fmt.Fprint(w, `{"ok":true,"result":{"message_id":1}}`)
					return
				}
				uploads.Add(1)
				conn, _, err := w.(http.Hijacker).Hijack()
				if err != nil {
					t.Error(err)
					return
				}
				conn.Close()
			}))
			defer server.Close()
			opts := migration.DefaultOptions()
			opts.WebOrigin = server.URL
			opts.AllowInsecureHTTP = true
			opts.HTTPClient = server.Client()
			opts.LoadHetznerToken = func() (string, error) { return providerToken, nil }
			opts.MigrationRecipient = func() (int64, error) { return 42, nil }
			opts.PrepareRecipientHandoffParts = func(id int64) (func(context.Context, string, []byte) error, error) {
				return telegram.PrepareRecipientMigrationHandoffPartsWith(paths, server.Client(), server.URL, id)
			}
			h := newRealCronMigrationHarness(t, singleMigrationToolModel(func(ctx context.Context, tool chat.Tool) (string, error) {
				return tool.Handler(ctx, scheduledMigrationToolArguments)
			}), func(ctx context.Context, req migration.Request, owner int64) migration.OneShotResult {
				return reportScheduledMigrationOutcome(ctx, owner, migration.CreateOnce(ctx, req, opts, owner), func(context.Context, int64, migration.OneShotResult) error { return nil })
			})
			job := addScheduledMigrationJob(t, h)
			var logs strings.Builder
			restore := diagnostics.SetOutput(&logs)
			runs := h.jobs.RunDue(context.Background())
			restore()
			saved, ok := h.jobs.Get(job.ID)
			if runs != 1 || !ok || !saved.ReviewRequired || saved.Enabled || creates.Load() != 1 {
				t.Fatalf("runs=%d state=%+v creates=%d", runs, saved, creates.Load())
			}
			if split && (messages.Load() != 1 || uploads.Load() != 1) {
				t.Fatalf("messages=%d uploads=%d", messages.Load(), uploads.Load())
			}
			log := logs.String()
			owner := "migration.create"
			if split {
				owner = "telegram.api"
			}
			if strings.Count(log, "ERROR") != 1 || !strings.Contains(log, owner) {
				t.Fatalf("unexpected diagnostic: %s", log)
			}
			for _, secret := range []string{token, providerToken, "PRIVATE KEY", server.URL} {
				if strings.Contains(log, secret) {
					t.Fatal("secret in diagnostic")
				}
			}
		})
	}
}

func TestGenericCronNoCapacitySelfDisableWinsRetry(t *testing.T) {
	var harness realCronMigrationHarness
	harness = newRealCronMigrationHarness(t, singleMigrationToolModel(func(ctx context.Context, tool chat.Tool) (string, error) {
		result, err := tool.Handler(ctx, scheduledMigrationToolArguments)
		if err != nil {
			return "", err
		}
		if _, err := harness.selfDisable().Handler(ctx, `{}`); err != nil {
			return "", err
		}
		return result, nil
	}), func(context.Context, migration.Request, int64) migration.OneShotResult {
		return migration.OneShotResult{CreationState: "not_created", Code: "no_capacity"}
	})
	job := addScheduledMigrationJob(t, harness)
	if runs := harness.jobs.RunDue(context.Background()); runs != 1 {
		t.Fatalf("runs = %d", runs)
	}
	saved, ok := harness.jobs.Get(job.ID)
	if !ok || saved.Enabled || saved.NextRun != nil || saved.Status != scheduler.StatusDisabled {
		t.Fatalf("state = %+v", saved)
	}
	harness.advance(24 * time.Hour)
	restarted, err := scheduler.New(scheduler.Options{Path: harness.jobs.Path()})
	if err != nil {
		t.Fatal(err)
	}
	if restarted.RunDue(context.Background()) != 0 || harness.jobs.RunDue(context.Background()) != 0 || harness.modelCalls() != 1 {
		t.Fatal("self-disabled job replayed")
	}
}
