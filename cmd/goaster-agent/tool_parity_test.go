package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"goaster/internal/chat"
	"goaster/internal/migration"
	"goaster/internal/scheduler"
)

type parityTransport func(*http.Request) (*http.Response, error)

func (f parityTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestProductionScheduledToolParity(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	var snapshots []map[string]map[string]any
	dispatch := false
	oldTransport := http.DefaultTransport
	http.DefaultTransport = parityTransport(func(r *http.Request) (*http.Response, error) {
		var request map[string]any
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			return nil, err
		}
		tools := map[string]map[string]any{}
		for _, raw := range request["tools"].([]any) {
			v := raw.(map[string]any)
			tools[v["name"].(string)] = v
		}
		snapshots = append(snapshots, tools)
		body := `{"output_text":"ok"}`
		if dispatch {
			dispatch = false
			body = `{"output":[{"type":"function_call","call_id":"one","name":"parity_synthetic","arguments":"{}"}]}`
		}
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body)), Request: r}, nil
	})
	defer func() { http.DefaultTransport = oldTransport }()
	creds := chat.CredentialStore{Path: filepath.Join(t.TempDir(), "credentials")}
	if err := creds.Save(chat.Credential{AccessToken: "test", ExpiresAt: time.Now().Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}
	provider, manager, err := newAgentProvider(creds)
	if err != nil {
		t.Fatal(err)
	}
	defer manager.Close()
	now := time.Unix(2100000000, 0)
	sessionFor := newAgentSessionFactory(func(int64) agentSession { return chat.NewSession(chat.Config{Model: chat.DefaultModel}, provider) })
	jobs, err := newCronSchedulerWithMigrationAt(sessionFor, func(scheduler.Job, string) error { return nil }, nil, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	if err := registerAgentScheduledTools(provider, jobs, func(context.Context, migration.Request, int64) migration.OneShotResult {
		return migration.OneShotResult{}
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := provider.Reply(context.Background(), chat.DefaultModel, "hello"); err != nil {
		t.Fatal(err)
	}
	count := 0
	if err := provider.RegisterTool(chat.Tool{Name: "parity_synthetic", Description: "test", Parameters: map[string]any{"type": "object", "properties": map[string]any{}}, Handler: func(context.Context, string) (string, error) { count++; return "ok", nil }}); err != nil {
		t.Fatal(err)
	}
	dispatch = true
	if _, err := provider.Reply(context.Background(), chat.DefaultModel, "hello"); err != nil {
		t.Fatal(err)
	}
	job, err := jobs.Add(scheduler.Job{Kind: scheduler.ScheduleEvery, EverySeconds: 60, Message: "hello", OriginChatID: 42, OriginChatType: "private"})
	if err != nil {
		t.Fatal(err)
	}
	_ = job
	dispatch = true
	now = now.Add(time.Minute)
	if jobs.RunDue(context.Background()) != 1 || count != 2 {
		t.Fatalf("scheduled dispatch count=%d", count)
	}
	if _, ok := snapshots[0]["parity_synthetic"]; ok {
		t.Fatal("synthetic present before registration")
	}
	normal, scheduled := snapshots[1], snapshots[3]
	if _, ok := normal["cron_disable_self"]; ok {
		t.Fatal("self disable exposed normally")
	}
	if _, ok := scheduled["cron_disable_self"]; !ok {
		t.Fatal("self disable absent")
	}
	delete(scheduled, "cron_disable_self")
	if len(normal) != len(scheduled) {
		t.Fatalf("tool counts normal=%d scheduled=%d", len(normal), len(scheduled))
	}
	for name, n := range normal {
		s, ok := scheduled[name]
		if !ok {
			t.Fatalf("missing scheduled tool %s", name)
		}
		if name == "cron" {
			action := n["parameters"].(map[string]any)["properties"].(map[string]any)["action"].(map[string]any)
			var allowed []any
			found := false
			for _, v := range action["enum"].([]any) {
				if v == "add" {
					found = true
				} else {
					allowed = append(allowed, v)
				}
			}
			if !found {
				t.Fatal("normal cron add absent")
			}
			action["enum"] = allowed
		}
		if !reflect.DeepEqual(n, s) {
			t.Fatal(fmt.Sprintf("tool definition differs: %s", name))
		}
	}
}
