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
	if saved.Error != "scheduled reply could not be queued" {
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
	reload           func() cronSchedulerRuntime
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
		reload: func() cronSchedulerRuntime {
			var restarted cronSchedulerRuntime
			var err error
			restarted, err = scheduler.New(scheduler.Options{Path: jobs.Path(), Now: func() time.Time { return now }, JobExecutor: func(ctx context.Context, job scheduler.Job) error {
				return executeAgentCronJob(ctx, job, restarted, sessionFor, func(_ scheduler.Job, reply string) error {
					mu.Lock()
					defer mu.Unlock()
					queued = append(queued, reply)
					return nil
				}, create)
			}})
			if err != nil {
				t.Fatal(err)
			}
			return restarted
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
	if harness.unexpectedCreate() || len(harness.queuedReplies()) != 2 {
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
		return migration.OneShotResult{Status: "success", CreationState: "created", Code: "created", Handoff: "delivered", ServerName: request.ServerName}
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
		{name: "target_exists", result: migration.OneShotResult{Status: "stop", CreationState: "not_created", Code: "target_exists"}},
		{name: "success", result: migration.OneShotResult{Status: "success", CreationState: "created", Code: "created", Handoff: "delivered"}},
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
				result.ProviderMessage = "secret-provider-token"
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
			retryable := testCase.name != "target_exists" && testCase.name != "success"
			wantStatus := scheduler.StatusCompleted
			if retryable {
				wantStatus = scheduler.StatusScheduled
			}
			if !ok || saved.Enabled != retryable || saved.Status != wantStatus || saved.ReviewRequired || (retryable && (saved.RunCount != 0 || saved.NextRun == nil)) || (!retryable && (saved.RunCount != 1 || saved.NextRun != nil)) {
				t.Fatalf("terminal scheduler state = %+v", saved)
			}
			harness.advance(1800 * time.Second)
			wantCalls := 1
			if retryable {
				wantCalls = 2
			}
			if runs := harness.jobs.RunDue(context.Background()); calls != wantCalls || runs != wantCalls-1 {
				t.Fatalf("next run: runs=%d creates=%d", runs, calls)
			}
		})
	}
}

func TestGenericCronRealSessionMigrationSecondToolCallPreservesRetry(t *testing.T) {
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
	if !ok || !saved.Enabled || saved.ReviewRequired || createCalls != 1 || len(harness.queuedReplies()) != 1 {
		t.Fatalf("repeated tool state = %+v creates=%d queued=%d", saved, createCalls, len(harness.queuedReplies()))
	}
	if harness.jobs.RunDue(context.Background()) != 0 {
		t.Fatal("second migration tool call rearmed the job")
	}

}

func TestGenericCronRejectedSecondMigrationCallPreservesRetry(t *testing.T) {
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
			if !ok || !saved.Enabled || saved.ReviewRequired || calls != 1 || len(h.queuedReplies()) != 1 {
				t.Fatalf("state=%+v calls=%d replies=%v", saved, calls, h.queuedReplies())
			}
			h.advance(1800 * time.Second)
			if h.jobs.RunDue(context.Background()) != 1 || calls != 2 {
				t.Fatal("rejected call stopped job")
			}
		})
	}
}

