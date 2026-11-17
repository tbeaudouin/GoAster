package chat

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"goaster/internal/scheduler"
)

type cronToolTestRegistrar struct {
	tool Tool
}

func (r *cronToolTestRegistrar) RegisterTool(tool Tool) error {
	r.tool = tool
	return nil
}

func TestCronToolManagesJobsAndPreservesOmittedFields(t *testing.T) {
	jobs, err := scheduler.New(scheduler.Options{
		Path: filepath.Join(t.TempDir(), "jobs.json"),
		Now:  func() time.Time { return time.Unix(1000, 0) },
	})
	if err != nil {
		t.Fatal(err)
	}
	registrar := &cronToolTestRegistrar{}
	if err := RegisterCronTool(registrar, jobs); err != nil {
		t.Fatal(err)
	}
	if registrar.tool.Name != cronToolName || registrar.tool.Handler == nil {
		t.Fatalf("registered cron tool = %+v", registrar.tool)
	}

	result, err := registrar.tool.Handler(context.Background(), `{"action":"add","every_seconds":60,"message":"first message","max_runs":3}`)
	if err != nil {
		t.Fatal(err)
	}
	var added scheduler.Job
	if err := json.Unmarshal([]byte(result), &added); err != nil {
		t.Fatalf("decode add result %q: %v", result, err)
	}
	if added.Kind != scheduler.ScheduleEvery || added.EverySeconds != 60 || added.MaxRuns != 3 || added.Message != "first message" {
		t.Fatalf("added job = %+v", added)
	}

	result, err = registrar.tool.Handler(context.Background(), `{"action":"update","id":"`+added.ID+`","message":"updated message"}`)
	if err != nil {
		t.Fatal(err)
	}
	var updated scheduler.Job
	if err := json.Unmarshal([]byte(result), &updated); err != nil {
		t.Fatalf("decode update result %q: %v", result, err)
	}
	if updated.Message != "updated message" || updated.Kind != scheduler.ScheduleEvery || updated.EverySeconds != 60 || updated.MaxRuns != 3 || updated.NextRun == nil {
		t.Fatalf("partial tool update did not preserve fields: %+v", updated)
	}

	if _, err := registrar.tool.Handler(context.Background(), `{"action":"disable","id":"`+added.ID+`"}`); err != nil {
		t.Fatal(err)
	}
	if _, err := registrar.tool.Handler(context.Background(), `{"action":"enable","id":"`+added.ID+`"}`); err != nil {
		t.Fatal(err)
	}
	listResult, err := registrar.tool.Handler(context.Background(), `{"action":"list"}`)
	if err != nil {
		t.Fatal(err)
	}
	var listed struct {
		Jobs []scheduler.Job `json:"jobs"`
	}
	if err := json.Unmarshal([]byte(listResult), &listed); err != nil {
		t.Fatalf("decode list result %q: %v", listResult, err)
	}
	if len(listed.Jobs) != 1 || listed.Jobs[0].ID != added.ID {
		t.Fatalf("listed jobs = %+v", listed.Jobs)
	}
	if _, err := registrar.tool.Handler(context.Background(), `{"action":"remove","id":"`+added.ID+`"}`); err != nil {
		t.Fatal(err)
	}
	if _, ok := jobs.Get(added.ID); ok {
		t.Fatal("cron tool remove left job behind")
	}
}

