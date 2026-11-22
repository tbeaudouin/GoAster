package chat

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"goaster/internal/localenv"
)

func TestToolBudgetProductionQuotaAndInitialInstructions(t *testing.T) {
	const wantQuota = 1000
	if maxCodexToolCalls != wantQuota {
		t.Fatalf("production tool quota = %d, want %d", maxCodexToolCalls, wantQuota)
	}

	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		var req codexRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Error(err)
			return
		}
		if requests != 1 {
			t.Errorf("initial request count = %d", requests)
		}
		wantInstructions := fmt.Sprintf("You may request at most %d total tool calls per user turn", wantQuota)
		if !strings.Contains(req.Instructions, wantInstructions) {
			t.Errorf("initial model instructions omitted %q: %q", wantInstructions, req.Instructions)
		}
		fmt.Fprint(w, `{"output_text":"natural finish"}`)
	}))
	defer server.Close()

	p := NewCodexProvider(CredentialStore{})
	p.backendURL, p.httpClient = server.URL, server.Client()
	reply, _, err := p.requestReply(context.Background(), Credential{}, DefaultModel, nil, true)
	if err != nil || reply != "natural finish" || requests != 1 {
		t.Fatalf("reply=%q err=%v requests=%d", reply, err, requests)
	}
}

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
			if calls != maxCodexToolCalls+3 || outputs != maxCodexToolCalls+3 || skipped != 3 {
				t.Errorf("calls=%d outputs=%d skipped=%d", calls, outputs, skipped)
			}
			// A noncompliant backend cannot cause another action or request.
			writeCodexFunctionCall(t, w, "malicious", "shell_execute", `{}`)
			return
		}
		n := maxCodexToolCalls - 2
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
	if actions != maxCodexToolCalls*2 || requests != 6 || observed != maxCodexToolCalls*2 {
		t.Fatalf("actions=%d requests=%d", actions, requests)
	}
}

func TestToolBudgetBelowNewBoundaryFinishesNaturally(t *testing.T) {
	const toolCalls = 101
	requests, actions := 0, 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		var req codexRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Error(err)
			return
		}
		if strings.Contains(req.Instructions, "tool budget has been reached") {
			t.Error("below-boundary request advertised an exhausted budget")
		}
		if requests <= toolCalls {
			writeCodexFunctionCall(t, w, fmt.Sprint(requests), "shell_execute", `{}`)
			return
		}
		fmt.Fprint(w, `{"output_text":"natural finish"}`)
	}))
	defer server.Close()

	p := NewCodexProvider(CredentialStore{})
	p.backendURL, p.httpClient = server.URL, server.Client()
	p.tools.byName["shell_execute"] = func(context.Context, string) (string, error) {
		actions++
		return "done", nil
	}
	reply, _, err := p.requestReply(context.Background(), Credential{}, DefaultModel, nil, true)
	if err != nil || reply != "natural finish" || strings.Contains(reply, "budget reached") {
		t.Fatalf("reply=%q err=%v", reply, err)
	}
	if requests != toolCalls+1 || actions != toolCalls {
		t.Fatalf("requests=%d actions=%d", requests, actions)
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
		calls := make([]codexOutputItem, maxCodexToolCalls)
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
					fmt.Fprint(w, `{"error":{"message":"fixture-error-body"}}`)
					return
				}
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
				if tc.cancel && actions == maxCodexToolCalls {
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
			if err == nil || actions != maxCodexToolCalls || requests > 2 {
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
		if actions == 1 {
			return strings.Repeat("x", maxContextTokens*4), nil
		}
		return "", nil
	}
	_, _, err := p.requestReply(context.Background(), Credential{}, DefaultModel, nil, true)
	if err == nil || !strings.Contains(err.Error(), "insufficient room") || requests != 1 || actions != maxCodexToolCalls {
		t.Fatalf("err=%v requests=%d actions=%d", err, requests, actions)
	}
}

