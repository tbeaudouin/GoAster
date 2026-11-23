package chat

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"goaster/internal/command"
	"goaster/internal/scheduler"
)

func strictCronInput(action string) map[string]any {
	return map[string]any{"action": action, "id": nil, "at_seconds": nil, "every_seconds": nil, "cron_expr": nil, "message": nil, "max_runs": nil, "delete_after_run": nil}
}

// Check the exact vocabulary emitted by this schema, including its constraints.
func checkCronWireInput(t *testing.T, schema map[string]any, input map[string]any, scheduled bool) {
	t.Helper()
	if schema["type"] != "object" || schema["additionalProperties"] != false {
		t.Fatal("invalid object schema", schema)
	}
	props := schema["properties"].(map[string]any)
	required := schema["required"].([]any)
	if len(props) != 8 || len(required) != len(props) || len(input) != len(props) {
		t.Fatal("incomplete strict shape")
	}
	seen := map[string]bool{}
	for _, v := range required {
		seen[v.(string)] = true
	}
	for name, raw := range props {
		if !seen[name] {
			t.Fatalf("%s not required", name)
		}
		p := raw.(map[string]any)
		if name == "at_seconds" || name == "every_seconds" {
			if p["minimum"] != float64(1) {
				t.Fatal("schedule minimum", name, p)
			}
		}
		if name == "max_runs" && p["minimum"] != float64(0) {
			t.Fatal("max_runs minimum", p)
		}
		value, exists := input[name]
		if !exists {
			t.Fatalf("missing %s", name)
		}
		if name == "action" {
			if p["type"] != "string" {
				t.Fatal("action nullable")
			}
			found, add := false, false
			for _, a := range p["enum"].([]any) {
				if a == value {
					found = true
				}
				if a == "add" {
					add = true
				}
			}
			if !found || add == scheduled {
				t.Fatal("incorrect action enum", p)
			}
			continue
		}
		types := p["type"].([]any)
		expected := map[string]string{"id": "string", "at_seconds": "integer", "every_seconds": "integer", "cron_expr": "string", "message": "string", "max_runs": "integer", "delete_after_run": "boolean"}
		if len(types) != 2 || types[0] != expected[name] || types[1] != "null" {
			t.Fatalf("%s not nullable: %v", name, types)
		}
		if value == nil {
			continue
		}
		switch types[0] {
		case "string":
			if _, ok := value.(string); !ok {
				t.Fatal(name, value)
			}
		case "integer":
			n, ok := value.(float64)
			if !ok || n != float64(int64(n)) {
				t.Fatal(name, value)
			}
			if min, ok := p["minimum"].(float64); ok && n < min {
				t.Fatal(name, value)
			}
		case "boolean":
			if _, ok := value.(bool); !ok {
				t.Fatal(name, value)
			}
		default:
			t.Fatal("unexpected type", types)
		}
	}
}

func TestCronWireSchemaAndRecurringCall(t *testing.T) {
	for _, scheduled := range []bool{false, true} {
		t.Run(fmt.Sprint(scheduled), func(t *testing.T) {
			clock := time.Unix(1000, 0)
			var scheduledReply func(context.Context, scheduler.Job) error
			jobs, err := scheduler.New(scheduler.Options{Path: filepath.Join(t.TempDir(), "jobs.json"), Now: func() time.Time { return clock }, JobExecutor: func(ctx context.Context, job scheduler.Job) error { return scheduledReply(ctx, job) }})
			if err != nil {
				t.Fatal(err)
			}
			calls := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				var request map[string]any
				if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
					t.Error(err)
					return
				}
				var schema map[string]any
				for _, raw := range request["tools"].([]any) {
					tool := raw.(map[string]any)
					if tool["name"] == cronToolName {
						schema = tool["parameters"].(map[string]any)
					}
				}
				if schema == nil {
					t.Error("cron absent from wire")
					return
				}
				input := strictCronInput("add")
				input["every_seconds"] = float64(1800)
				input["message"] = "reminder"
				if scheduled {
					input = strictCronInput("list")
				}
				checkCronWireInput(t, schema, input, scheduled)
				if calls == 1 {
					args, _ := json.Marshal(input)
					encoded, _ := json.Marshal(string(args))
					json.NewEncoder(w).Encode(codexResponse{Output: []codexOutputItem{{Type: "function_call", CallID: "one", Name: cronToolName, Arguments: encoded}}})
				} else {
					fmt.Fprint(w, `{"output_text":"confirmed"}`)
				}
			}))
			defer server.Close()
			creds := CredentialStore{Path: filepath.Join(t.TempDir(), "credentials")}
			if err := creds.Save(Credential{AccessToken: "test", ExpiresAt: time.Now().Add(time.Hour)}); err != nil {
				t.Fatal(err)
			}
			provider := NewCodexProvider(creds)
			provider.backendURL = server.URL
			provider.httpClient = server.Client()
			if err := RegisterCronTool(provider, jobs); err != nil {
				t.Fatal(err)
			}
			invoke := func(ctx context.Context) error {
				reply, err := provider.Reply(ctx, DefaultModel, "hello")
				if err != nil || reply != "confirmed" || calls != 2 {
					return fmt.Errorf("reply=%q calls=%d err=%v", reply, calls, err)
				}
				return nil
			}
			if scheduled {
				scheduledReply = func(ctx context.Context, job scheduler.Job) error {
					ctx = context.WithValue(ctx, scheduledTurnKey{}, true)
					return invoke(WithScheduledCronJobID(ctx, job.ID))
				}
				if _, err := jobs.Add(scheduler.Job{Kind: scheduler.ScheduleEvery, EverySeconds: 60, Message: "synthetic"}); err != nil {
					t.Fatal(err)
				}
				clock = clock.Add(time.Minute)
				if jobs.RunDue(context.Background()) != 1 || calls != 2 {
					t.Fatalf("scheduled calls=%d", calls)
				}
				if len(jobs.List()) != 1 {
					t.Fatal("scheduled call added a job")
				}
			} else {
				if err := invoke(context.Background()); err != nil {
					t.Fatal(err)
				}
				if len(jobs.List()) != 1 {
					t.Fatalf("jobs=%v", jobs.List())
				}
			}
		})
	}
}