func TestCronToolSupportsAtAndCronSchedulesAndRejectsAmbiguousAdd(t *testing.T) {
	clock := time.Unix(1000, 0)
	jobs, err := scheduler.New(scheduler.Options{Path: filepath.Join(t.TempDir(), "jobs.json"), Now: func() time.Time { return clock }})
	if err != nil {
		t.Fatal(err)
	}
	registrar := &cronToolTestRegistrar{}
	if err := RegisterCronTool(registrar, jobs); err != nil {
		t.Fatal(err)
	}
	atSeconds := int64(3600)
	result, err := registrar.tool.Handler(context.Background(), `{"action":"add","at_seconds":`+formatInt64(atSeconds)+`,"message":"later"}`)
	if err != nil {
		t.Fatal(err)
	}
	var atJob scheduler.Job
	if err := json.Unmarshal([]byte(result), &atJob); err != nil {
		t.Fatal(err)
	}
	if atJob.Kind != scheduler.ScheduleAt || atJob.AtSeconds != atSeconds || !atJob.DeleteAfterRun || atJob.NextRun == nil || !atJob.NextRun.Equal(clock.Add(time.Duration(atSeconds)*time.Second)) {
		t.Fatalf("at job = %+v, want delete-after-run", atJob)
	}
	if _, err := registrar.tool.Handler(context.Background(), `{"action":"add","at_seconds":0,"message":"invalid"}`); err == nil {
		t.Fatal("at_seconds=0 unexpectedly succeeded")
	}
	updatedAtSeconds := int64(10)
	result, err = registrar.tool.Handler(context.Background(), `{"action":"update","id":"`+atJob.ID+`","at_seconds":`+formatInt64(updatedAtSeconds)+`}`)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal([]byte(result), &atJob); err != nil {
		t.Fatal(err)
	}
	if atJob.AtSeconds != updatedAtSeconds || atJob.NextRun == nil || !atJob.NextRun.Equal(clock.Add(time.Duration(updatedAtSeconds)*time.Second)) {
		t.Fatalf("updated at job = %+v, want 10 seconds from now", atJob)
	}
	result, err = registrar.tool.Handler(context.Background(), `{"action":"add","cron_expr":"0 9 * * 1","message":"weekly"}`)
	if err != nil {
		t.Fatal(err)
	}
	var cronJob scheduler.Job
	if err := json.Unmarshal([]byte(result), &cronJob); err != nil {
		t.Fatal(err)
	}
	if cronJob.Kind != scheduler.ScheduleCron || cronJob.CronExpr != "0 9 * * 1" {
		t.Fatalf("cron job = %+v", cronJob)
	}
	if _, err := registrar.tool.Handler(context.Background(), `{"action":"add","every_seconds":10,"cron_expr":"* * * * *","message":"ambiguous"}`); err == nil {
		t.Fatal("ambiguous cron add unexpectedly succeeded")
	}
	if description, ok := registrar.tool.Parameters["properties"].(map[string]any)["at_seconds"].(map[string]any)["description"].(string); !ok || description == "" {
		t.Fatal("at_seconds tool description is missing")
	} else if description != "Positive number of seconds from now for a one-time chat turn" {
		t.Fatalf("at_seconds tool description = %q", description)
	}
}