func TestToolBudgetPersistentSequentialFinish(t *testing.T) {
	requests, actions := 0, 0
	summary := fmt.Sprintf("Collected result-1 through result-%d.", maxCodexToolCalls)
	path := filepath.Join(t.TempDir(), "session.json")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		var req codexRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Error(err)
			return
		}
		if requests == 1 {
			data, _ := json.Marshal(req.Input)
			if !strings.Contains(string(data), "original persistent request") {
				t.Error("initial persistent request missing")
			}
		}
		if requests <= maxCodexToolCalls {
			writeCodexFunctionCall(t, w, fmt.Sprint(requests), "shell_execute", fmt.Sprintf(`{"n":%d}`, requests))
			return
		}
		if requests == maxCodexToolCalls+1 {
			data, _ := json.Marshal(req.Input)
			checkpoint, err := os.ReadFile(path)
			if !strings.Contains(string(data), "original persistent request") || err != nil || !strings.Contains(string(checkpoint), "original persistent request") {
				t.Error("current request missing from finish or in-flight checkpoint")
			}
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
			if calls != maxCodexToolCalls || outputs != maxCodexToolCalls {
				t.Errorf("calls=%d outputs=%d", calls, outputs)
			}
			fmt.Fprintf(w, `{"output_text":%q}`, summary)
			return
		}
		data, _ := json.Marshal(req.Input)
		if requests != maxCodexToolCalls+2 || !strings.Contains(string(data), summary) {
			t.Error("saved turn missing after restart")
		}
		fmt.Fprint(w, `{"output_text":"next answer"}`)
	}))
	defer server.Close()
	p := runtimeTestProvider(t, server.Client(), server.URL)
	p.tools.byName["shell_execute"] = func(context.Context, string) (string, error) {
		actions++
		return fmt.Sprintf("result-%d", actions), nil
	}
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
	if err != nil || !strings.Contains(answer, "budget reached") || !strings.Contains(answer, summary) {
		t.Fatalf("answer=%q err=%v", answer, err)
	}
	s, err = NewPersistentSessionAt(Config{Model: DefaultModel}, p, path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.Reply(ctx, "next request"); err != nil {
		t.Fatal(err)
	}
	if requests != maxCodexToolCalls+2 || actions != maxCodexToolCalls || observed != maxCodexToolCalls {
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
					calls := make([]codexOutputItem, maxCodexToolCalls)
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
			if err != nil || !strings.Contains(reply, "Unable to produce") || actions != maxCodexToolCalls || requests != 2 {
				t.Fatalf("reply=%q err=%v actions=%d requests=%d", reply, err, actions, requests)
			}
			for _, want := range []string{tc.reason, `model_detail="unrecognized"`, `backend_type_detail="absent"`, `backend_code_detail="absent"`, `request_id_detail="absent"`, `request_reference="chat-0123456789abcdef01234567"`, `phase="post-stream"`, `status="200"`, fmt.Sprintf(`tool_started_count="%d"`, maxCodexToolCalls), fmt.Sprintf(`tool_completed_count="%d"`, maxCodexToolCalls)} {
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

func TestToolBudgetEnvironmentConfiguration(t *testing.T) {
	t.Setenv(toolCallLimitEnvironment, "")
	for _, tc := range []struct {
		name, process string
		saved         map[string]string
		want          int
	}{
		{"default", "", nil, 1000}, {"blank", "  ", nil, 1000}, {"process", "7", nil, 7},
		{"saved precedence", "7", map[string]string{toolCallLimitEnvironment: "3"}, 3},
		{"saved blank", "7", map[string]string{toolCallLimitEnvironment: " "}, 1000},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv(toolCallLimitEnvironment, tc.process)
			got, err := effectiveToolCallLimit(WithShellEnvironment(context.Background(), tc.saved))
			if err != nil || got != tc.want {
				t.Fatalf("limit=%d err=%v", got, err)
			}
		})
	}
}

func TestToolBudgetSavedEnvironmentAndSnapshot(t *testing.T) {
	t.Setenv(toolCallLimitEnvironment, "9")
	path := filepath.Join(t.TempDir(), "env")
	store := localenv.NewStoreAt(path)
	if _, err := store.UpsertIfUnchanged(localenv.Digest(nil), map[string]string{toolCallLimitEnvironment: "3", "PRIVATE_OTHER": "private-other-secret"}); err != nil {
		t.Fatal(err)
	}
	reloaded := localenv.NewStoreAt(path)
	if err := reloaded.Load(); err != nil {
		t.Fatal(err)
	}
	requests, actions := 0, 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		var req codexRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Error(err)
			return
		}
		data, _ := json.Marshal(req)
		if strings.Contains(string(data), "private-other-secret") {
			t.Error("private setting leaked")
		}
		if requests == 1 {
			if !strings.Contains(req.Instructions, "at most 3 total") {
				t.Error("missing saved limit")
			}
			os.Setenv(toolCallLimitEnvironment, "2")
			calls := make([]codexOutputItem, 5)
			for i := range calls {
				calls[i] = codexOutputItem{Type: "function_call", CallID: fmt.Sprint(i), Name: "shell_execute", Arguments: json.RawMessage(`"{}"`)}
			}
			json.NewEncoder(w).Encode(codexResponse{Output: calls})
			return
		}
		if requests == 2 {
			if len(req.Tools) != 0 || !strings.Contains(req.Instructions, "3-call tool budget") {
				t.Error("finish limit changed")
			}
			pairs, skipped := 0, 0
			for _, item := range req.Input {
				if item.Type == "function_call_output" {
					pairs++
					if strings.Contains(item.Output, "tool_limit_reached") {
						skipped++
					}
				}
			}
			if pairs != 5 || skipped != 2 {
				t.Errorf("pairs=%d skipped=%d", pairs, skipped)
			}
		} else if !strings.Contains(req.Instructions, "at most 4 total") {
			t.Error("next turn did not observe saved change")
		}
		fmt.Fprint(w, `{"output_text":"done"}`)
	}))
	defer server.Close()
	p := runtimeTestProvider(t, server.Client(), server.URL)
	p.tools.byName["shell_execute"] = func(context.Context, string) (string, error) { actions++; return "done", nil }
	if _, _, err := p.requestReply(WithShellEnvironment(context.Background(), reloaded.Values()), Credential{}, DefaultModel, nil, true); err != nil {
		t.Fatal(err)
	}
	if _, err := reloaded.UpsertIfUnchanged(localenv.Digest(reloaded.Values()), map[string]string{toolCallLimitEnvironment: "4"}); err != nil {
		t.Fatal(err)
	}
	if err := reloaded.Load(); err != nil {
		t.Fatal(err)
	}
	if _, _, err := p.requestReply(WithShellEnvironment(context.Background(), reloaded.Values()), Credential{}, DefaultModel, nil, true); err != nil {
		t.Fatal(err)
	}
	if actions != 3 || requests != 3 {
		t.Fatalf("actions=%d requests=%d", actions, requests)
	}
}

