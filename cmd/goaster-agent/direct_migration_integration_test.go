package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"goaster/internal/chat"
	"goaster/internal/migration"
	"goaster/internal/scheduler"
)

func TestDirectMigrationOfflineInitialAndRestart(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	store := chat.CredentialStore{Path: filepath.Join(home, "auth.json")}
	if err := store.Save(chat.Credential{AccessToken: "offline-test", ExpiresAt: time.Now().Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}
	previous := http.DefaultTransport
	t.Cleanup(func() { http.DefaultTransport = previous })
	modelCalls := 0
	http.DefaultTransport = offlineMigrationTransport(func(r *http.Request) (*http.Response, error) {
		modelCalls++
		body := fmt.Sprintf(`{"output":[{"type":"function_call","call_id":"migration","name":"goaster_migration","arguments":%q}]}`, scheduledMigrationToolArguments)
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body)), Request: r}, nil
	})
	provider := chat.NewCodexProvider(store)
	now := time.Unix(2100000000, 0)
	attempts, notices := 0, 0
	create := func(_ context.Context, req migration.Request, owner int64) migration.OneShotResult {
		attempts++
		if owner != 42 || req.ServerName != "goaster-migration-0123456789abcdef" || req.ServerType != "cx22" || len(req.Locations) != 1 || req.Locations[0] != "fsn1" {
			t.Fatalf("request changed: %+v owner %d", req, owner)
		}
		if attempts == 3 {
			return migration.OneShotResult{Status: "stop", ServerName: req.ServerName}
		}
		return migration.OneShotResult{Status: "retry_later", Code: "no_capacity"}
	}
	factory := func(int64, int64) agentSession { t.Fatal("direct retry invoked model session"); return nil }
	enqueue := func(scheduler.Job, string) error { notices++; return nil }
	jobs, err := newCronSchedulerWithMigrationAt(factory, enqueue, create, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	if err = registerAgentScheduledTools(provider, jobs, create, func(int64) error { return nil }); err != nil {
		t.Fatal(err)
	}
	ctx := chat.WithConversationID(chat.WithConversationType(context.Background(), "private"), 42)
	session := chat.NewSession(chat.Config{Model: "offline-test"}, provider)
	reply, err := session.Reply(ctx, "Create the approved server")
	if err != nil || !strings.Contains(reply, "retry_later") {
		t.Fatalf("reply=%q err=%v", reply, err)
	}
	if attempts != 1 || modelCalls != 1 {
		t.Fatalf("attempts=%d model=%d", attempts, modelCalls)
	}
	jobs, err = newCronSchedulerWithMigrationAt(factory, enqueue, create, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	now = now.Add(30 * time.Minute)
	if jobs.RunDue(context.Background()) != 1 {
		t.Fatal("first retry missing")
	}
	now = now.Add(30 * time.Minute)
	if jobs.RunDue(context.Background()) != 1 {
		t.Fatal("second retry missing")
	}
	now = now.Add(time.Hour)
	if jobs.RunDue(context.Background()) != 0 || modelCalls != 1 || attempts != 3 || notices != 2 {
		t.Fatalf("attempts=%d model=%d notices=%d", attempts, modelCalls, notices)
	}
}

func TestDirectMigrationFailuresRequireReview(t *testing.T) {
	for _, tc := range []struct {
		name        string
		coordinator failingCronCoordinator
		result      migration.OneShotResult
		enqueue     func(scheduler.Job, string) error
		creates     int
	}{
		{name: "claim", coordinator: failingCronCoordinator{claimErr: fmt.Errorf("claim failed")}, creates: 0},
		{name: "disable", coordinator: failingCronCoordinator{disableErr: fmt.Errorf("disable failed")}, result: migration.OneShotResult{Status: "stop"}, creates: 1},
		{name: "queue", result: migration.OneShotResult{Status: "retry_later"}, enqueue: func(scheduler.Job, string) error { return fmt.Errorf("queue failed") }, creates: 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			err := executeDirectMigration(context.Background(), scheduler.Job{OriginChatID: 42}, tc.coordinator, tc.enqueue, func(context.Context, migration.Request, int64) migration.OneShotResult { calls++; return tc.result })
			if !errors.Is(err, scheduler.ErrReviewRequired) || calls != tc.creates {
				t.Fatalf("error=%v calls=%d", err, calls)
			}
		})
	}
}

func TestDirectMigrationCancellationAndLocalFailure(t *testing.T) {
	for _, cancelled := range []bool{false, true} {
		ctx := context.Background()
		if cancelled {
			var cancel context.CancelFunc
			ctx, cancel = context.WithCancel(ctx)
			cancel()
		}
		creates, notices := 0, 0
		err := executeDirectMigration(ctx, scheduler.Job{OriginChatID: 42}, failingCronCoordinator{}, func(scheduler.Job, string) error { notices++; return nil }, func(context.Context, migration.Request, int64) migration.OneShotResult {
			creates++
			return migration.OneShotResult{Status: "retry_later", LocalFailure: true}
		})
		if !errors.Is(err, scheduler.ErrReviewRequired) || notices != 0 || (cancelled && creates != 0) || (!cancelled && creates != 1) {
			t.Fatalf("cancelled=%v creates=%d notices=%d err=%v", cancelled, creates, notices, err)
		}
	}
}

func TestDirectMigrationPartialHandoffDoesNotReplay(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	now := time.Unix(2100000000, 0)
	attempts, handoffs, notices := 0, 0, 0
	create := func(context.Context, migration.Request, int64) migration.OneShotResult {
		attempts++
		if attempts == 1 {
			handoffs++
			return migration.OneShotResult{Status: "retry_later", CreationState: "created", Handoff: "failed"}
		}
		return migration.OneShotResult{Status: "stop"}
	}
	jobs, err := newCronSchedulerWithMigrationAt(func(int64, int64) agentSession { t.Fatal("unexpected model"); return nil }, func(scheduler.Job, string) error { notices++; return nil }, create, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	creator := jobs.(interface {
		CreateOnce(migration.Request, int64) (scheduler.Job, error)
	})
	_, err = creator.CreateOnce(migration.Request{ServerType: "cx22", ServerName: "goaster-migration-0123456789abcdef"}, 42)
	if err != nil {
		t.Fatal(err)
	}
	now = now.Add(30 * time.Minute)
	jobs.RunDue(context.Background())
	now = now.Add(30 * time.Minute)
	jobs.RunDue(context.Background())
	now = now.Add(30 * time.Minute)
	jobs.RunDue(context.Background())
	if attempts != 2 || handoffs != 1 || notices != 2 {
		t.Fatalf("attempts=%d handoffs=%d notices=%d", attempts, handoffs, notices)
	}
}