func TestCronToolScopesManagementAndExecutionOriginToConversation(t *testing.T) {
	jobs, err := scheduler.New(scheduler.Options{Path: filepath.Join(t.TempDir(), "jobs.json"), Now: func() time.Time { return time.Unix(1000, 0) }})
	if err != nil {
		t.Fatal(err)
	}
	registrar := &cronToolTestRegistrar{}
	if err := RegisterCronTool(registrar, jobs); err != nil {
		t.Fatal(err)
	}
	conversation := func(chatID, threadID int64) context.Context {
		ctx := WithConversationType(context.Background(), "group")
		ctx = WithConversationID(ctx, chatID)
		return WithConversationThreadID(ctx, threadID)
	}
	add := func(ctx context.Context, message string) scheduler.Job {
		result, err := registrar.tool.Handler(ctx, `{"action":"add","every_seconds":60,"message":"`+message+`"}`)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(result, "origin_chat") {
			t.Fatalf("cron result exposed transport origin: %s", result)
		}
		var job scheduler.Job
		if err := json.Unmarshal([]byte(result), &job); err != nil {
			t.Fatal(err)
		}
		return job
	}

	first := add(conversation(101, 7), "first")
	second := add(conversation(202, 8), "second")
	if _, err := registrar.tool.Handler(conversation(101, 7), `{"action":"add","every_seconds":60,"message":"spoof","chat_id":202}`); err == nil {
		t.Fatal("model-supplied cron recipient unexpectedly succeeded")
	}
	for _, test := range []struct {
		name     string
		ctx      context.Context
		wantID   string
		wantSize int
	}{{"first", conversation(101, 7), first.ID, 1}, {"second", conversation(202, 8), second.ID, 1}} {
		t.Run(test.name, func(t *testing.T) {
			result, err := registrar.tool.Handler(test.ctx, `{"action":"list"}`)
			if err != nil {
				t.Fatal(err)
			}
			var listed struct {
				Jobs []scheduler.Job `json:"jobs"`
			}
			if err := json.Unmarshal([]byte(result), &listed); err != nil {
				t.Fatal(err)
			}
			if len(listed.Jobs) != test.wantSize || listed.Jobs[0].ID != test.wantID {
				t.Fatalf("conversation jobs = %+v", listed.Jobs)
			}
		})
	}
	if _, err := registrar.tool.Handler(conversation(101, 7), fmt.Sprintf(`{"action":"get","id":%q}`, second.ID)); !errors.Is(err, scheduler.ErrJobNotFound) {
		t.Fatalf("cross-conversation get error = %v", err)
	}
	if _, err := registrar.tool.Handler(conversation(101, 7), fmt.Sprintf(`{"action":"update","id":%q,"message":"hijacked"}`, second.ID)); !errors.Is(err, scheduler.ErrJobNotFound) {
		t.Fatalf("cross-conversation update error = %v", err)
	}
	if saved, ok := jobs.Get(second.ID); !ok || saved.Message != "second" || saved.OriginChatID != 202 || saved.OriginThreadID != 8 {
		t.Fatalf("cross-conversation mutation changed job = %+v", saved)
	}
}

func formatInt64(value int64) string {
	return strconv.FormatInt(value, 10)
}