func TestToolBudgetInvalidConfigurationIsSafe(t *testing.T) {
	t.Setenv(toolCallLimitEnvironment, "")
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { requests++; fmt.Fprint(w, `{"output_text":"tool-free"}`) }))
	defer server.Close()
	p := runtimeTestProvider(t, server.Client(), server.URL)
	for _, value := range []string{"0", "-1", "secret-value-INJECTED", "999999999999999999999999999999999"} {
		ctx := WithShellEnvironment(context.Background(), map[string]string{toolCallLimitEnvironment: value})
		_, _, err := p.requestReply(ctx, Credential{}, DefaultModel, nil, true)
		var failure localFailure
		if !errors.As(err, &failure) || failure.reason != "tool_config_invalid" {
			t.Fatalf("err=%v", err)
		}
		if value == "secret-value-INJECTED" && strings.Contains(err.Error(), value) {
			t.Fatal("configuration value leaked")
		}
		if value == "secret-value-INJECTED" && !errors.Is(err, strconv.ErrSyntax) {
			t.Fatal("parsing cause missing")
		}
		reply, _, freeErr := p.requestReply(ctx, Credential{}, DefaultModel, nil, false)
		if freeErr != nil || reply != "tool-free" {
			t.Fatalf("tool-free: %q %v", reply, freeErr)
		}
	}
	ctx := WithShellEnvironmentUnavailable(context.Background())
	_, _, err := p.requestReply(ctx, Credential{}, DefaultModel, nil, true)
	var failure localFailure
	if !errors.As(err, &failure) || failure.reason != "tool_config_unavailable" {
		t.Fatalf("unavailable: %v", err)
	}
	if _, _, err := p.requestReply(ctx, Credential{}, DefaultModel, nil, false); err != nil {
		t.Fatal(err)
	}
	if requests != 5 {
		t.Fatalf("unexpected provider requests: %d", requests)
	}
}

