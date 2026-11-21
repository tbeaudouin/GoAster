package chat

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
)

func TestToolBudgetPrefixFinishAndReset(t *testing.T) {
	requests, actions, observed := 0, 0, 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req codexRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Error(err)
			return
		}
		requests++
		round := (requests - 1) % 3
		if round == 2 {
			if len(req.Tools) != 0 || !strings.Contains(req.Instructions, "partial report") {
				t.Error("finish must disable tools and retain guidance")
			}
			calls, outputs, skipped := 0, 0, 0
			seen := map[string]string{}
			for _, item := range req.Input {
				if item.Type == "function_call" {
					calls++
					seen[item.CallID] = item.Name
				}
				if item.Type == "function_call_output" {
					outputs++
					if seen[item.CallID] == "" {
						t.Errorf("unpaired output %q", item.CallID)
					}
					if seen[item.CallID] == "shell_execute" && item.Output != "done" {
						t.Errorf("changed output %q", item.Output)
					}
					if strings.Contains(item.Output, `"error":"tool_limit_reached"`) && strings.Contains(item.Output, "NOT EXECUTED") {
						skipped++
					}
				}
			}
			if calls != 103 || outputs != 103 || skipped != 3 {
				t.Errorf("calls=%d outputs=%d skipped=%d", calls, outputs, skipped)
			}
			// A noncompliant backend cannot cause another action or request.
			writeCodexFunctionCall(t, w, "malicious", "shell_execute", `{}`)
			return
		}
		n := 98
		if round == 1 {
			n = 5
		}
		calls := make([]codexOutputItem, n)
		for i := range calls {
			name := "shell_execute"
			if round == 1 && i >= 2 {
				name = "web_fetch"
			}
			calls[i] = codexOutputItem{Type: "function_call", CallID: fmt.Sprintf("%d-%d", round, i), Name: name, Arguments: json.RawMessage(`"{}"`)}
		}
		json.NewEncoder(w).Encode(codexResponse{Output: calls})
	}))
	defer server.Close()
	p := NewCodexProvider(CredentialStore{})
	p.backendURL, p.httpClient = server.URL, server.Client()
	p.tools.byName["shell_execute"] = func(context.Context, string) (string, error) { actions++; return "done", nil }
	p.tools.byName["web_fetch"] = func(context.Context, string) (string, error) { t.Error("over-budget call executed"); return "", nil }
	ctx := WithToolCallObserver(context.Background(), func(name, args string) {
		observed++
		if name != "shell_execute" || args != "{}" {
			t.Errorf("unexpected observer %s %s", name, args)
		}
	})
	for turn := 0; turn < 2; turn++ {
		reply, _, err := p.requestReply(ctx, Credential{}, DefaultModel, nil, true)
		if err != nil || !strings.Contains(reply, "budget reached") || !strings.Contains(reply, "incomplete") {
			t.Fatalf("reply=%q err=%v", reply, err)
		}
	}
	if actions != 200 || requests != 6 || observed != 200 {
		t.Fatalf("actions=%d requests=%d", actions, requests)
	}
}

func TestToolBudgetSuppressedCallsCount(t *testing.T) {
	setupSkillFixture(t)
	requests, actions := 0, 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		var req codexRequest
		json.NewDecoder(r.Body).Decode(&req)
		if requests == 2 {
			if len(req.Tools) != 0 {
				t.Error("tools enabled")
			}
			fmt.Fprint(w, `{"output_text":"partial"}`)
			return
		}
		calls := make([]codexOutputItem, 100)
		for i := range calls {
			calls[i] = codexOutputItem{Type: "function_call", CallID: fmt.Sprint(i), Name: "shell_execute", Arguments: json.RawMessage(`"{}"`)}
		}
		calls[0].Name = "load_skill"
		calls[0].Arguments = json.RawMessage(`"{\"name\":\"fixture-skill\"}"`)
		json.NewEncoder(w).Encode(codexResponse{Output: calls})
	}))
	defer server.Close()
	p := NewCodexProvider(CredentialStore{})
	p.backendURL, p.httpClient = server.URL, server.Client()
	p.tools.byName["shell_execute"] = func(context.Context, string) (string, error) { actions++; return "done", nil }
	var reply string
	var err error
	log := captureChatDiagnostics(t, func() {
		reply, _, err = p.requestReply(context.Background(), Credential{}, DefaultModel, nil, true)
	})
	if err != nil || actions != 0 || requests != 2 || !strings.Contains(reply, "budget reached") {
		t.Fatalf("reply=%q err=%v actions=%d requests=%d", reply, err, actions, requests)
	}
	if strings.Contains(log, "ERROR operation=") {
		t.Fatalf("normal finish logged an error: %s", log)
	}
}