func TestCronConcurrentLimitEditPreservesReviewAndEdits(t *testing.T) {
	var seconds atomic.Int64
	seconds.Store(1000)
	var calls atomic.Int32
	entered := make(chan struct{})
	release := make(chan struct{})
	unblock := sync.OnceFunc(func() { close(release) })
	defer unblock()
	options := scheduler.Options{
		Path: filepath.Join(t.TempDir(), "jobs.json"),
		Now:  func() time.Time { return time.Unix(seconds.Load(), 0) },
		Executor: func(context.Context, string) error {
			if calls.Add(1) == 2 {
				close(entered)
				<-release
				return scheduler.ReviewRequiredError{Message: "reconcile the completed actions"}
			}
			return nil
		},
	}
	jobs, err := scheduler.New(options)
	if err != nil {
		t.Fatal(err)
	}
	job, err := jobs.Add(scheduler.Job{Kind: scheduler.ScheduleEvery, EverySeconds: 1, Message: "original", MaxRuns: 3})
	if err != nil {
		t.Fatal(err)
	}
	seconds.Add(1)
	if runs := jobs.RunDue(context.Background()); runs != 1 {
		t.Fatalf("first run = %d", runs)
	}
	seconds.Add(1)
	finished := make(chan int, 1)
	go func() { finished <- jobs.RunDue(context.Background()) }()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("second run did not enter executor")
	}

	// Use another scheduler instance to exercise the persisted transaction and
	// the same locking used by separate processes.
	editor, err := scheduler.New(options)
	if err != nil {
		t.Fatal(err)
	}
	invalid := fmt.Sprintf(`{"action":"update","id":%q,"max_runs":1,"message":"must not be saved"}`, job.ID)
	if _, err := executeCronTool(context.Background(), invalid, editor); err == nil || !strings.Contains(err.Error(), "is running") {
		t.Fatalf("limit without room for the claim = %v", err)
	}
	active, ok := editor.Get(job.ID)
	if !ok || active.Status != scheduler.StatusRunning || active.RunCount != 1 || active.MaxRuns != 3 || active.Message != "original" {
		t.Fatalf("rejected update changed the running job: %+v", active)
	}
	valid := fmt.Sprintf(`{"action":"update","id":%q,"max_runs":2,"message":"edited","every_seconds":7}`, job.ID)
	if _, err := executeCronTool(context.Background(), valid, editor); err != nil {
		t.Fatal(err)
	}
	for _, action := range []string{"disable", "enable"} {
		if _, err := executeCronTool(context.Background(), fmt.Sprintf(`{"action":%q,"id":%q}`, action, job.ID), editor); err != nil {
			t.Fatal(err)
		}
		active, ok = editor.Get(job.ID)
		if !ok || active.Status != scheduler.StatusRunning || active.Enabled != (action == "enable") {
			t.Fatalf("%s erased the running claim: %+v", action, active)
		}
	}
	if runs := editor.RunDue(context.Background()); runs != 0 {
		t.Fatalf("another scheduler claimed the active run: %d", runs)
	}
	unblock()
	select {
	case runs := <-finished:
		if runs != 1 {
			t.Fatalf("second run = %d", runs)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("second run did not finish")
	}
	jobs, err = scheduler.New(options)
	if err != nil {
		t.Fatal(err)
	}
	saved, ok := jobs.Get(job.ID)
	if !ok || saved.Enabled || !saved.ReviewRequired || saved.Status != scheduler.StatusFailed || saved.Error != "reconcile the completed actions" || saved.RunCount != 2 || saved.MaxRuns != 2 || saved.Message != "edited" || saved.EverySeconds != 7 || saved.NextRun != nil || saved.LastRun == nil {
		t.Fatalf("review or concurrent edits lost after restart: %+v", saved)
	}
	for _, limit := range []int{2, 3, 0} {
		args := fmt.Sprintf(`{"action":"update","id":%q,"max_runs":%d}`, job.ID, limit)
		if _, err := executeCronTool(context.Background(), args, jobs); err != nil {
			t.Fatal(err)
		}
		saved, ok = jobs.Get(job.ID)
		if !ok || saved.Enabled || !saved.ReviewRequired || saved.Error != "reconcile the completed actions" || saved.RunCount != 2 || saved.MaxRuns != limit {
			t.Fatalf("limit edit cleared review: %+v", saved)
		}
	}
	seconds.Add(100)
	if runs := jobs.RunDue(context.Background()); runs != 0 || calls.Load() != 2 {
		t.Fatalf("reviewed job replayed: runs=%d calls=%d", runs, calls.Load())
	}
}