func TestGenericCronRealSessionMigrationUnclassifiedErrorAfterNoCapacityRequiresReview(t *testing.T) {
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
	if !ok || saved.Enabled || !saved.ReviewRequired || saved.Status != scheduler.StatusFailed || createCalls != 1 {
		t.Fatalf("post-tool error state = %+v creates=%d", saved, createCalls)
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
		return migration.OneShotResult{Status: "success", CreationState: "created", Handoff: "delivered"}
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
						fmt.Fprint(w, `{"status":"retry_later","creation_state":"not_created","error":{"code":"provider_lookup_failure","stage":"provider","http_status":502}}`)
						return
					}
					secret := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
					w.WriteHeader(http.StatusCreated)
					fmt.Fprintf(w, `{"status":"success","creation_state":"created","attempt":"attempt1","server_label":"goaster-migration-0123456789abcdef","server_id":42,"ip":"192.0.2.1","location":"fsn1","expires":2000000000,"setup_url":"http://%s/setup#owner=%s"}`, r.Host, secret)
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
				return migration.CreateOnce(ctx, req, opts, owner)
			})
			job := addScheduledMigrationJob(t, h)
			var logs strings.Builder
			restore := diagnostics.SetOutput(&logs)
			runs := h.jobs.RunDue(context.Background())
			restore()
			saved, ok := h.jobs.Get(job.ID)
			if runs != 1 || !ok || saved.ReviewRequired || !saved.Enabled || creates.Load() != 1 {
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

func TestGenericCronNoCapacitySelfDisableCannotStopRetry(t *testing.T) {
	var harness realCronMigrationHarness
	harness = newRealCronMigrationHarness(t, singleMigrationToolModel(func(ctx context.Context, tool chat.Tool) (string, error) {
		result, err := tool.Handler(ctx, scheduledMigrationToolArguments)
		if err != nil {
			return "", err
		}
		if result, err := harness.selfDisable().Handler(ctx, `{}`); err != nil || !strings.Contains(result, `"disabled":false`) {
			t.Fatalf("self-disable result=%s err=%v", result, err)
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
	if !ok || !saved.Enabled || saved.NextRun == nil || saved.Status != scheduler.StatusScheduled {
		t.Fatalf("state = %+v", saved)
	}
	harness.advance(24 * time.Hour)
	restarted, err := scheduler.New(scheduler.Options{Path: harness.jobs.Path()})
	if err != nil {
		t.Fatal(err)
	}
	if restarted.RunDue(context.Background()) != 0 || harness.jobs.RunDue(context.Background()) != 1 || harness.modelCalls() != 2 {
		t.Fatal("protected retry did not run")
	}
}

func TestGenericCronUnknownSelfDisableCannotStopRetry(t *testing.T) {
	var harness realCronMigrationHarness
	harness = newRealCronMigrationHarness(t, singleMigrationToolModel(func(ctx context.Context, tool chat.Tool) (string, error) {
		result, err := tool.Handler(ctx, scheduledMigrationToolArguments)
		if err != nil {
			return "", err
		}
		if result, err := harness.selfDisable().Handler(ctx, `{}`); err != nil || !strings.Contains(result, `"disabled":false`) {
			t.Fatalf("self-disable result=%s err=%v", result, err)
		}
		return result, nil
	}), func(context.Context, migration.Request, int64) migration.OneShotResult {
		return migration.OneShotResult{CreationState: "unknown", Code: "response_unavailable"}
	})
	job := addScheduledMigrationJob(t, harness)
	if runs := harness.jobs.RunDue(context.Background()); runs != 1 {
		t.Fatalf("runs = %d", runs)
	}
	saved, ok := harness.jobs.Get(job.ID)
	if !ok || !saved.Enabled || saved.NextRun == nil || saved.Status != scheduler.StatusScheduled {
		t.Fatalf("state = %+v", saved)
	}
	harness.advance(24 * time.Hour)
	restarted, err := scheduler.New(scheduler.Options{Path: harness.jobs.Path()})
	if err != nil {
		t.Fatal(err)
	}
	if restarted.RunDue(context.Background()) != 0 || harness.jobs.RunDue(context.Background()) != 1 || harness.modelCalls() != 2 {
		t.Fatal("protected retry did not run")
	}
}

func TestRestartGuardCronRetryPersistsBeforeExecution(t *testing.T) {
	for _, kind := range []string{scheduler.ScheduleAt, scheduler.ScheduleEvery} {
		t.Run(kind, func(t *testing.T) {
			home := t.TempDir()
			t.Setenv("HOME", home)
			t.Setenv("USERPROFILE", home)
			now := time.Unix(2100000000, 0)
			calls := 0
			factory := func(int64, int64) agentSession {
				return genericCronSession{replyScheduled: func(context.Context, string) (string, error) { calls++; return "done", nil }}
			}
			newJobs := func() cronSchedulerRuntime {
				j, err := newCronSchedulerWithMigrationAt(factory, func(scheduler.Job, string) error { return nil }, nil, func() time.Time { return now })
				if err != nil {
					t.Fatal(err)
				}
				return j
			}
			jobs := newJobs()
			job := scheduler.Job{Kind: kind, Message: "reminder", OriginChatID: 42, OriginChatType: "private", MaxRuns: 1}
			if kind == scheduler.ScheduleAt {
				job.AtSeconds = 1
			} else {
				job.EverySeconds = 1
			}
			added, err := jobs.Add(job)
			if err != nil {
				t.Fatal(err)
			}
			now = now.Add(time.Second)
			restartWorkMu.Lock()
			jobs.RunDue(context.Background())
			restartWorkMu.Unlock()
			if calls != 0 {
				t.Fatal("work ran under guard")
			}
			jobs = newJobs()
			saved, ok := jobs.Get(added.ID)
			if !ok || !saved.Enabled || saved.ReviewRequired || saved.RunCount != 0 || saved.NextRun == nil {
				t.Fatalf("retry not durable: %+v", saved)
			}
			now = now.Add(6 * time.Second)
			jobs.RunDue(context.Background())
			jobs.RunDue(context.Background())
			if calls != 1 {
				t.Fatalf("executions = %d", calls)
			}
			jobs = newJobs()
			saved, ok = jobs.Get(added.ID)
			if ok && (saved.Enabled || saved.RunCount != 1) {
				t.Fatalf("completed state: %+v", saved)
			}
		})
	}
}

func TestGenericCronToolAddedMigrationRetriesAndStops(t *testing.T) {
	for _, limit := range []string{"omitted", "null", "0", "3"} {
		for _, outcome := range []string{"no_capacity", "target_exists", "created", "unknown", "timeout", "partial", "lookup_error", "auth_error"} {
			t.Run(limit+"/"+outcome, func(t *testing.T) {
				calls := 0
				h := newRealCronMigrationHarness(t, singleMigrationToolModel(func(ctx context.Context, tool chat.Tool) (string, error) {
					return tool.Handler(ctx, scheduledMigrationToolArguments)
				}), func(_ context.Context, req migration.Request, owner int64) migration.OneShotResult {
					calls++
					if owner != 42 || req.ServerName != "goaster-migration-0123456789abcdef" || req.ServerType != "cx22" || !reflect.DeepEqual(req.Locations, []string{"fsn1"}) {
						t.Fatalf("unexpected request: %+v owner=%d", req, owner)
					}
					switch outcome {
					case "created":
						return migration.OneShotResult{Status: "success", CreationState: "created", Handoff: "delivered"}
					case "unknown", "timeout":
						return migration.OneShotResult{CreationState: "unknown", Code: "response_unavailable"}
					case "auth_error":
						return migration.OneShotResult{CreationState: "not_created", Code: "provider_auth_rejected"}
					case "partial":
						return migration.OneShotResult{CreationState: "created", Code: "handoff_failed", Handoff: "failed"}
					case "lookup_error":
						return migration.OneShotResult{CreationState: "not_created", Code: "provider_lookup_failure"}
					}
					if outcome == "target_exists" {
						return migration.OneShotResult{Status: "stop", CreationState: "not_created", Code: outcome, ServerName: req.ServerName}
					}
					return migration.OneShotResult{Status: "retry_later", CreationState: "not_created", Code: outcome, ServerName: req.ServerName}
				})
				registrar := genericCronRegistrar{tools: make(chan chat.Tool, 1)}
				if err := chat.RegisterCronTool(registrar, h.jobs); err != nil {
					t.Fatal(err)
				}
				tool := <-registrar.tools
				ctx := chat.WithConversationID(chat.WithConversationType(context.Background(), "private"), 42)
				args := `{"action":"add","every_seconds":1800,"message":"migration retry","delete_after_run":false}`
				if limit != "omitted" {
					args = fmt.Sprintf(`{"action":"add","every_seconds":1800,"message":"migration retry","max_runs":%s,"delete_after_run":false}`, limit)
				}
				result, err := tool.Handler(ctx, args)
				if err != nil {
					t.Fatal(err)
				}
				jobs := h.jobs.List()
				if len(jobs) != 1 {
					t.Fatalf("add result %s: jobs=%d", result, len(jobs))
				}
				added := jobs[0]
				h.advance(1800 * time.Second)
				if h.jobs.RunDue(context.Background()) != 1 {
					t.Fatal("missing first run")
				}
				h.advance(1800 * time.Second)
				retryable := outcome != "target_exists" && outcome != "created"
				if retryable {
					if h.jobs.RunDue(context.Background()) != 1 || calls != 2 || (outcome == "no_capacity" && len(h.queuedReplies()) != 2) {
						t.Fatalf("retry calls=%d replies=%v", calls, h.queuedReplies())
					}
				} else if h.jobs.RunDue(context.Background()) != 0 || calls != 1 {
					t.Fatalf("terminal replay calls=%d", calls)
				}
				reloaded, err := scheduler.New(scheduler.Options{Path: h.jobs.Path()})
				if err != nil {
					t.Fatal(err)
				}
				saved, ok := reloaded.Get(added.ID)
				wantStatus := scheduler.StatusDisabled
				if retryable {
					wantStatus = scheduler.StatusScheduled
				}
				if !ok || saved.Enabled != retryable || saved.ReviewRequired || saved.Status != wantStatus || (retryable && (saved.RunCount != 0 || saved.NextRun == nil)) || (!retryable && (saved.RunCount != 1 || saved.NextRun != nil)) {
					t.Fatalf("reloaded: %+v", saved)
				}
				if retryable {
					restarted := h.reload()
					h.advance(1800 * time.Second)
					if restarted.RunDue(context.Background()) != 1 || calls != 3 {
						t.Fatalf("reloaded retry calls=%d", calls)
					}
				} else {
					restarted := h.reload()
					h.advance(24 * time.Hour)
					if restarted.RunDue(context.Background()) != 0 || calls != 1 {
						t.Fatalf("terminal result replayed after reload: calls=%d", calls)
					}
				}
			})
		}
	}
}

func TestGenericCronManualDisableDuringMigrationClaim(t *testing.T) {
	var h realCronMigrationHarness
	var jobID string
	h = newRealCronMigrationHarness(t, singleMigrationToolModel(func(ctx context.Context, tool chat.Tool) (string, error) {
		return tool.Handler(ctx, scheduledMigrationToolArguments)
	}), func(context.Context, migration.Request, int64) migration.OneShotResult {
		if _, err := h.jobs.SetEnabled(jobID, false); !errors.Is(err, scheduler.ErrExecutionInProgress) {
			t.Fatalf("disable during claim: %v", err)
		}
		return migration.OneShotResult{CreationState: "not_created", Code: "no_capacity"}
	})
	job := addScheduledMigrationJob(t, h)
	jobID = job.ID
	if h.jobs.RunDue(context.Background()) != 1 {
		t.Fatal("missing execution")
	}
	if _, err := h.jobs.SetEnabled(jobID, false); err != nil {
		t.Fatal(err)
	}
	h.advance(24 * time.Hour)
	if h.reload().RunDue(context.Background()) != 0 {
		t.Fatal("manually disabled job replayed")
	}
}

func TestGenericCronReminderCanSelfDisable(t *testing.T) {
	var h realCronMigrationHarness
	h = newRealCronMigrationHarness(t, singleMigrationToolModel(func(ctx context.Context, _ chat.Tool) (string, error) {
		return h.selfDisable().Handler(ctx, `{}`)
	}), func(context.Context, migration.Request, int64) migration.OneShotResult {
		t.Fatal("unexpected migration")
		return migration.OneShotResult{}
	})
	job := addScheduledMigrationJob(t, h)
	if h.jobs.RunDue(context.Background()) != 1 {
		t.Fatal("missing execution")
	}
	saved, ok := h.jobs.Get(job.ID)
	if !ok || saved.Enabled || saved.ReviewRequired {
		t.Fatalf("state: %+v", saved)
	}
}

func TestGenericCronSelfDisableBeforeMigrationIsImmediate(t *testing.T) {
	var h realCronMigrationHarness
	creates := 0
	h = newRealCronMigrationHarness(t, singleMigrationToolModel(func(ctx context.Context, tool chat.Tool) (string, error) {
		result, err := h.selfDisable().Handler(ctx, `{}`)
		if err != nil || !strings.Contains(result, `"disabled":true`) {
			t.Fatalf("self-disable result=%s err=%v", result, err)
		}
		return tool.Handler(ctx, scheduledMigrationToolArguments)
	}), func(context.Context, migration.Request, int64) migration.OneShotResult {
		creates++
		return migration.OneShotResult{CreationState: "not_created", Code: "no_capacity"}
	})
	job := addScheduledMigrationJob(t, h)
	h.jobs.RunDue(context.Background())
	saved, _ := h.jobs.Get(job.ID)
	if creates != 0 || saved.Enabled || !saved.ReviewRequired {
		t.Fatalf("state=%+v creates=%d", saved, creates)
	}
}

func TestGenericCronMissingCreateDiagnosticPrivacy(t *testing.T) {
	for _, invoke := range []bool{false, true} {
		t.Run(fmt.Sprintf("invoke_%t", invoke), func(t *testing.T) {
			h := newRealCronMigrationHarness(t, singleMigrationToolModel(func(ctx context.Context, tool chat.Tool) (string, error) {
				if invoke {
					return tool.Handler(ctx, scheduledMigrationToolArguments)
				}
				return "ordinary reminder", nil
			}), nil)
			job, err := h.jobs.Add(scheduler.Job{Kind: scheduler.ScheduleEvery, EverySeconds: 3600, Message: "private-message-marker", OriginChatID: 42, OriginChatType: "private"})
			if err != nil {
				t.Fatal(err)
			}
			h.advance(time.Hour)
			var logs strings.Builder
			restore := diagnostics.SetOutput(&logs)
			h.jobs.RunDue(context.Background())
			restore()
			log := logs.String()
			if invoke && (!strings.Contains(log, "migration_reason=\"create_unavailable\"") || strings.Count(log, "ERROR") != 1) {
				t.Fatalf("diagnostic=%q", log)
			}
			if !invoke && log != "" {
				t.Fatalf("reminder diagnostic=%q", log)
			}
			for _, secret := range []string{"private-message-marker", "private-name-marker", job.ID, "goaster-migration-0123456789abcdef"} {
				if strings.Contains(log, secret) {
					t.Fatalf("private value leaked: %q", log)
				}
			}
		})
	}
}

func TestScheduledMigrationAvailableAcrossSchedulesAndLimits(t *testing.T) {
	schedules := []scheduler.Job{
		{Kind: scheduler.ScheduleAt, AtSeconds: 60},
		{Kind: scheduler.ScheduleEvery, EverySeconds: 60},
		{Kind: scheduler.ScheduleEvery, EverySeconds: 1800},
		{Kind: scheduler.ScheduleCron, CronExpr: "* * * * *"},
	}
	for _, schedule := range schedules {
		for _, limit := range []int{0, 1, 3} {
			t.Run(fmt.Sprintf("%s_%d_%d_%d", schedule.Kind, schedule.AtSeconds, schedule.EverySeconds, limit), func(t *testing.T) {
				creates := 0
				h := newRealCronMigrationHarness(t, singleMigrationToolModel(func(ctx context.Context, tool chat.Tool) (string, error) {
					return tool.Handler(ctx, scheduledMigrationToolArguments)
				}), func(context.Context, migration.Request, int64) migration.OneShotResult {
					creates++
					return migration.OneShotResult{CreationState: "not_created", Code: "no_capacity"}
				})
				schedule.MaxRuns = limit
				schedule.Message = "ordinary message"
				schedule.OriginChatID = 42
				schedule.OriginChatType = "private"
				job, err := h.jobs.Add(schedule)
				if err != nil {
					t.Fatal(err)
				}
				h.advance(30 * time.Minute)
				if h.jobs.RunDue(context.Background()) != 1 {
					t.Fatal("job did not run")
				}
				saved, ok := h.jobs.Get(job.ID)
				if !ok || creates != 1 || !saved.Enabled || saved.RunCount != 0 || saved.ReviewRequired || len(h.queuedReplies()) != 1 {
					t.Fatalf("job=%+v creates=%d replies=%v", saved, creates, h.queuedReplies())
				}
			})
		}
	}
}

func TestScheduledMigrationRejectsNonPrivateOrigin(t *testing.T) {
	for _, origin := range []struct {
		kind       string
		id, thread int64
	}{{"group", 42, 0}, {"private", 42, 7}, {"private", -42, 0}} {
		t.Run(fmt.Sprintf("%s_%d_%d", origin.kind, origin.id, origin.thread), func(t *testing.T) {
			creates := 0
			h := newRealCronMigrationHarness(t, singleMigrationToolModel(func(ctx context.Context, tool chat.Tool) (string, error) {
				return tool.Handler(ctx, scheduledMigrationToolArguments)
			}), func(context.Context, migration.Request, int64) migration.OneShotResult {
				creates++
				return migration.OneShotResult{}
			})
			job, err := h.jobs.Add(scheduler.Job{Kind: scheduler.ScheduleEvery, EverySeconds: 60, Message: "test", OriginChatID: origin.id, OriginChatType: origin.kind, OriginThreadID: origin.thread})
			if err != nil {
				t.Fatal(err)
			}
			h.advance(time.Minute)
			h.jobs.RunDue(context.Background())
			saved, _ := h.jobs.Get(job.ID)
			if creates != 0 || !saved.ReviewRequired {
				t.Fatalf("creates=%d job=%+v", creates, saved)
			}
		})
	}
}

func TestMigrationPartialDeliveryNextRunStopsAtExistingName(t *testing.T) {
	calls := 0
	h := newRealCronMigrationHarness(t, singleMigrationToolModel(func(ctx context.Context, tool chat.Tool) (string, error) {
		return tool.Handler(ctx, scheduledMigrationToolArguments)
	}), func(_ context.Context, request migration.Request, _ int64) migration.OneShotResult {
		calls++
		if calls == 1 {
			return migration.OneShotResult{CreationState: "created", Handoff: "partial", Code: "handoff_failed", ServerName: request.ServerName}
		}
		return migration.OneShotResult{Status: "stop", CreationState: "not_created", Code: "target_exists", ServerName: request.ServerName}
	})
	job := addScheduledMigrationJob(t, h)
	if h.jobs.RunDue(context.Background()) != 1 {
		t.Fatal("missing first run")
	}
	saved, _ := h.jobs.Get(job.ID)
	if !saved.Enabled || saved.RunCount != 0 {
		t.Fatalf("partial state: %+v", saved)
	}
	h.advance(1800 * time.Second)
	if h.jobs.RunDue(context.Background()) != 1 {
		t.Fatal("missing next run")
	}
	saved, _ = h.jobs.Get(job.ID)
	if saved.Enabled || saved.ReviewRequired || calls != 2 {
		t.Fatalf("existing target state: %+v calls=%d", saved, calls)
	}
}

func TestMigrationTerminalRequiresMatchingTargetName(t *testing.T) {
	result := migration.OneShotResult{Status: "stop", CreationState: "not_created", Code: "target_exists", ServerName: "goaster-migration-ffffffffffffffff"}
	if !scheduledMigrationTerminal(result, "goaster-migration-0123456789abcdef") {
		t.Fatal("stop status must be terminal")
	}
}

type offlineMigrationTransport func(*http.Request) (*http.Response, error)

func (transport offlineMigrationTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	return transport(request)
}

func TestGenericCronMigrationModelFailureAndOtherTools(t *testing.T) {
	for _, scenario := range []string{"model", "other_success", "other_before", "handler", "storage", "interrupted"} {
		t.Run(scenario, func(t *testing.T) {
			other := scenario != "model"
			home := t.TempDir()
			t.Setenv("HOME", home)
			t.Setenv("USERPROFILE", home)
			store := chat.CredentialStore{Path: filepath.Join(home, "auth.json")}
			if err := store.Save(chat.Credential{AccessToken: "offline-test", ExpiresAt: time.Now().Add(time.Hour)}); err != nil {
				t.Fatal(err)
			}
			previous := http.DefaultTransport
			t.Cleanup(func() { http.DefaultTransport = previous })
			var calls, otherCalls int
			http.DefaultTransport = offlineMigrationTransport(func(request *http.Request) (*http.Response, error) {
				calls++
				body := `{"error":{"type":"invalid_request_error","code":"invalid_request"}}`
				status := http.StatusBadRequest
				if calls == 1 {
					status = http.StatusOK
					body = fmt.Sprintf(`{"output":[{"type":"function_call","call_id":"migration","name":"goaster_migration","arguments":%q}]}`, scheduledMigrationToolArguments)
					if other {
						body = fmt.Sprintf(`{"output":[{"type":"function_call","call_id":"migration","name":"goaster_migration","arguments":%q},{"type":"function_call","call_id":"other","name":"uncertain_test","arguments":"{}"}]}`, scheduledMigrationToolArguments)
					}
					if scenario == "other_before" || scenario == "handler" || scenario == "storage" || scenario == "interrupted" {
						body = fmt.Sprintf(`{"output":[{"type":"function_call","call_id":"other","name":"uncertain_test","arguments":"{}"},{"type":"function_call","call_id":"migration","name":"goaster_migration","arguments":%q}]}`, scheduledMigrationToolArguments)
					}
				}
				return &http.Response{StatusCode: status, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body)), Request: request}, nil
			})
			provider := chat.NewCodexProvider(store)
			if err := chat.RegisterMigrationTool(provider, chat.MigrationToolOperations{}); err != nil {
				t.Fatal(err)
			}
			if err := provider.RegisterTool(chat.Tool{Name: "uncertain_test", Description: "test", Parameters: map[string]any{"type": "object"}, Handler: func(context.Context, string) (string, error) {
				otherCalls++
				switch scenario {
				case "handler":
					return "", errors.New("uncertain action")
				case "storage":
					return "", os.ErrPermission
				case "interrupted":
					return "", context.Canceled
				}
				return "uncertain result", nil
			}}); err != nil {
				t.Fatal(err)
			}
			now := time.Unix(2100000000, 0)
			jobs, err := newCronSchedulerWithMigrationAt(func(int64, int64) agentSession { return chat.NewSession(chat.Config{Model: "offline-test"}, provider) }, func(scheduler.Job, string) error { return nil }, func(_ context.Context, request migration.Request, _ int64) migration.OneShotResult {
				return migration.OneShotResult{CreationState: "not_created", Code: "provider_http_error", ServerName: request.ServerName}
			}, func() time.Time { return now })
			if err != nil {
				t.Fatal(err)
			}
			job, err := jobs.Add(scheduler.Job{Kind: scheduler.ScheduleEvery, EverySeconds: 1800, Message: "migration", OriginChatID: 42, OriginChatType: "private"})
			if err != nil {
				t.Fatal(err)
			}
			now = now.Add(1800 * time.Second)
			observed := 0
			ctx := chat.WithToolCallObserver(context.Background(), func(string, string) { observed++ })
			jobs.RunDue(ctx)
			saved, _ := jobs.Get(job.ID)
			failedBefore := scenario == "handler" || scenario == "storage" || scenario == "interrupted"
			wantObserved := 1
			if scenario == "other_before" {
				wantObserved = 2
			}
			guarded := !failedBefore && saved.Enabled && !saved.ReviewRequired || failedBefore && (saved.ReviewRequired || saved.Status == "failed")
			wantOtherCalls := boolInt(scenario == "other_before" || failedBefore)
			if !guarded || observed != wantObserved || calls != 1 || otherCalls != wantOtherCalls {
				t.Fatalf("state=%+v observed=%d requests=%d other_calls=%d", saved, observed, calls, otherCalls)
			}
		})
	}
}

func boolInt(value bool) int {
	if value {
		return 1
	}
	return 0
}

func TestGenericCronCompletedMigrationToolOutcome(t *testing.T) {
	for _, tc := range []struct {
		name     string
		result   migration.OneShotResult
		terminal bool
	}{
		{"capacity", migration.OneShotResult{CreationState: "not_created", Code: "no_capacity"}, false},
		{"remote", migration.OneShotResult{CreationState: "not_created", Code: "provider_http_error"}, false},
		{"timeout", migration.OneShotResult{CreationState: "unknown", Code: "timeout"}, false},
		{"unknown", migration.OneShotResult{CreationState: "unknown"}, false},
		{"partial", migration.OneShotResult{CreationState: "created", Handoff: "failed"}, false},
		{"created", migration.OneShotResult{Status: "success", CreationState: "created", Handoff: "delivered"}, true},
		{"existing", migration.OneShotResult{Status: "stop", CreationState: "not_created", Code: "target_exists"}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			home := t.TempDir()
			t.Setenv("HOME", home)
			t.Setenv("USERPROFILE", home)
			store := chat.CredentialStore{Path: filepath.Join(home, "auth.json")}
			if err := store.Save(chat.Credential{AccessToken: "offline-test", ExpiresAt: time.Now().Add(time.Hour)}); err != nil {
				t.Fatal(err)
			}
			previous := http.DefaultTransport
			t.Cleanup(func() { http.DefaultTransport = previous })
			requests := 0
			http.DefaultTransport = offlineMigrationTransport(func(request *http.Request) (*http.Response, error) {
				requests++
				body := fmt.Sprintf(`{"output":[{"type":"function_call","call_id":"migration","name":"goaster_migration","arguments":%q},{"type":"function_call","call_id":"again","name":"goaster_migration","arguments":%q}]}`, scheduledMigrationToolArguments, scheduledMigrationToolArguments)
				return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body)), Request: request}, nil
			})
			provider := chat.NewCodexProvider(store)
			if err := chat.RegisterMigrationTool(provider, chat.MigrationToolOperations{}); err != nil {
				t.Fatal(err)
			}
			now := time.Unix(2100000000, 0)
			creates := 0
			queue, err := newCronDeliveryQueue(filepath.Join(home, "queue", "deliveries.json"))
			if err != nil {
				t.Fatal(err)
			}
			jobs, err := newCronSchedulerWithMigrationAt(func(int64, int64) agentSession { return chat.NewSession(chat.Config{Model: "offline-test"}, provider) }, queue.Enqueue, func(_ context.Context, request migration.Request, _ int64) migration.OneShotResult {
				creates++
				result := tc.result
				result.ServerName = request.ServerName
				return result
			}, func() time.Time { return now })
			if err != nil {
				t.Fatal(err)
			}
			job, err := jobs.Add(scheduler.Job{Kind: scheduler.ScheduleEvery, EverySeconds: 1800, Message: "migration", OriginChatID: 42, OriginChatType: "private", MaxRuns: 1})
			if err != nil {
				t.Fatal(err)
			}
			now = now.Add(1800 * time.Second)
			jobs.RunDue(context.Background())
			saved, _ := jobs.Get(job.ID)
			deliveries, err := queue.Pending()
			if err != nil || len(deliveries) != 1 || deliveries[0].ChatID != 42 || deliveries[0].Text != scheduledMigrationStatus(tc.result, tc.terminal) || strings.Contains(deliveries[0].Text, "secret-provider-token") {
				t.Fatalf("deliveries=%+v err=%v", deliveries, err)
			}
			if requests != 1 || creates != 1 || len(deliveries) != 1 || saved.Enabled == tc.terminal || saved.ReviewRequired || (!tc.terminal && saved.RunCount != 0) {
				t.Fatalf("requests=%d creates=%d enqueued=%d state=%+v", requests, creates, len(deliveries), saved)
			}
			restarted, err := scheduler.New(scheduler.Options{Path: jobs.Path(), Now: func() time.Time { return now }})
			if err != nil {
				t.Fatal(err)
			}
			persisted, ok := restarted.Get(job.ID)
			if !ok || persisted.Enabled == tc.terminal || (!tc.terminal && (persisted.NextRun == nil || !persisted.NextRun.Equal(now.Add(30*time.Minute)))) {
				t.Fatalf("persisted state=%+v", persisted)
			}
			if jobs.RunDue(context.Background()) != 0 || requests != 1 || creates != 1 {
				t.Fatal("migration replayed before the next interval")
			}
		})
	}
}

