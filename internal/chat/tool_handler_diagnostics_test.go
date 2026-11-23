package chat

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"goaster/internal/diagnostics"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestHandlerFailureRecoveryAndNoReplay(t *testing.T) {
	for _, name := range []string{"read_file", "list_dir", "shell_execute"} {
		t.Run(name, func(t *testing.T) {
			requests, actions := 0, 0
			var followup string
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests++
				var req codexRequest
				if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
					t.Error(err)
					return
				}
				if requests == 1 {
					args, _ := json.Marshal(`{"secret":"PRIVATE_ARGUMENT"}`)
					json.NewEncoder(w).Encode(codexResponse{Output: []codexOutputItem{{Type: "function_call", CallID: "call1", Name: name, Arguments: args}}})
					return
				}
				data, _ := json.Marshal(req.Input)
				followup = string(data)
				fmt.Fprint(w, `{"output_text":"I could not complete the tool."}`)
			}))
			defer server.Close()
			creds := CredentialStore{Path: filepath.Join(t.TempDir(), "credentials")}
			if err := creds.Save(Credential{AccessToken: "test", ExpiresAt: time.Now().Add(time.Hour)}); err != nil {
				t.Fatal(err)
			}
			provider := NewCodexProvider(creds)
			provider.backendURL = server.URL
			provider.httpClient = server.Client()
			cause := errors.New("PRIVATE_ERROR https://private.example/token")
			if name == "read_file" || name == "list_dir" {
				var data any
				cause = fmt.Errorf("PRIVATE_ERROR: %w", json.Unmarshal([]byte("{"), &data))
			}
			provider.tools.byName[name] = func(context.Context, string) (string, error) { actions++; return "PRIVATE_OUTPUT", cause }
			var reply string
			var err error
			log := captureChatDiagnostics(t, func() {
				reply, err = provider.Reply(WithRequestReference(context.Background(), "chat-0123456789abcdef"), DefaultModel, "hello")
			})
			if actions != 1 {
				t.Fatalf("actions=%d", actions)
			}
			if name == "read_file" || name == "list_dir" {
				if err != nil || requests != 2 || !strings.Contains(reply, "could not complete") || !strings.Contains(reply, "chat-0123456789abcdef") || !strings.Contains(reply, "self-debug") || !strings.Contains(followup, "tool_handler_failed") {
					t.Fatalf("reply=%q err=%v requests=%d followup=%s", reply, err, requests, followup)
				}
				for _, field := range []string{`configured_model="` + DefaultModel + `"`, `phase="post-stream"`, `status="200"`, `backend_type_detail="absent"`, `backend_code_detail="absent"`, `request_id_detail="absent"`} {
					if !strings.Contains(log, field) {
						t.Fatalf("missing %s in %s", field, log)
					}
				}
				if strings.Count(log, `ERROR operation=chat.tool_handler `) != 1 {
					t.Fatalf("expected one handler failure diagnostic: %s", log)
				}
				if !strings.Contains(followup, "invalid_arguments") {
					t.Fatal("typed reason missing from model output")
				}
				if strings.Contains(followup, "PRIVATE_ERROR") || strings.Contains(followup, "PRIVATE_OUTPUT") {
					t.Fatal("private handler error or output exposed")
				}
			} else {
				if err == nil || !errors.Is(err, cause) || requests != 1 {
					t.Fatalf("err=%v requests=%d", err, requests)
				}
				message := ModelStreamMessageForError(err, "chat-0123456789abcdef")
				if !strings.Contains(message, "A tool failed") || strings.Contains(message, "model request failed") || !strings.Contains(message, "self-debug") {
					t.Fatalf("message=%s", message)
				}
			}
			for _, secret := range []string{"PRIVATE_ARGUMENT", "PRIVATE_ERROR", "PRIVATE_OUTPUT", "private.example"} {
				if strings.Contains(log, secret) {
					t.Fatalf("log leaked %s: %s", secret, log)
				}
			}
			reason := "unrecognized"
			if name == "read_file" || name == "list_dir" {
				reason = "invalid_arguments"
			}
			for _, field := range []string{`local_stage="tool_handler"`, `tool_name="` + name + `"`, `tool_cause_detail="` + reason + `"`, `request_reference="chat-0123456789abcdef"`} {
				if !strings.Contains(log, field) {
					t.Fatalf("missing %s in %s", field, log)
				}
			}
		})
	}
}

func TestHandlerCancellationStops(t *testing.T) {
	// The cause remains discoverable even when wrapped by the safe handler error.
	err := toolHandlerError{cause: fmt.Errorf("private: %w", context.Canceled), tool: "read_file"}
	if !errors.Is(err, context.Canceled) || !handlerFailureMustStop(err) || strings.Contains(err.Error(), "private") {
		t.Fatal(err)
	}
}

func TestHandlerStorageFailureMustStop(t *testing.T) {
	err := fmt.Errorf("wrapped: %w", syscall.EIO)
	if !handlerFailureMustStop(err) {
		t.Fatal("storage error was recoverable")
	}
}