func TestCronStrictInputsAndNullPatch(t *testing.T) {
	jobs, err := scheduler.New(scheduler.Options{Path: filepath.Join(t.TempDir(), "jobs.json")})
	if err != nil {
		t.Fatal(err)
	}
	run := func(input map[string]any) error {
		b, _ := json.Marshal(input)
		_, err := executeCronTool(context.Background(), string(b), jobs)
		return err
	}
	for _, field := range []string{"at_seconds", "every_seconds", "cron_expr"} {
		a := strictCronInput("add")
		a["message"] = "message"
		a["max_runs"] = 3
		a["delete_after_run"] = true
		if field == "cron_expr" {
			a[field] = "0 9 * * *"
		} else {
			a[field] = 60
		}
		if err := run(a); err != nil {
			t.Fatalf("%s: %v", field, err)
		}
	}
	id := jobs.List()[1].ID
	for _, action := range []string{"list", "get", "update", "disable", "enable", "remove"} {
		a := strictCronInput(action)
		if action != "list" {
			a["id"] = id
		}
		if action == "update" {
			a["message"] = "changed"
		}
		if err := run(a); err != nil {
			t.Fatalf("%s: %v", action, err)
		}
	}
	id = jobs.List()[0].ID
	before, _ := jobs.Get(id)
	a := strictCronInput("update")
	a["id"] = id
	if err := run(a); err == nil {
		t.Fatal("empty patch accepted")
	}
	after, _ := jobs.Get(id)
	if !reflect.DeepEqual(before, after) {
		t.Fatal("null patch mutated job")
	}
	a["message"] = "changed"
	if err := run(a); err != nil {
		t.Fatal(err)
	}
	after, _ = jobs.Get(id)
	before.Message = "changed"
	if !reflect.DeepEqual(before, after) {
		t.Fatalf("null fields changed job: before=%+v after=%+v", before, after)
	}
	a["message"] = nil
	a["max_runs"] = 0
	a["delete_after_run"] = false
	if err := run(a); err != nil {
		t.Fatal(err)
	}
	after, _ = jobs.Get(id)
	before.MaxRuns = 0
	before.DeleteAfterRun = false
	if !reflect.DeepEqual(before, after) {
		t.Fatalf("explicit fields changed other values: before=%+v after=%+v", before, after)
	}
}

func TestCronInvalidInputsDoNotMutate(t *testing.T) {
	jobs, err := scheduler.New(scheduler.Options{Path: filepath.Join(t.TempDir(), "jobs.json")})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := executeCronTool(context.Background(), `{"action":"add","every_seconds":60,"message":"private","max_runs":3}`, jobs); err != nil {
		t.Fatal(err)
	}
	before := jobs.List()
	for _, input := range []string{
		`{"action":null}`, `{"action":"add","every_seconds":60,"message":null}`, `{"action":"update","id":null,"message":"x"}`,
		`{"action":"add","every_seconds":60,"at_seconds":1,"message":"x"}`, `{"action":"add","every_seconds":-1,"message":"x"}`,
		`{"action":"add","every_seconds":"60","message":"x"}`, `{"action":"list","id":null,"id":null}`,
		`{"action":"list","unknown":null}`, `{"action":"list"} {}`, `{"action":"add","every_seconds":9223372036854775807,"message":"x"}`,
	} {
		if _, err := executeCronTool(context.Background(), input, jobs); err == nil {
			t.Errorf("accepted %s", input)
		}
		if !reflect.DeepEqual(before, jobs.List()) {
			t.Fatalf("mutated on %s", input)
		}
	}
}