func TestGenericCronMigrationDirectCompletionLocalFailures(t *testing.T) {
	for _, scenario := range []string{"cancel_during_create", "diagnostic_checkpoint"} {
		t.Run(scenario, func(t *testing.T) {
			home := t.TempDir()
			t.Setenv("HOME", home)
			t.Setenv("USERPROFILE", home)
			credentials := chat.CredentialStore{Path: filepath.Join(home, "auth.json")}
			if err := credentials.Save(chat.Credential{AccessToken: "offline-test", ExpiresAt: time.Now().Add(time.Hour)}); err != nil {
				t.Fatal(err)
			}
			previous := http.DefaultTransport
			t.Cleanup(func() { http.DefaultTransport = previous })
			requests, creates, enqueued := 0, 0, 0
			http.DefaultTransport = offlineMigrationTransport(func(request *http.Request) (*http.Response, error) {
				requests++
				body := fmt.Sprintf(`{"output":[{"type":"function_call","call_id":"migration","name":"goaster_migration","arguments":%q},{"type":"function_call","call_id":"again","name":"goaster_migration","arguments":%q}]}`, scheduledMigrationToolArguments, scheduledMigrationToolArguments)
				return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body)), Request: request}, nil
			})
			provider := chat.NewCodexProvider(credentials)
			if err := chat.RegisterMigrationTool(provider, chat.MigrationToolOperations{}); err != nil {
				t.Fatal(err)
			}
			directory := filepath.Join(home, "conversation")
			if err := os.Mkdir(directory, 0700); err != nil {
				t.Fatal(err)
			}
			session, err := chat.NewPersistentSessionAt(chat.Config{Model: "offline-test"}, provider, filepath.Join(directory, "session.json"))
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			now := time.Unix(2100000000, 0)
			jobs, err := newCronSchedulerWithMigrationAt(func(int64, int64) agentSession { return session }, func(scheduler.Job, string) error { enqueued++; return nil }, func(callCtx context.Context, request migration.Request, _ int64) migration.OneShotResult {
				creates++
				if scenario == "cancel_during_create" {
					cancel()
					if !errors.Is(callCtx.Err(), context.Canceled) {
						t.Error("create context was not canceled")
					}
				} else {
					if err := os.Rename(directory, directory+"-moved"); err != nil {
						t.Fatal(err)
					}
					if err := os.WriteFile(directory, []byte("block session directory"), 0600); err != nil {
						t.Fatal(err)
					}
				}
				if scenario == "diagnostic_checkpoint" {
					return migration.OneShotResult{Status: "success", CreationState: "created", Handoff: "delivered", ServerName: request.ServerName}
				}
				return migration.OneShotResult{CreationState: "not_created", Code: "no_capacity", ServerName: request.ServerName}
			}, func() time.Time { return now })
			if err != nil {
				t.Fatal(err)
			}
			job, err := jobs.Add(scheduler.Job{Kind: scheduler.ScheduleEvery, EverySeconds: 1800, Message: "migration", OriginChatID: 42, OriginChatType: "private", MaxRuns: 1})
			if err != nil {
				t.Fatal(err)
			}
			now = now.Add(30 * time.Minute)
			jobs.RunDue(ctx)
			saved, ok := jobs.Get(job.ID)
			if !ok || !saved.ReviewRequired || requests != 1 || creates != 1 || enqueued != 1 {
				t.Fatalf("requests=%d creates=%d enqueued=%d state=%+v", requests, creates, enqueued, saved)
			}
			restarted, err := scheduler.New(scheduler.Options{Path: jobs.Path(), Now: func() time.Time { return now }})
			if err != nil {
				t.Fatal(err)
			}
			persisted, ok := restarted.Get(job.ID)
			if !ok || !persisted.ReviewRequired {
				t.Fatalf("persisted state=%+v", persisted)
			}
			now = now.Add(30 * time.Minute)
			if jobs.RunDue(context.Background()) != 0 || requests != 1 || creates != 1 {
				t.Fatal("review-blocked job replayed")
			}
		})
	}
}

