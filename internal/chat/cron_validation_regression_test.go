package chat

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
	"strings"
	"syscall"
	"testing"
	"time"

	"goaster/internal/diagnostics"
	"goaster/internal/scheduler"
)

func TestCronValidationRegression(t *testing.T) {
	jobs, err := scheduler.New(scheduler.Options{Path: filepath.Join(t.TempDir(), "jobs.json"), Now: func() time.Time { return time.Unix(1000, 0) }})
	if err != nil {
		t.Fatal(err)
	}
	for _, input := range []string{
		`{"action":"add","at_seconds":60,"message":"a"}`,
		`{"action":"add","every_seconds":60,"message":"a"}`,
		`{"action":"add","cron_expr":"0 9 * * *","message":"a"}`,
		`{"action":"add","at_seconds":0,"every_seconds":60,"cron_expr":"","message":"a"}`,
		`{"action":"add","at_seconds":null,"every_seconds":60,"cron_expr":null,"message":"a"}`,
	} {
		if _, err := executeCronTool(context.Background(), input, jobs); err != nil {
			t.Errorf("valid input: %v", err)
		}
	}
	for _, input := range []string{
		`null`, `[]`, `{"action":"list","action":"add"}`, `{"action":"list","unknown":1}`, `{"action":"list"} {}`, `{"action":null}`, `{"action":"add","at_seconds":"0","every_seconds":60,"message":"a"}`,
		`{"action":"add","at_seconds":-1,"every_seconds":60,"message":"a"}`, `{"action":"add","at_seconds":1,"every_seconds":60,"message":"a"}`, `{"action":"add","message":"a"}`, `{"action":"add","every_seconds":60}`, `{"action":"update","message":"a"}`, `{"action":"add","at_seconds":0,"cron_expr":"","message":"a"}`, `{"action":"add","every_seconds":9223372036854775807,"message":"a"}`, `{"action":"add","cron_expr":"PRIVATE invalid","message":"a"}`,
	} {
		if _, err := executeCronTool(context.Background(), input, jobs); err == nil {
			t.Errorf("accepted invalid input %s", input)
		}
	}
}

func TestCronProviderCorrectionDiagnostic(t *testing.T) {
	jobs, err := scheduler.New(scheduler.Options{Path: filepath.Join(t.TempDir(), "jobs.json"), Now: func() time.Time { return time.Unix(1000, 0) }})
	if err != nil {
		t.Fatal(err)
	}
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		var req codexRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Error(err)
			return
		}
		switch requests {
		case 1, 2:
			args := `{"action":"add","at_seconds":1,"every_seconds":60,"message":"PRIVATE_MESSAGE"}`
			if requests == 2 {
				data, _ := json.Marshal(req.Input)
				if strings.Count(string(data), "NOT EXECUTED") != 2 || !strings.Contains(string(data), "schedule_count") {
					t.Error("missing safe correction guidance")
				}
				args = `{"action":"add","at_seconds":0,"every_seconds":60,"message":"PRIVATE_MESSAGE"}`
			}
			encoded, _ := json.Marshal(args)
			items := []codexOutputItem{{Type: "function_call", CallID: fmt.Sprint(requests), Name: cronToolName, Arguments: encoded}}
			if requests == 1 {
				items = append(items, codexOutputItem{Type: "function_call", CallID: "skipped", Name: cronToolName, Arguments: encoded})
			}
			json.NewEncoder(w).Encode(codexResponse{Output: items})
		default:
			fmt.Fprint(w, `{"output_text":"scheduled"}`)
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
	var reply string
	log := captureChatDiagnostics(t, func() {
		reply, err = provider.Reply(WithRequestReference(context.Background(), "chat-0123456789abcdef"), DefaultModel, "hello")
	})
	if err != nil || requests != 3 || reply != "scheduled" || len(jobs.List()) != 1 {
		t.Fatalf("requests=%d jobs=%d reply=%q err=%v", requests, len(jobs.List()), reply, err)
	}
	for _, field := range []string{`cron_action="add"`, `cron_stage="validate"`, `cron_reason="schedule_count"`, `request_reference="chat-0123456789abcdef"`} {
		if !strings.Contains(log, field) {
			t.Fatalf("missing diagnostic field %s: %s", field, log)
		}
	}
	if strings.Count(log, "ERROR operation=chat.tool_handler") != 1 || strings.Contains(log, "PRIVATE_MESSAGE") {
		t.Fatalf("unsafe diagnostic: %s", log)
	}
}

type failingCronScheduler struct {
	CronScheduler
	calls chan struct{}
	cause error
}