func TestCronConcurrentLimitEditPreservesSuccessAndRetry(t *testing.T) {
	for _, retry := range []bool{false, true} {
		t.Run(fmt.Sprintf("retry=%v", retry), func(t *testing.T) {
			var seconds atomic.Int64
			seconds.Store(1000)
			var calls atomic.Int32
			entered, release := make(chan struct{}), make(chan struct{})
			unblock := sync.OnceFunc(func() { close(release) })
			defer unblock()
			options := scheduler.Options{
				Path: filepath.Join(t.TempDir(), "jobs.json"),
				Now:  func() time.Time { return time.Unix(seconds.Load(), 0) },
				Executor: func(context.Context, string) error {
					if calls.Add(1) == 2 {
						close(entered)
						<-release
						if retry {
							return scheduler.ErrRetry
						}
					}
					return nil
				},
			}
			jobs, err := scheduler.New(options)
			if err != nil {
				t.Fatal(err)
			}
			job, err := jobs.Add(scheduler.Job{Kind: scheduler.ScheduleEvery, EverySeconds: 1, Message: "original", MaxRuns: 3})
			if err != nil {
				t.Fatal(err)
			}
			seconds.Add(1)
			if runs := jobs.RunDue(context.Background()); runs != 1 {
				t.Fatalf("first run = %d", runs)
			}
			seconds.Add(1)
			finished := make(chan int, 1)
			go func() { finished <- jobs.RunDue(context.Background()) }()
			select {
			case <-entered:
			case <-time.After(5 * time.Second):
				t.Fatal("second run did not enter executor")
			}
			args := fmt.Sprintf(`{"action":"update","id":%q,"max_runs":2,"message":"edited","every_seconds":7}`, job.ID)
			if _, err := executeCronTool(context.Background(), args, jobs); err != nil {
				t.Fatal(err)
			}
			unblock()
			select {
			case runs := <-finished:
				if runs != 1 {
					t.Fatalf("second run = %d", runs)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("second run did not finish")
			}
			jobs, err = scheduler.New(options)
			if err != nil {
				t.Fatal(err)
			}
			saved, ok := jobs.Get(job.ID)
			if !ok || saved.ReviewRequired || saved.Message != "edited" || saved.EverySeconds != 7 || saved.MaxRuns != 2 {
				t.Fatalf("completion lost concurrent edits: %+v", saved)
			}
			if retry {
				if !saved.Enabled || saved.Status != scheduler.StatusScheduled || saved.RunCount != 1 || saved.NextRun == nil || !saved.NextRun.Equal(time.Unix(1009, 0)) {
					t.Fatalf("retry did not preserve the updated schedule: %+v", saved)
				}
				if runs := jobs.RunDue(context.Background()); runs != 0 {
					t.Fatalf("retry ran before the updated schedule: %d", runs)
				}
				seconds.Add(7)
				if runs := jobs.RunDue(context.Background()); runs != 1 || calls.Load() != 3 {
					t.Fatalf("updated retry: runs=%d calls=%d", runs, calls.Load())
				}
				saved, ok = jobs.Get(job.ID)
			}
			if !ok || saved.Enabled || saved.Status != scheduler.StatusCompleted || saved.RunCount != 2 || saved.NextRun != nil {
				t.Fatalf("run limit after completion: %+v", saved)
			}
		})
	}
}

func TestCronReviewRecoveryAtRunLimitRequiresExplicitEnable(t *testing.T) {
	for _, tc := range []struct {
		name  string
		job   scheduler.Job
		limit int
	}{
		{"one-time raised limit", scheduler.Job{Kind: scheduler.ScheduleAt, AtSeconds: 1, Message: "once", MaxRuns: 1}, 2},
		{"one-time unlimited", scheduler.Job{Kind: scheduler.ScheduleAt, AtSeconds: 1, Message: "once", MaxRuns: 1}, 0},
		{"recurring raised limit", scheduler.Job{Kind: scheduler.ScheduleEvery, EverySeconds: 1, Message: "repeat", MaxRuns: 1}, 2},
		{"recurring unlimited", scheduler.Job{Kind: scheduler.ScheduleEvery, EverySeconds: 1, Message: "repeat", MaxRuns: 1}, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			clock := time.Unix(1000, 0)
			calls := 0
			options := scheduler.Options{Path: filepath.Join(t.TempDir(), "jobs.json"), Now: func() time.Time { return clock }, Executor: func(context.Context, string) error {
				calls++
				if calls == 1 {
					return scheduler.ReviewRequiredError{Message: "reconcile first"}
				}
				return nil
			}}
			jobs, err := scheduler.New(options)
			if err != nil {
				t.Fatal(err)
			}
			job, err := jobs.Add(tc.job)
			if err != nil {
				t.Fatal(err)
			}
			clock = clock.Add(time.Second)
			if runs := jobs.RunDue(context.Background()); runs != 1 {
				t.Fatalf("review run = %d", runs)
			}
			enable := fmt.Sprintf(`{"action":"enable","id":%q}`, job.ID)
			if _, err := executeCronTool(context.Background(), enable, jobs); err == nil || !strings.Contains(err.Error(), "increase max_runs above run_count or set it to 0") {
				t.Fatalf("enable at limit = %v", err)
			}
			update := fmt.Sprintf(`{"action":"update","id":%q,"max_runs":%d}`, job.ID, tc.limit)
			if _, err := executeCronTool(context.Background(), update, jobs); err != nil {
				t.Fatal(err)
			}
			jobs, err = scheduler.New(options)
			if err != nil {
				t.Fatal(err)
			}
			clock = clock.Add(time.Minute)
			saved, ok := jobs.Get(job.ID)
			if !ok || saved.Enabled || !saved.ReviewRequired || saved.Error != "reconcile first" || saved.RunCount != 1 || saved.MaxRuns != tc.limit || jobs.RunDue(context.Background()) != 0 || calls != 1 {
				t.Fatalf("limit edit or restart cleared review: %+v calls=%d", saved, calls)
			}
			if _, err := executeCronTool(context.Background(), enable, jobs); err != nil {
				t.Fatal(err)
			}
			saved, ok = jobs.Get(job.ID)
			if !ok || !saved.Enabled || saved.ReviewRequired || saved.Error != "" || saved.NextRun == nil {
				t.Fatalf("explicit enable did not clear review: %+v", saved)
			}
			clock = clock.Add(time.Second)
			if runs := jobs.RunDue(context.Background()); runs != 1 || calls != 2 {
				t.Fatalf("explicit recovery: runs=%d calls=%d", runs, calls)
			}
		})
	}
}