func TestGenericCronPreToolFailureQueuesOriginAndQueueFailureRequiresReview(t *testing.T) {
	for _, failQueue := range []bool{false, true} {
		t.Run(fmt.Sprint(failQueue), func(t *testing.T) {
			home := t.TempDir()
			t.Setenv("HOME", home)
			t.Setenv("USERPROFILE", home)
			queue, err := newCronDeliveryQueue(filepath.Join(home, "delivery", "queue.json"))
			if err != nil {
				t.Fatal(err)
			}
			enqueue := queue.Enqueue
			if failQueue {
				enqueue = func(scheduler.Job, string) error { return errors.New("offline queue failure") }
			}
			now := time.Unix(2100000000, 0)
			jobs, err := newCronSchedulerWithMigrationAt(func(int64, int64) agentSession {
				return genericCronSession{replyScheduled: func(context.Context, string) (string, error) { return "", errors.New("offline model failure") }}
			}, enqueue, nil, func() time.Time { return now })
			if err != nil {
				t.Fatal(err)
			}
			job, err := jobs.Add(scheduler.Job{Kind: scheduler.ScheduleEvery, EverySeconds: 1800, Message: "scheduled task", OriginChatID: 42, OriginThreadID: 9, OriginChatType: "private"})
			if err != nil {
				t.Fatal(err)
			}
			now = now.Add(30 * time.Minute)
			jobs.RunDue(context.Background())
			deliveries, err := queue.Pending()
			if err != nil {
				t.Fatal(err)
			}
			if failQueue {
				saved, _ := jobs.Get(job.ID)
				if len(deliveries) != 0 || !saved.ReviewRequired {
					t.Fatalf("deliveries=%+v job=%+v", deliveries, saved)
				}
			} else if len(deliveries) != 1 || deliveries[0].ChatID != 42 || deliveries[0].ThreadID != 9 || deliveries[0].Text != "Scheduled run failed before completion. Check the job status before retrying." {
				t.Fatalf("deliveries=%+v", deliveries)
			}
		})
	}
}