func (s failingCronScheduler) Add(scheduler.Job) (scheduler.Job, error) {
	s.calls <- struct{}{}
	return scheduler.Job{}, s.cause
}
func (s failingCronScheduler) Update(_ string, _ scheduler.Patch) (scheduler.Job, error) {
	s.calls <- struct{}{}
	return scheduler.Job{}, s.cause
}

func TestCronScheduleValidationPreservesCausePrivately(t *testing.T) {
	for _, action := range []string{"add", "update"} {
		for _, tc := range []struct {
			name, field, reason string
		}{
			{"expression", `"cron_expr":"PRIVATE_FIELD * * * *"`, "invalid_cron_expression"},
			{"duration", `"every_seconds":9223372036854775807`, "invalid_schedule"},
		} {
			t.Run(action+"/"+tc.name, func(t *testing.T) {
				jobs := failingCronScheduler{calls: make(chan struct{}, 1)}
				input := `{"action":"` + action + `","id":"id","message":"PRIVATE_MESSAGE",` + tc.field + `}`
				_, err := executeCronTool(context.Background(), input, jobs)
				argumentErr, ok := err.(cronArgumentError)
				cause := errors.Unwrap(err)
				if !ok || cause == nil || !errors.Is(err, cause) || argumentErr.action != action || argumentErr.reason != tc.reason || err.Error() != "Cron schedule is invalid." || len(jobs.calls) != 0 {
					t.Fatalf("validation error or mutation: %v, calls=%d", err, len(jobs.calls))
				}
				if tc.name == "expression" && !strings.Contains(strings.ToLower(cause.Error()), "private_field") {
					t.Fatal("underlying field error was not preserved")
				}
				log := captureChatDiagnostics(t, func() { diagnostics.Error("chat.tool_handler", toolHandlerError{tool: cronToolName, cause: err}) })
				if !strings.Contains(log, `cron_action="`+action+`"`) || !strings.Contains(log, `cron_reason="`+tc.reason+`"`) || strings.Contains(strings.ToLower(log), "private_") || strings.Contains(strings.ToLower(err.Error()), "private_") {
					t.Fatalf("unsafe or incomplete diagnostic: %s", log)
				}
			})
		}
	}
}

func TestCronMutationFailurePreservesCause(t *testing.T) {
	cause := fmt.Errorf("PRIVATE_STORAGE: %w", syscall.EIO)
	jobs := failingCronScheduler{calls: make(chan struct{}, 2), cause: cause}
	_, err := executeCronTool(context.Background(), `{"action":"add","every_seconds":60,"message":"PRIVATE_MESSAGE"}`, jobs)
	if !errors.Is(err, cause) || len(jobs.calls) != 1 || strings.Contains(err.Error(), "PRIVATE_STORAGE") {
		t.Fatalf("mutation error or calls: %v %d", err, len(jobs.calls))
	}
	log := captureChatDiagnostics(t, func() { diagnostics.Error("chat.tool_handler", toolHandlerError{tool: cronToolName, cause: err}) })
	if !strings.Contains(log, `cron_stage="scheduler_mutation"`) || !strings.Contains(log, `cron_reason="storage_failure"`) || strings.Contains(log, "PRIVATE_") {
		t.Fatalf("diagnostic: %s", log)
	}
}

func TestCronProviderMutationFailureStopsBatch(t *testing.T) {
	for _, tc := range []struct {
		name   string
		cause  error
		reason string
	}{
		{"storage", fmt.Errorf("PRIVATE_CAUSE: %w", syscall.EIO), "storage_failure"},
		{"private", errors.New("PRIVATE_CAUSE"), "unknown"},
		{"nested_argument", cronArgumentError{reason: "invalid_schedule", message: "PRIVATE_CAUSE"}, "unknown"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			jobs := failingCronScheduler{calls: make(chan struct{}, 2), cause: tc.cause}
			requests := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests++
				args, _ := json.Marshal(`{"action":"add","every_seconds":60,"message":"PRIVATE_MESSAGE"}`)
				json.NewEncoder(w).Encode(codexResponse{Output: []codexOutputItem{
					{Type: "function_call", CallID: "1", Name: cronToolName, Arguments: args},
					{Type: "function_call", CallID: "2", Name: cronToolName, Arguments: args},
				}})
			}))
			defer server.Close()
			creds := CredentialStore{Path: filepath.Join(t.TempDir(), "credentials")}
			if err := creds.Save(Credential{AccessToken: "PRIVATE_TOKEN", ExpiresAt: time.Now().Add(time.Hour)}); err != nil {
				t.Fatal(err)
			}
			provider := NewCodexProvider(creds)
			provider.backendURL = server.URL
			provider.httpClient = server.Client()
			if err := RegisterCronTool(provider, jobs); err != nil {
				t.Fatal(err)
			}
			var err error
			log := captureChatDiagnostics(t, func() {
				_, err = provider.Reply(WithRequestReference(context.Background(), "chat-0123456789abcdef"), DefaultModel, "PRIVATE_PROMPT")
			})
			if !errors.Is(err, tc.cause) || requests != 1 || len(jobs.calls) != 1 {
				t.Fatalf("cause=%v requests=%d calls=%d", errors.Is(err, tc.cause), requests, len(jobs.calls))
			}
			for _, field := range []string{`request_reference="chat-0123456789abcdef"`, `configured_model="` + DefaultModel + `"`, `status="200"`, `phase="post-stream"`, `cron_action="add"`, `cron_stage="scheduler_mutation"`, `cron_reason="` + tc.reason + `"`} {
				if !strings.Contains(log, field) {
					t.Errorf("missing %s: %s", field, log)
				}
			}
			if strings.Contains(log, "PRIVATE_") || strings.Contains(err.Error(), "PRIVATE_") {
				t.Fatal("private value exposed")
			}
		})
	}
}