// Exercise the production registry and wire encoding with a live scheduler owner.
func TestScheduledToolRegistryParity(t *testing.T) {
	clock := time.Unix(1000, 0)
	var scheduledReply func(context.Context, scheduler.Job) error
	jobs, err := scheduler.New(scheduler.Options{Path: filepath.Join(t.TempDir(), "jobs.json"), Now: func() time.Time { return clock }, JobExecutor: func(ctx context.Context, job scheduler.Job) error { return scheduledReply(ctx, job) }})
	if err != nil {
		t.Fatal(err)
	}
	var captures []map[string]map[string]any
	dispatchCall := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request map[string]any
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Error(err)
			return
		}
		snapshot := map[string]map[string]any{}
		for _, raw := range request["tools"].([]any) {
			tool := raw.(map[string]any)
			snapshot[tool["name"].(string)] = tool
		}
		captures = append(captures, snapshot)
		if dispatchCall {
			dispatchCall = false
			fmt.Fprint(w, `{"output":[{"type":"function_call","call_id":"parity","name":"parity_new_tool","arguments":"{}"}]}`)
		} else {
			fmt.Fprint(w, `{"output_text":"ok"}`)
		}
	}))
	defer server.Close()
	creds := CredentialStore{Path: filepath.Join(t.TempDir(), "credentials")}
	if err := creds.Save(Credential{AccessToken: "test", ExpiresAt: time.Now().Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}
	provider := NewCodexProvider(creds)
	provider.backendURL = server.URL
	provider.httpClient = server.Client()
	for _, err := range []error{
		RegisterCronTool(provider, jobs), RegisterCronSelfDisableTool(provider, jobs),
		RegisterBackgroundCommandTool(provider, command.BackgroundCommandManager{}),
		RegisterSendFileTool(provider, func(context.Context, int64, int64, string, []byte) error { return nil }),
		RegisterMigrationTool(provider, MigrationToolOperations{}),
	} {
		if err != nil {
			t.Fatal(err)
		}
	}
	invoke := func(ctx context.Context) {
		t.Helper()
		if _, err := provider.Reply(ctx, DefaultModel, "hello"); err != nil {
			t.Fatal(err)
		}
	}
	invoke(context.Background())
	dispatched := 0
	if err := provider.RegisterTool(Tool{Name: "parity_new_tool", Description: "test", Parameters: map[string]any{"type": "object", "properties": map[string]any{}}, Handler: func(context.Context, string) (string, error) { dispatched++; return "ok", nil }}); err != nil {
		t.Fatal(err)
	}
	invoke(context.Background())
	scheduledReply = func(ctx context.Context, job scheduler.Job) error {
		ctx = WithScheduledCronJobID(ctx, job.ID)
		session := NewSession(Config{Model: DefaultModel}, provider)
		if _, err := session.ReplyScheduled(ctx, "hello"); err != nil {
			t.Fatal(err)
		}
		ctx = context.WithValue(ctx, scheduledTurnKey{}, true)
		normal := provider.tools.definitionsFor(context.Background())
		scheduled := provider.tools.definitionsFor(ctx)
		for _, def := range normal {
			n, ok := provider.tools.handlerFor(def.Name, context.Background())
			if !ok {
				t.Fatalf("normal handler missing %s", def.Name)
			}
			s, ok := provider.tools.handlerFor(def.Name, ctx)
			if !ok || reflect.ValueOf(n).Pointer() != reflect.ValueOf(s).Pointer() {
				t.Fatalf("handler differs %s", def.Name)
			}
		}
		if len(scheduled) != len(normal)+1 {
			t.Fatalf("registry sizes normal=%d scheduled=%d", len(normal), len(scheduled))
		}
		for _, c := range []context.Context{context.Background(), ctx} {
			dispatchCall = true
			invoke(c)
		}
		return nil
	}
	if _, err := jobs.Add(scheduler.Job{Kind: scheduler.ScheduleEvery, EverySeconds: 60, Message: "test"}); err != nil {
		t.Fatal(err)
	}
	clock = clock.Add(time.Minute)
	if jobs.RunDue(context.Background()) != 1 {
		t.Fatal("scheduled job did not run")
	}
	if dispatched != 2 {
		t.Fatalf("dispatches=%d", dispatched)
	}
	if _, ok := captures[0]["parity_new_tool"]; ok {
		t.Fatal("new tool in initial snapshot")
	}
	normal, scheduled := captures[1], captures[2]
	if _, ok := normal[cronSelfDisableToolName]; ok {
		t.Fatal("self-disable exposed normally")
	}
	if _, ok := scheduled[cronSelfDisableToolName]; !ok {
		t.Fatal("self-disable missing")
	}
	delete(scheduled, cronSelfDisableToolName)
	if len(normal) != len(scheduled) {
		t.Fatalf("wire tool counts: %d %d", len(normal), len(scheduled))
	}
	for name, n := range normal {
		s, ok := scheduled[name]
		if !ok {
			t.Fatalf("scheduled missing %s", name)
		}
		if name == cronToolName {
			action := n["parameters"].(map[string]any)["properties"].(map[string]any)["action"].(map[string]any)
			var filtered []any
			for _, v := range action["enum"].([]any) {
				if v != "add" {
					filtered = append(filtered, v)
				}
			}
			action["enum"] = filtered
		}
		if !reflect.DeepEqual(n, s) {
			t.Fatalf("wire definition differs for %s: normal=%v scheduled=%v", name, n, s)
		}
	}
}