func TestScheduledMigrationStatusSafeExactMessages(t *testing.T) {
	for _, tc := range []struct {
		result migration.OneShotResult
		want   string
	}{
		{migration.OneShotResult{CreationState: "not_created", Code: "provider_auth_rejected"}, "Provider authentication failed. No server was created. The job will retry in 30 minutes and check the existing name."},
		{migration.OneShotResult{CreationState: "not_created", Code: "provider_lookup_failure"}, "Provider lookup failed. No server was created. The job will retry in 30 minutes and check the existing name."},
		{migration.OneShotResult{CreationState: "not_created", Code: "provider_http_error"}, "Provider request failed. No server was created. The job will retry in 30 minutes and check the existing name."},
		{migration.OneShotResult{CreationState: "unknown", Code: "provider_timeout"}, "Provider request timed out. Creation outcome is unknown. The job will retry in 30 minutes and check the existing name."},
		{migration.OneShotResult{CreationState: "unknown", Code: "secret-provider-token", ServerName: "secret-provider-token"}, "Creation outcome is unknown. The job will retry in 30 minutes and check the existing name."},
	} {
		if got := scheduledMigrationStatus(tc.result, false); got != tc.want {
			t.Errorf("got %q want %q", got, tc.want)
		}
	}
}

type failingCronCoordinator struct{ claimErr, disableErr error }