func TestCronPureUpdateValidation(t *testing.T) {
	for _, input := range []string{
		`{"action":"update","id":"id","at_seconds":0}`,
		`{"action":"update","id":"id","every_seconds":-1}`,
		`{"action":"update","id":"id","cron_expr":"bad PRIVATE"}`,
		`{"action":"update","id":"id","at_seconds":1,"every_seconds":2}`,
		`{"action":"update","id":"id","message":" "}`,
		`{"action":"update","id":"id","max_runs":-1}`,
	} {
		_, err := executeCronTool(context.Background(), input, failingCronScheduler{})
		if _, ok := err.(cronArgumentError); !ok {
			t.Fatalf("expected precheck for %s: %v", input, err)
		}
	}
}

func TestCronParserPreservesSafeTypedCause(t *testing.T) {
	_, err := parseCronToolArguments(`{"action":"add","every_seconds":"PRIVATE_VALUE"}`)
	if fmt.Sprintf("%T", errors.Unwrap(err)) != "*json.UnmarshalTypeError" || strings.Contains(err.Error(), "PRIVATE_VALUE") {
		t.Fatalf("type cause not preserved safely: %v", err)
	}
	_, err = parseCronToolArguments(`{"action":`)
	if fmt.Sprintf("%T", errors.Unwrap(err)) != "*json.SyntaxError" && !errors.Is(err, io.ErrUnexpectedEOF) && !errors.Is(err, io.EOF) {
		t.Fatalf("parse cause missing: %v", err)
	}
}

func TestCronUpdateExplicitValues(t *testing.T) {
	jobs, err := scheduler.New(scheduler.Options{Path: filepath.Join(t.TempDir(), "jobs.json")})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = executeCronTool(context.Background(), `{"action":"add","every_seconds":60,"message":"a","max_runs":3,"delete_after_run":true}`, jobs); err != nil {
		t.Fatal(err)
	}
	id := jobs.List()[0].ID
	input, _ := json.Marshal(map[string]any{"action": "update", "id": id, "max_runs": 0, "delete_after_run": false, "at_seconds": 7})
	if _, err = executeCronTool(context.Background(), string(input), jobs); err != nil {
		t.Fatal(err)
	}
	job, _ := jobs.Get(id)
	if job.MaxRuns != 0 || job.DeleteAfterRun || job.Kind != scheduler.ScheduleAt || job.AtSeconds != 7 {
		t.Fatalf("explicit values lost: %+v", job)
	}
}

func TestCronSchemaRedaction(t *testing.T) {
	for _, input := range []string{`{"action":"list","PRIVATE_KEY":"PRIVATE_VALUE"}`, `{"action":"list","action":"PRIVATE_VALUE"}`, `{"action":"list","id":123,"message":"PRIVATE_VALUE"}`} {
		_, err := parseCronToolArguments(input)
		log := captureChatDiagnostics(t, func() { diagnostics.Error("chat.tool_handler", err) })
		if err == nil || strings.Contains(err.Error(), "PRIVATE_") || strings.Contains(log, "PRIVATE_") {
			t.Fatal("schema error leaked private content")
		}
	}
}

func TestCronMutationKnownReasons(t *testing.T) {
	for _, tc := range []struct {
		cause  error
		reason string
	}{{os.ErrPermission, "permission_denied"}, {os.ErrNotExist, "file_not_found"}} {
		log := captureChatDiagnostics(t, func() { diagnostics.Error("chat.tool_handler", cronMutationError{action: "add", cause: tc.cause}) })
		if !strings.Contains(log, `cron_reason="`+tc.reason+`"`) {
			t.Fatal(log)
		}
	}
}