func TestToolBudgetProcessEnvironmentSnapshot(t *testing.T) {
	t.Setenv(toolCallLimitEnvironment, "3")
	requests, actions := 0, 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		var req codexRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Error(err)
			return
		}
		switch requests {
		case 1:
			if !strings.Contains(req.Instructions, "at most 3 total") {
				t.Error("initial limit")
			}
			os.Setenv(toolCallLimitEnvironment, "2")
			calls := make([]codexOutputItem, 4)
			for i := range calls {
				calls[i] = codexOutputItem{Type: "function_call", CallID: fmt.Sprint(i), Name: "shell_execute", Arguments: json.RawMessage(`"{}"`)}
			}
			json.NewEncoder(w).Encode(codexResponse{Output: calls})
		case 2:
			if !strings.Contains(req.Instructions, "3-call tool budget") || len(req.Tools) != 0 {
				t.Error("finish limit changed")
			}
			fmt.Fprint(w, `{"output_text":"done"}`)
		default:
			if !strings.Contains(req.Instructions, "at most 2 total") {
				t.Error("next limit not updated")
			}
			fmt.Fprint(w, `{"output_text":"done"}`)
		}
	}))
	defer server.Close()
	p := runtimeTestProvider(t, server.Client(), server.URL)
	p.tools.byName["shell_execute"] = func(context.Context, string) (string, error) { actions++; return "done", nil }
	for i := 0; i < 2; i++ {
		if _, _, err := p.requestReply(context.Background(), Credential{}, DefaultModel, nil, true); err != nil {
			t.Fatal(err)
		}
	}
	if actions != 3 || requests != 3 {
		t.Fatalf("actions=%d requests=%d", actions, requests)
	}
}

func TestToolBudgetInvalidSessionConfigurationMessage(t *testing.T) {
	t.Setenv(toolCallLimitEnvironment, "")
	p := runtimeTestProvider(t, http.DefaultClient, "http://127.0.0.1:1")
	session, err := NewPersistentSessionAt(Config{Model: DefaultModel}, p, filepath.Join(t.TempDir(), "session.json"))
	if err != nil {
		t.Fatal(err)
	}
	ctx := WithShellEnvironment(context.Background(), map[string]string{toolCallLimitEnvironment: "private-secret-value"})
	var reply string
	log := captureChatDiagnostics(t, func() { reply, err = session.Reply(ctx, "request") })
	for _, value := range []string{reply, ModelStreamMessageForError(err, ""), requestFailureDiagnostic(err), log} {
		if strings.Contains(value, "private-secret-value") {
			t.Fatalf("secret leaked: %s", value)
		}
	}
	if err == nil || !strings.Contains(ModelStreamMessageForError(err, ""), "GOASTER_TOOL_CALL_LIMIT must be a positive integer") || !strings.Contains(log, `local_stage="configuration"`) || !strings.Contains(log, `local_reason="tool_config_invalid"`) || strings.Count(log, "ERROR operation=") != 1 {
		t.Fatalf("err=%v log=%s", err, log)
	}
}