func (c failingCronCoordinator) ClaimExecutionMatching(_ context.Context, job scheduler.Job) (scheduler.Job, func(error) (scheduler.Job, error), error) {
	return job, nil, c.claimErr
}
func (c failingCronCoordinator) DisableOwnExecution(_ context.Context, job scheduler.Job) (scheduler.Job, error) {
	return job, c.disableErr
}

func TestScheduledMigrationFailureNoticeAndCauseRetention(t *testing.T) {
	claimErr := errors.New("claim failure")
	disableErr := errors.New("disable failure")
	queueErr := errors.New("queue failure")
	for _, tc := range []struct {
		name        string
		coordinator failingCronCoordinator
		queueFail   bool
		original    error
	}{
		{"claim and queue", failingCronCoordinator{claimErr: claimErr}, true, claimErr},
		{"disable notice", failingCronCoordinator{disableErr: disableErr}, false, disableErr},
		{"disable and queue", failingCronCoordinator{disableErr: disableErr}, true, disableErr},
	} {
		t.Run(tc.name, func(t *testing.T) {
			registrar := genericCronRegistrar{tools: make(chan chat.Tool, 1)}
			if err := chat.RegisterMigrationTool(registrar, chat.MigrationToolOperations{}); err != nil {
				t.Fatal(err)
			}
			tool := <-registrar.tools
			provider := scheduledModelProvider{replyWithHistory: func(ctx context.Context, _ []chat.ChatMessage) (string, error) {
				return tool.Handler(ctx, scheduledMigrationToolArguments)
			}}
			session := chat.NewSession(chat.Config{Model: "offline-test"}, provider)
			var notices []string
			err := executeAgentCronJob(context.Background(), scheduler.Job{ID: "job", OriginChatID: 42, OriginChatType: "private"}, tc.coordinator, func(int64, int64) agentSession { return session }, func(_ scheduler.Job, text string) error {
				notices = append(notices, text)
				if tc.queueFail {
					return queueErr
				}
				return nil
			}, func(context.Context, migration.Request, int64) migration.OneShotResult {
				return migration.OneShotResult{Status: "success", CreationState: "created", Handoff: "delivered"}
			})
			if !errors.Is(err, tc.original) || (tc.queueFail && !errors.Is(err, queueErr)) {
				t.Fatalf("lost cause: %v", err)
			}
			if len(notices) != 1 {
				t.Fatalf("notices=%v", notices)
			}
			if tc.coordinator.disableErr != nil && notices[0] != "Migration attempt requires review; automatic stopping could not be confirmed." {
				t.Fatalf("notice=%q", notices[0])
			}
		})
	}
}