func TestCronInterruptedClaimRemainsInReviewThroughLimitEdits(t *testing.T) {
	for _, restart := range []bool{false, true} {
		t.Run(fmt.Sprintf("restart=%v", restart), func(t *testing.T) {
			clock := time.Unix(1000, 0)
			calls := 0
			options := scheduler.Options{Path: filepath.Join(t.TempDir(), "jobs.json"), Now: func() time.Time { return clock }, Executor: func(context.Context, string) error {
				calls++
				return nil
			}}
			jobs, err := scheduler.New(options)
			if err != nil {
				t.Fatal(err)
			}
			store, err := scheduler.NewStore(options.Path)
			if err != nil {
				t.Fatal(err)
			}
			if err := store.Save([]scheduler.Job{{ID: "interrupted", Kind: scheduler.ScheduleEvery, EverySeconds: 1, Message: "repeat", Enabled: true, Status: scheduler.StatusRunning, RunCount: 1, MaxRuns: 2}}); err != nil {
				t.Fatal(err)
			}
			if restart {
				jobs, err = scheduler.New(options)
				if err != nil {
					t.Fatal(err)
				}
			} else if runs := jobs.RunDue(context.Background()); runs != 0 {
				t.Fatalf("interrupted claim replayed: %d", runs)
			}
			for _, args := range []string{
				`{"action":"disable","id":"interrupted"}`,
				`{"action":"update","id":"interrupted","max_runs":1}`,
				`{"action":"update","id":"interrupted","max_runs":2}`,
				`{"action":"update","id":"interrupted","every_seconds":7}`,
			} {
				if _, err := executeCronTool(context.Background(), args, jobs); err != nil {
					t.Fatal(err)
				}
			}
			jobs, err = scheduler.New(options)
			if err != nil {
				t.Fatal(err)
			}
			clock = clock.Add(time.Minute)
			saved, ok := jobs.Get("interrupted")
			if !ok || saved.Enabled || !saved.ReviewRequired || saved.Error != "previous run was interrupted" || saved.RunCount != 1 || saved.MaxRuns != 2 || saved.EverySeconds != 7 || jobs.RunDue(context.Background()) != 0 || calls != 0 {
				t.Fatalf("edits lost interrupted-run review: %+v calls=%d", saved, calls)
			}
			if _, err := executeCronTool(context.Background(), `{"action":"enable","id":"interrupted"}`, jobs); err != nil {
				t.Fatal(err)
			}
			clock = clock.Add(7 * time.Second)
			if runs := jobs.RunDue(context.Background()); runs != 1 || calls != 1 {
				t.Fatalf("explicitly enabled interrupted job: runs=%d calls=%d", runs, calls)
			}
		})
	}
}