func TestToolCauseReason(t *testing.T) {
	var syntax error
	// Decode a real syntax error without exposing its contents.
	var data any
	syntax = json.Unmarshal([]byte("{"), &data)
	cases := []struct {
		err  error
		want string
	}{
		{syntax, "invalid_arguments"}, {fmt.Errorf("PRIVATE: %w", os.ErrNotExist), "file_not_found"},
		{os.ErrPermission, "permission_denied"}, {syscall.EIO, "storage_failure"},
		{context.DeadlineExceeded, "deadline_exceeded"}, {context.Canceled, "canceled"},
		{localFailure{reason: "skill_checkpoint"}, "local_failure"}, {errors.New("PRIVATE"), "unrecognized"},
	}
	for _, tc := range cases {
		if got := toolCauseReason(tc.err); got != tc.want {
			t.Errorf("got %q want %q", got, tc.want)
		}
	}
	if !handlerFailureMustStop(conversationSaveError{cause: errors.New("PRIVATE")}) {
		t.Fatal("checkpoint error must stop")
	}
}

func TestToolDiagnosticNamesAreSanitized(t *testing.T) {
	for _, name := range []string{"web_search", "web_fetch", "telegram_command_reference", "read_file\nPRIVATE"} {
		log := captureChatDiagnostics(t, func() { diagnostics.Error("chat.tool_handler", toolHandlerError{tool: name, cause: os.ErrPermission}) })
		want := name
		if strings.Contains(name, "PRIVATE") {
			want = "unrecognized"
		}
		if !strings.Contains(log, `tool_name="`+want+`"`) || !strings.Contains(log, `tool_cause_detail="permission_denied"`) || strings.Contains(log, "PRIVATE") {
			t.Fatalf("unsafe diagnostic: %s", log)
		}
	}
}

func TestHandlerRecoveryStopsBatchAndFinishesSafely(t *testing.T) {
	for _, finish := range []string{"text", "empty", "tools", "error", "checkpoint", "storage"} {
		t.Run(finish, func(t *testing.T) {
			requests, later := 0, 0
			var items []codexInputItem
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests++
				var req codexRequest
				if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
					t.Error(err)
					return
				}
				if requests == 1 {
					json.NewEncoder(w).Encode(codexResponse{Output: []codexOutputItem{
						{Type: "function_call", CallID: "first", Name: "read_file", Arguments: json.RawMessage(`"{}"`)},
						{Type: "function_call", CallID: "later", Name: "shell_execute", Arguments: json.RawMessage(`"{}"`)},
					}})
					return
				}
				items = req.Input
				switch finish {
				case "text":
					fmt.Fprint(w, `{"output_text":"Partial results."}`)
				case "empty":
					fmt.Fprint(w, `{"output_text":""}`)
				case "tools":
					fmt.Fprint(w, `{"output":[{"type":"function_call","call_id":"again","name":"shell_execute","arguments":"{}"}]}`)
				case "error":
					w.WriteHeader(http.StatusBadRequest)
				}
			}))
			defer server.Close()
			creds := CredentialStore{Path: filepath.Join(t.TempDir(), "credentials")}
			if err := creds.Save(Credential{AccessToken: "test", ExpiresAt: time.Now().Add(time.Hour)}); err != nil {
				t.Fatal(err)
			}
			p := NewCodexProvider(creds)
			p.backendURL = server.URL
			p.httpClient = server.Client()
			p.tools.byName["read_file"] = func(context.Context, string) (string, error) {
				switch finish {
				case "storage":
					return "", syscall.EIO
				case "checkpoint":
					return "", conversationSaveError{cause: errors.New("private")}
				}
				return "", errors.New("private")
			}
			p.tools.byName["shell_execute"] = func(context.Context, string) (string, error) { later++; return "", nil }
			var reply string
			var err error
			captureChatDiagnostics(t, func() {
				reply, err = p.Reply(WithRequestReference(context.Background(), "chat-0123456789abcdef"), DefaultModel, "hello")
			})
			if later != 0 {
				t.Fatal("later tool ran")
			}
			if finish == "storage" || finish == "checkpoint" {
				if err == nil || requests != 1 {
					t.Fatalf("err=%v requests=%d", err, requests)
				}
				return
			}
			if requests != 2 {
				t.Fatalf("requests=%d err=%v", requests, err)
			}
			seen := map[string]bool{}
			for _, item := range items {
				if item.Type == "function_call" {
					seen["call:"+item.CallID] = true
				}
				if item.Type == "function_call_output" {
					seen["output:"+item.CallID] = true
					if item.CallID == "later" && !strings.Contains(item.Output, "NOT EXECUTED") {
						t.Fatalf("output=%q", item.Output)
					}
				}
			}
			for _, key := range []string{"call:first", "output:first", "call:later", "output:later"} {
				if !seen[key] {
					t.Errorf("missing %s", key)
				}
			}
			if finish == "error" || finish == "empty" {
				if err == nil {
					t.Fatal("expected model error")
				}
				return
			}
			if err != nil || !strings.Contains(reply, "chat-0123456789abcdef") || !strings.Contains(reply, "self-debug") || strings.Count(reply, "A tool failed;") != 1 {
				t.Fatalf("reply=%q err=%v", reply, err)
			}
		})
	}
}