func TestToolBudgetFinishFailureDoesNotReplay(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status int
		cancel bool
	}{{"http", 400, false}, {"unauthorized", 401, false}, {"cancel", 0, true}} {
		t.Run(tc.name, func(t *testing.T) {
			requests, actions := 0, 0
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests++
				if requests > 1 {
					w.WriteHeader(tc.status)
					fmt.Fprint(w, `{"error":{"message":"fixture-secret-token"}}`)
					return
				}
				calls := make([]codexOutputItem, 100)
				for i := range calls {
					calls[i] = codexOutputItem{Type: "function_call", CallID: fmt.Sprint(i), Name: "shell_execute", Arguments: json.RawMessage(`"{}"`)}
				}
				json.NewEncoder(w).Encode(codexResponse{Output: calls})
			}))
			defer server.Close()
			p := NewCodexProvider(CredentialStore{})
			p.backendURL, p.httpClient = server.URL, server.Client()
			p.tools.byName["shell_execute"] = func(context.Context, string) (string, error) {
				actions++
				if tc.cancel && actions == 100 {
					cancel()
				}
				return "done", nil
			}
			var status int
			var err error
			log := captureChatDiagnostics(t, func() {
				_, status, err = p.requestReply(ctx, Credential{AccessToken: "fixture-secret-token"}, DefaultModel, nil, true)
			})
			if strings.Contains(log, "fixture-secret-token") || len(log) > 4096 || (!tc.cancel && strings.Count(log, "ERROR operation=") != 1) || (tc.cancel && strings.Count(log, "ERROR operation=") > 1) {
				t.Fatalf("unsafe or duplicate diagnostic: %s", log)
			}
			if err == nil || actions != 100 || requests > 2 {
				t.Fatalf("err=%v actions=%d requests=%d", err, actions, requests)
			}
			if !tc.cancel && (status != tc.status || requests != 2) {
				t.Fatalf("status=%d requests=%d err=%v", status, requests, err)
			}
			if tc.cancel && !errors.Is(err, context.Canceled) {
				t.Fatalf("cancellation lost: %v", err)
			}
			if tc.status == 401 && PostToolAuthenticationMessage(err) == "" {
				t.Fatalf("post-tool authentication lost: %v", err)
			}
		})
	}
}

func TestToolBudgetOversizedFinishStopsBeforeNetwork(t *testing.T) {
	requests, actions := 0, 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		calls := make([]codexOutputItem, maxCodexToolCalls)
		for i := range calls {
			calls[i] = codexOutputItem{Type: "function_call", CallID: fmt.Sprint(i), Name: "shell_execute", Arguments: json.RawMessage(`"{}"`)}
		}
		json.NewEncoder(w).Encode(codexResponse{Output: calls})
	}))
	defer server.Close()
	p := NewCodexProvider(CredentialStore{})
	p.backendURL, p.httpClient = server.URL, server.Client()
	p.tools.byName["shell_execute"] = func(context.Context, string) (string, error) {
		actions++
		return strings.Repeat("x", maxContextTokens), nil
	}
	_, _, err := p.requestReply(context.Background(), Credential{}, DefaultModel, nil, true)
	if err == nil || !strings.Contains(err.Error(), "insufficient room") || requests != 1 || actions != maxCodexToolCalls {
		t.Fatalf("err=%v requests=%d actions=%d", err, requests, actions)
	}
}