func TestScheduledCronManagementAndMigrationGate(t *testing.T) {
	jobs, err := scheduler.New(scheduler.Options{Path: filepath.Join(t.TempDir(), "jobs.json")})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.WithValue(context.Background(), scheduledTurnKey{}, true)
	result, err := executeCronTool(ctx, `{"action":"add","every_seconds":60,"message":"ordinary"}`, jobs)
	if err != nil {
		t.Fatal(err)
	}
	var job scheduler.Job
	if err := json.Unmarshal([]byte(result), &job); err != nil {
		t.Fatal(err)
	}
	if _, err := executeCronTool(ctx, `{"action":"disable","id":"`+job.ID+`"}`, jobs); err != nil {
		t.Fatal(err)
	}
	ctx = WithScheduledMigrationStarted(ctx, func() bool { return false })
	if _, err := executeCronTool(ctx, `{"action":"add","every_seconds":60,"message":"recursive"}`, jobs); err == nil {
		t.Fatal("migration added cron before create")
	}
	ctx = WithScheduledMigrationStarted(ctx, func() bool { return true })
	if _, err := executeCronTool(ctx, `{"action":"add","every_seconds":60,"message":"recursive"}`, jobs); err == nil {
		t.Fatal("migration added cron after create")
	}
	if _, err := executeCronTool(ctx, `{"action":"enable","id":"`+job.ID+`"}`, jobs); err == nil {
		t.Fatal("migration rearmed cron")
	}
	if _, err := executeCronTool(ctx, `{"action":"list"}`, jobs); err != nil {
		t.Fatal(err)
	}
}