func TestGenericCronEarlyUnavailableNotice(t *testing.T) {
	queueErr := errors.New("queue failure")
	for _, tc := range []struct {
		name        string
		coordinator cronExecutionCoordinator
		factory     func(int64, int64) agentSession
		cause       string
	}{
		{"coordinator", nil, nil, "execution coordinator is unavailable"},
		{"session factory", failingCronCoordinator{}, nil, "session factory is unavailable"},
		{"session", failingCronCoordinator{}, func(int64, int64) agentSession { return nil }, "session is unavailable"},
	} {
		for _, failQueue := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/queue_failure=%v", tc.name, failQueue), func(t *testing.T) {
				var notices []string
				creates := 0
				err := executeAgentCronJob(context.Background(), scheduler.Job{OriginChatID: 42}, tc.coordinator, tc.factory, func(_ scheduler.Job, text string) error {
					notices = append(notices, text)
					if failQueue {
						return queueErr
					}
					return nil
				}, func(context.Context, migration.Request, int64) migration.OneShotResult {
					creates++
					return migration.OneShotResult{}
				})
				var review scheduler.ReviewRequiredError
				if !errors.As(err, &review) || review.Cause == nil || !strings.Contains(review.Cause.Error(), tc.cause) || (failQueue && !errors.Is(err, queueErr)) {
					t.Fatalf("lost cause: %v", err)
				}
				if creates != 0 || !reflect.DeepEqual(notices, []string{"Scheduled run failed before completion. Check the job status before retrying."}) {
					t.Fatalf("creates=%d notices=%v", creates, notices)
				}
			})
		}
	}
	calls := 0
	err := executeAgentCronJob(context.Background(), scheduler.Job{}, nil, nil, func(scheduler.Job, string) error { calls++; return nil }, nil)
	if err == nil || calls != 0 {
		t.Fatalf("missing origin: err=%v calls=%d", err, calls)
	}
}

func TestGenericCronOrdinaryReplyDelivery(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	for _, tc := range []struct{ reply, want string }{
		{"ordinary AI reply", "ordinary AI reply"},
		{" \n", "Scheduled run completed without a result."},
	} {
		t.Run(fmt.Sprintf("reply=%q", tc.reply), func(t *testing.T) {
			var notices []string
			creates := 0
			session := genericCronSession{replyScheduled: func(context.Context, string) (string, error) { return tc.reply, nil }}
			err := executeAgentCronJob(context.Background(), scheduler.Job{OriginChatID: 42}, failingCronCoordinator{}, func(int64, int64) agentSession { return session }, func(_ scheduler.Job, text string) error { notices = append(notices, text); return nil }, func(context.Context, migration.Request, int64) migration.OneShotResult {
				creates++
				return migration.OneShotResult{}
			})
			if err != nil || creates != 0 || !reflect.DeepEqual(notices, []string{tc.want}) {
				t.Fatalf("err=%v creates=%d notices=%v", err, creates, notices)
			}
		})
	}
}