func TestToolBudgetPersistentSequentialFinish(t *testing.T) {
	requests, actions := 0, 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		var req codexRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Error(err)
			return
		}
		if requests <= maxCodexToolCalls {
			writeCodexFunctionCall(t, w, fmt.Sprint(requests), "shell_execute", fmt.Sprintf(`{"n":%d}`, requests))
			return
		}
		if requests == maxCodexToolCalls+1 {
			if len(req.Tools) != 0 || !strings.Contains(req.Instructions, "partial report") {
				t.Error("finish tools or instructions")
			}
			calls, outputs := 0, 0
			for _, item := range req.Input {
				if item.Type == "function_call" {
					calls++
					if item.CallID != fmt.Sprint(calls) {
						t.Errorf("call id %q", item.CallID)
					}
				}
				if item.Type == "function_call_output" {
					outputs++
					if item.CallID != fmt.Sprint(outputs) || item.Output != fmt.Sprintf("result-%d", outputs) {
						t.Errorf("output %d: %+v", outputs, item)
					}
				}
			}
			if calls != 100 || outputs != 100 {
				t.Errorf("calls=%d outputs=%d", calls, outputs)
			}
			fmt.Fprint(w, `{"output_text":"Collected result-1 through result-100."}`)
			return
		}
		data, _ := json.Marshal(req.Input)
		if !strings.Contains(string(data), "original persistent request") || !strings.Contains(string(data), "Collected result-1 through result-100.") {
			t.Error("saved turn missing")
		}
		fmt.Fprint(w, `{"output_text":"next answer"}`)
	}))
	defer server.Close()
	p := runtimeTestProvider(t, server.Client(), server.URL)
	p.tools.byName["shell_execute"] = func(context.Context, string) (string, error) {
		actions++
		return fmt.Sprintf("result-%d", actions), nil
	}
	path := filepath.Join(t.TempDir(), "session.json")
	s, err := NewPersistentSessionAt(Config{Model: DefaultModel}, p, path)
	if err != nil {
		t.Fatal(err)
	}
	observed := 0
	ctx := WithToolCallObserver(context.Background(), func(name, args string) {
		observed++
		if name != "shell_execute" || !strings.Contains(args, fmt.Sprintf(`"n":%d`, observed)) {
			t.Errorf("observer %d: %s %s", observed, name, args)
		}
	})
	answer, err := s.Reply(ctx, "original persistent request")
	if err != nil || !strings.Contains(answer, "budget reached") || !strings.Contains(answer, "Collected result-1 through result-100.") {
		t.Fatalf("answer=%q err=%v", answer, err)
	}
	s, err = NewPersistentSessionAt(Config{Model: DefaultModel}, p, path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.Reply(ctx, "next request"); err != nil {
		t.Fatal(err)
	}
	if requests != 102 || actions != 100 || observed != 100 {
		t.Fatalf("requests=%d actions=%d observed=%d", requests, actions, observed)
	}
}

func TestToolBudgetUnexpectedFinishDiagnostic(t *testing.T) {
	for _, tc := range []struct{ name, response, reason string }{
		{"tools", `{"output":[{"type":"function_call","call_id":"secret-call","name":"shell_execute","arguments":"private-finish-arguments"}]}`, "finish_tools"},
		{"whitespace", `{"output_text":"   "}`, "finish_empty"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			requests, actions := 0, 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests++
				if requests == 1 {
					calls := make([]codexOutputItem, 100)
					for i := range calls {
						calls[i] = codexOutputItem{Type: "function_call", CallID: fmt.Sprint(i), Name: "shell_execute", Arguments: json.RawMessage(`"private-tool-arguments"`)}
					}
					json.NewEncoder(w).Encode(codexResponse{Output: calls})
					return
				}
				fmt.Fprint(w, tc.response)
			}))
			defer server.Close()
			p := NewCodexProvider(CredentialStore{})
			p.backendURL, p.httpClient = server.URL, server.Client()
			p.tools.byName["shell_execute"] = func(context.Context, string) (string, error) { actions++; return "private-tool-result", nil }
			var reply string
			var err error
			log := captureChatDiagnostics(t, func() {
				ctx := WithRequestReference(context.Background(), "chat-0123456789abcdef01234567")
				reply, _, err = p.requestReply(ctx, Credential{AccessToken: "fixture-secret-token"}, "unknown-private-model", nil, true)
			})
			if err != nil || !strings.Contains(reply, "Unable to produce") || actions != 100 || requests != 2 {
				t.Fatalf("reply=%q err=%v actions=%d requests=%d", reply, err, actions, requests)
			}
			for _, want := range []string{tc.reason, `model_detail="unrecognized"`, `backend_type_detail="absent"`, `backend_code_detail="absent"`, `request_id_detail="absent"`, `request_reference="chat-0123456789abcdef01234567"`, `phase="post-stream"`, `status="200"`, `tool_started_count="100"`, `tool_completed_count="100"`} {
				if !strings.Contains(log, want) {
					t.Errorf("diagnostic missing %s: %s", want, log)
				}
			}
			for _, secret := range []string{"fixture-secret-token", "secret-call", "private-finish-arguments", "private-tool-arguments", "private-tool-result", "unknown-private-model"} {
				if strings.Contains(log, secret) || strings.Contains(reply, secret) {
					t.Errorf("private content %q in diagnostic or reply", secret)
				}
			}
			if strings.Count(log, "ERROR operation=") != 1 || len(log) > 4096 {
				t.Fatalf("diagnostic: %s", log)
			}
		})
	}
}