func TestCronSelfDisableIsScheduledOnlyBoundAndDurable(t *testing.T) {
	clock := time.Unix(1000, 0)
	var selfHandler ToolHandler
	var cronHandler ToolHandler
	var handlerForContext func(context.Context) (ToolHandler, bool)
	var firstID string
	var staleContext context.Context
	var executions int
	jobs, err := scheduler.New(scheduler.Options{
		Path: filepath.Join(t.TempDir(), "jobs.json"),
		Now:  func() time.Time { return clock },
		JobExecutor: func(ctx context.Context, job scheduler.Job) error {
			executions++
			if job.ID != firstID {
				return fmt.Errorf("unexpected job %q", job.ID)
			}
			scheduled := context.WithValue(ctx, scheduledTurnKey{}, true)
			scheduled = WithConversationType(scheduled, "private")
			scheduled = WithConversationID(scheduled, 77)
			scheduled = WithConversationThreadID(scheduled, 9)
			scheduled = WithScheduledCronJobID(scheduled, job.ID)
			if bound, ok := ScheduledCronJobID(scheduled); !ok || bound != job.ID {
				return errors.New("runtime job ID was not visible in the genuine scheduled context")
			}
			if handlerForContext == nil {
				return errors.New("self-disable handler lookup is unavailable")
			}
			if _, ok := handlerForContext(scheduled); !ok {
				return errors.New("self-disable tool was not exposed to scheduled turn")
			}

			forgedID := WithScheduledCronJobID(scheduled, "other-job")
			if _, ok := ScheduledCronJobID(forgedID); ok {
				return errors.New("forged runtime job ID was exposed")
			}
			if _, err := selfHandler(forgedID, `{}`); err == nil {
				return errors.New("forged runtime job ID was accepted")
			}
			wrongOrigin := WithConversationID(scheduled, 88)
			if _, err := selfHandler(wrongOrigin, `{}`); err == nil {
				return errors.New("wrong conversation owner was accepted")
			}
			if _, err := selfHandler(scheduled, `{"id":"other-job"}`); err == nil {
				return errors.New("model-supplied target ID was accepted")
			}

			if _, err := cronHandler(scheduled, `{"action":"add","every_seconds":3600,"message":"ordinary scheduled management"}`); err != nil {
				return fmt.Errorf("ordinary scheduled add: %w", err)
			}
			before := WithScheduledMigrationStarted(scheduled, func() bool { return false })
			if _, err := cronHandler(before, `{"action":"add","every_seconds":60,"message":"recursive"}`); err == nil {
				return errors.New("cron add was accepted before migration began")
			}

			started := WithScheduledMigrationStarted(scheduled, func() bool { return true })
			staleContext = started
			if _, err := cronHandler(started, `{"action":"add","every_seconds":60,"message":"must be rejected"}`); err == nil {
				return errors.New("ordinary cron mutation was accepted after migration began")
			}
			if _, err := selfHandler(started, `{}`); err != nil {
				return fmt.Errorf("self-disable after migration began: %w", err)
			}
			return nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	provider := NewCodexProvider(CredentialStore{})
	if err := RegisterCronTool(provider, jobs); err != nil {
		t.Fatal(err)
	}
	if err := RegisterCronSelfDisableTool(provider, jobs); err != nil {
		t.Fatal(err)
	}
	selfHandler = provider.tools.byName[cronSelfDisableToolName]
	cronHandler = provider.tools.byName[cronToolName]
	if selfHandler == nil || cronHandler == nil {
		t.Fatal("cron handlers were not registered")
	}
	handlerForContext = func(ctx context.Context) (ToolHandler, bool) {
		return provider.tools.handlerFor(cronSelfDisableToolName, ctx)
	}
	if _, ok := provider.tools.handlerFor(cronSelfDisableToolName, context.Background()); ok {
		t.Fatal("self-disable tool was exposed during a normal turn")
	}
	for _, definition := range provider.tools.definitions() {
		if definition.Name == cronSelfDisableToolName {
			t.Fatal("self-disable definition was exposed during a normal turn")
		}
	}
	if _, err := selfHandler(context.Background(), `{}`); err == nil {
		t.Fatal("normal-turn self-disable call unexpectedly succeeded")
	}

	first, err := jobs.Add(scheduler.Job{
		ID:             "self-job",
		Kind:           scheduler.ScheduleEvery,
		EverySeconds:   60,
		Message:        "self-disable",
		MaxRuns:        1,
		OriginChatID:   77,
		OriginChatType: "private",
		OriginThreadID: 9,
	})
	if err != nil {
		t.Fatal(err)
	}
	firstID = first.ID
	other, err := jobs.Add(scheduler.Job{
		ID:             "other-job",
		Kind:           scheduler.ScheduleEvery,
		EverySeconds:   3600,
		Message:        "must remain enabled",
		OriginChatID:   88,
		OriginChatType: "private",
		OriginThreadID: 9,
	})
	if err != nil {
		t.Fatal(err)
	}
	clock = clock.Add(60 * time.Second)
	if runs := jobs.RunDue(context.Background()); runs != 1 || executions != 1 {
		t.Fatalf("self-disable execution count = runs:%d executions:%d", runs, executions)
	}
	if _, err := selfHandler(staleContext, `{}`); err == nil {
		t.Fatal("stale completed execution context was accepted")
	}
	saved, ok := jobs.Get(first.ID)
	if !ok || saved.Enabled || saved.Status != scheduler.StatusCompleted || saved.RunCount != 1 || saved.NextRun != nil {
		t.Fatalf("self-disabled job = %+v", saved)
	}
	otherSaved, ok := jobs.Get(other.ID)
	if !ok || !otherSaved.Enabled || otherSaved.Status != scheduler.StatusScheduled {
		t.Fatalf("other job was affected = %+v", otherSaved)
	}
	restarted, err := scheduler.New(scheduler.Options{Path: jobs.Path(), Now: func() time.Time { return clock }})
	if err != nil {
		t.Fatal(err)
	}
	if runs := restarted.RunDue(context.Background()); runs != 0 {
		t.Fatalf("self-disabled job replayed after restart: %d", runs)
	}
	restartedSaved, ok := restarted.Get(first.ID)
	if !ok || restartedSaved.Enabled || restartedSaved.Status != scheduler.StatusCompleted || restartedSaved.RunCount != 1 {
		t.Fatalf("restarted self-disabled job = %+v", restartedSaved)
	}
}
