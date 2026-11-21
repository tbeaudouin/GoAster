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
	"strings"
	"testing"
	"time"
)

type inflightTestProvider struct{ fail bool }

func (p inflightTestProvider) Reply(ctx context.Context, model, message string) (string, error) {
	observer, ok := ctx.Value(toolEvidenceObserverKey{}).(toolEvidenceObserver)
	if !ok {
		return "", errors.New("missing evidence observer")
	}
	if err := observer("shell_execute", "", false, ""); err != nil {
		return "", err
	}
	if p.fail {
		return "", errors.New("handler failed")
	}
	if err := observer("shell_execute", "", true, "PRIVATE_TOKEN /secret/path"); err != nil {
		return "", err
	}
	return "finished", nil
}

func TestInflightEvidencePersistsAndOmitsToolContent(t *testing.T) {
	for _, fail := range []bool{true, false} {
		t.Run(map[bool]string{true: "interrupted", false: "completed"}[fail], func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "session.json")
			session, err := NewPersistentSessionAt(Config{Model: "test"}, inflightTestProvider{fail: fail}, path)
			if err != nil {
				t.Fatal(err)
			}
			_, err = session.Reply(context.Background(), "user request")
			if (err != nil) != fail {
				t.Fatalf("reply error: %v", err)
			}
			data, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			text := string(data)
			for _, secret := range []string{"PRIVATE_TOKEN", "/secret/path"} {
				if strings.Contains(text, secret) {
					t.Fatalf("persisted secret: %s", secret)
				}
			}
			want := "completed; handler returned output"
			if fail {
				want = "started; outcome uncertain"
			}
			if !strings.Contains(text, "user request") || !strings.Contains(text, want) {
				t.Fatalf("missing evidence: %s", text)
			}
			if !fail && strings.Contains(text, "in progress or interrupted") {
				t.Fatalf("stale marker: %s", text)
			}
			restarted, err := NewPersistentSessionAt(Config{Model: "test"}, inflightTestProvider{}, path)
			if err != nil || len(restarted.history) == 0 {
				t.Fatalf("restart: %v", err)
			}
		})
	}
}

func TestToolHandlerErrorPreservesCauseWithoutExposingIt(t *testing.T) {
	cause := errors.New("PRIVATE_TOKEN")
	err := toolHandlerError{cause: cause}
	if !errors.Is(err, cause) || strings.Contains(err.Error(), "PRIVATE_TOKEN") {
		t.Fatalf("unsafe or lost cause: %v", err)
	}
}

func TestInflightCodexLoopRecoveryAndBatchFailure(t *testing.T) {
	for _, batch := range []bool{false, true} {
		t.Run(fmt.Sprintf("batch=%t", batch), func(t *testing.T) {
			requests, actions := 0, 0
			var resumed string
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests++
				var req codexRequest
				if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
					t.Error(err)
					return
				}
				if requests == 1 {
					calls := []codexOutputItem{}
					count := 1
					if batch {
						count = 3
					}
					for i := 0; i < count; i++ {
						args, _ := json.Marshal(`{"command":"git status"}`)
						calls = append(calls, codexOutputItem{Type: "function_call", CallID: fmt.Sprint(i), Name: "shell_execute", Arguments: args})
					}
					json.NewEncoder(w).Encode(codexResponse{Output: calls})
					return
				}
				if !batch && requests == 2 {
					http.Error(w, "failure", http.StatusBadRequest)
					return
				}
				data, _ := json.Marshal(req.Input)
				resumed = string(data)
				fmt.Fprint(w, `{"output_text":"follow-up"}`)
			}))
			defer server.Close()
			creds := CredentialStore{Path: filepath.Join(t.TempDir(), "credentials")}
			if err := creds.Save(Credential{AccessToken: "test", ExpiresAt: time.Now().Add(time.Hour)}); err != nil {
				t.Fatal(err)
			}
			provider := NewCodexProvider(creds)
			provider.backendURL = server.URL
			provider.httpClient = server.Client()
			provider.tools.byName["shell_execute"] = func(context.Context, string) (string, error) {
				actions++
				if batch && actions == 2 {
					return "", errors.New("handler failed")
				}
				return "stdout:\nOn branch main\nnothing to commit, working tree clean\nstderr:\n\nexit_code: 0", nil
			}
			path := filepath.Join(t.TempDir(), "session.json")
			session, err := NewPersistentSessionAt(Config{Model: DefaultModel}, provider, path)
			if err != nil {
				t.Fatal(err)
			}
			if _, err = session.Reply(context.Background(), "first turn"); err == nil {
				t.Fatal("expected failure")
			}
			wantActions, wantRequests := 1, 2
			if batch {
				wantActions, wantRequests = 2, 1
			}
			if actions != wantActions || requests != wantRequests {
				t.Fatalf("actions=%d requests=%d", actions, requests)
			}
			loaded, err := NewSessionStoreAt(path).Load()
			if err != nil {
				t.Fatal(err)
			}
			text := fmt.Sprint(loaded.Messages)
			if !strings.Contains(text, "shell_execute completed") || !strings.Contains(text, "Turn interrupted") || (batch && !strings.Contains(text, "shell_execute started; outcome uncertain")) {
				t.Fatalf("saved history: %s", text)
			}
			if _, err = session.Reply(context.Background(), "same process follow-up"); err != nil {
				t.Fatal(err)
			}
			if actions != wantActions || !strings.Contains(resumed, "first turn") || !strings.Contains(resumed, "git clean") || strings.Contains(resumed, "TOKEN=private") {
				t.Fatalf("same-process context or replay: actions=%d input=%s", actions, resumed)
			}
			restarted, err := NewPersistentSessionAt(Config{Model: DefaultModel}, provider, path)
			if err != nil {
				t.Fatal(err)
			}
			if _, err = restarted.Reply(context.Background(), "follow-up question"); err != nil {
				t.Fatal(err)
			}
			if actions != wantActions || !strings.Contains(resumed, "first turn") || !strings.Contains(resumed, "shell_execute completed") || !strings.Contains(resumed, "git clean") || (batch && !strings.Contains(resumed, "outcome uncertain")) {
				t.Fatalf("replay or missing context: actions=%d input=%s", actions, resumed)
			}
		})
	}
}

func TestInflightCodexSaveFailureGates(t *testing.T) {
	for _, failAt := range []int{1, 2, 3} {
		t.Run(fmt.Sprintf("save=%d", failAt), func(t *testing.T) {
			requests, actions, saves := 0, 0, 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests++
				calls := []codexOutputItem{}
				for i := 0; i < 2; i++ {
					args, _ := json.Marshal(`{}`)
					calls = append(calls, codexOutputItem{Type: "function_call", CallID: fmt.Sprint(i), Name: "shell_execute", Arguments: args})
				}
				json.NewEncoder(w).Encode(codexResponse{Output: calls})
			}))
			defer server.Close()
			creds := CredentialStore{Path: filepath.Join(t.TempDir(), "credentials")}
			if err := creds.Save(Credential{AccessToken: "test", ExpiresAt: time.Now().Add(time.Hour)}); err != nil {
				t.Fatal(err)
			}
			provider := NewCodexProvider(creds)
			provider.backendURL = server.URL
			provider.httpClient = server.Client()
			provider.tools.byName["shell_execute"] = func(context.Context, string) (string, error) { actions++; return "done", nil }
			path := filepath.Join(t.TempDir(), "session.json")
			session, err := NewPersistentSessionAt(Config{Model: DefaultModel}, provider, path)
			if err != nil {
				t.Fatal(err)
			}
			cause := errors.New("save gate")
			session.store.syncDirectory = func(string) error {
				saves++
				if saves == failAt {
					return cause
				}
				return nil
			}
			if _, err = session.Reply(context.Background(), "gate turn"); !errors.Is(err, cause) {
				t.Fatalf("error=%v", err)
			}
			wantRequests, wantActions := 1, 0
			if failAt == 1 {
				wantRequests = 0
			}
			if failAt == 3 {
				wantActions = 1
			}
			if requests != wantRequests || actions != wantActions || saves != failAt {
				t.Fatalf("requests=%d actions=%d saves=%d", requests, actions, saves)
			}
			live := fmt.Sprint(session.history)
			loaded, err := NewSessionStoreAt(path).Load()
			if err != nil {
				t.Fatal(err)
			}
			disk := fmt.Sprint(loaded.Messages)
			// Directory sync fails after replacement: disk can contain the attempted checkpoint,
			// while the session retains only the last acknowledged checkpoint.
			if !strings.Contains(disk, "gate turn") || (failAt == 1 && strings.Contains(live, "gate turn")) || (failAt > 1 && !strings.Contains(live, "gate turn")) || (failAt == 2 && (strings.Contains(live, "shell_execute") || !strings.Contains(disk, "outcome uncertain"))) || (failAt == 3 && (!strings.Contains(live, "outcome uncertain") || !strings.Contains(disk, "shell_execute completed"))) {
				t.Fatalf("live=%s disk=%s", live, disk)
			}
		})
	}
}

func TestInflightManyCodexNotesKeepCurrentRequest(t *testing.T) {
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
			calls := make([]codexOutputItem, 130)
			for i := range calls {
				args, _ := json.Marshal(`{}`)
				calls[i] = codexOutputItem{Type: "function_call", CallID: fmt.Sprint(i), Name: "shell_execute", Arguments: args}
			}
			json.NewEncoder(w).Encode(codexResponse{Output: calls})
			return
		}
		data, _ := json.Marshal(req.Input)
		followup = string(data)
		fmt.Fprint(w, `{"output_text":"done"}`)
	}))
	defer server.Close()
	creds := CredentialStore{Path: filepath.Join(t.TempDir(), "credentials")}
	if err := creds.Save(Credential{AccessToken: "test", ExpiresAt: time.Now().Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}
	provider := NewCodexProvider(creds)
	provider.backendURL, provider.httpClient = server.URL, server.Client()
	provider.tools.byName["shell_execute"] = func(ctx context.Context, _ string) (string, error) {
		actions++
		if actions == 129 {
			if err := ctx.Value(skillSessionKey{}).(skillSessionContext).remember("recent guidance stays"); err != nil {
				return "", err
			}
		}
		if actions == 130 {
			return "", errors.New("stopped")
		}
		return "done", nil
	}
	path := filepath.Join(t.TempDir(), "session.json")
	session, err := NewPersistentSessionAt(Config{Model: DefaultModel}, provider, path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = session.Reply(context.Background(), "original many-notes request"); err == nil {
		t.Fatal("expected handler failure")
	}
	if actions != 130 || requests != 1 {
		t.Fatalf("actions=%d requests=%d", actions, requests)
	}
	if _, err = session.Reply(context.Background(), "next request"); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"recent guidance stays", "Earlier conversation messages", "outcome uncertain"} {
		if !strings.Contains(followup, want) {
			t.Fatalf("missing %q in follow-up", want)
		}
	}
	if actions != 130 {
		t.Fatalf("replayed tools: %d", actions)
	}
}

func TestInflightBoundSkipsOversizedInteriorWithoutReplacingMessages(t *testing.T) {
	messages := []ChatMessage{{Role: "user", Content: "old request"}, {Role: "guidance", Content: strings.Repeat("x", 1000)}, {Role: "user", Content: "current request"}, {Role: "assistant", Content: "started note"}, {Role: "guidance", Content: "small guidance"}, {Role: "assistant", Content: "completion note"}}
	bounded, trimmed, err := boundInflightMessages("", messages, 100, 20)
	if err != nil || !trimmed {
		t.Fatalf("bound: %v trimmed=%v", err, trimmed)
	}
	text := fmt.Sprint(bounded)
	for _, want := range []string{"current request", "started note", "small guidance", "completion note", "Earlier conversation messages"} {
		if !strings.Contains(text, want) {
			t.Fatalf("missing %q: %s", want, text)
		}
	}
	messages = append(messages[:4], append([]ChatMessage{{Role: "guidance", Content: strings.Repeat("x", 1000)}}, messages[4:]...)...)
	if _, _, err := boundInflightMessages("", messages, 100, 20); err == nil || !strings.Contains(err.Error(), "guidance") {
		t.Fatal("expected insufficient evidence budget")
	}
}

type budgetTestProvider struct {
	summaryTestProvider
	overhead int
}

func (p *budgetTestProvider) skillContextOverhead(context.Context) (int, error) {
	return p.overhead, nil
}

func TestInteractiveCurrentRequestPreflightAndFallback(t *testing.T) {
	for _, input := range []string{strings.Repeat("x", maxSessionMessageBytes+1), "invalid\xff"} {
		path := filepath.Join(t.TempDir(), "session.json")
		provider := &summaryTestProvider{}
		session, err := NewPersistentSessionAt(Config{Model: "test"}, provider, path)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = session.Reply(context.Background(), input); err == nil {
			t.Fatal("expected rejection")
		}
		if _, err = os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("unexpected disk state: %v", err)
		}
		_, histories := provider.calls()
		if len(histories) != 0 || len(session.history) != 0 {
			t.Fatal("rejected request reached provider or history")
		}
	}
	provider := &summaryTestProvider{summarize: func() (string, error) { return "", errors.New("summary unavailable") }}
	session := NewSession(Config{Model: "test"}, provider)
	session.history = numberedHistory(maxSessionMessages)
	request := strings.Repeat("z", maxSessionMessageBytes)
	if _, err := session.Reply(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	_, histories := provider.calls()
	if !containsMessage(histories[0], "user", request) {
		t.Fatal("fallback changed current request")
	}
}

func TestInteractiveNoticeBudgetRejectsBeforeReply(t *testing.T) {
	request := strings.Repeat("x", 400)
	// Initial checkpoint fits, but the separately reserved request notice does not.
	overhead := maxContextTokens - estimateMessageTokens(ChatMessage{Role: "user", Content: request}) - estimateMessageTokens(ChatMessage{Role: "assistant", Content: "[" + contextTrimNotice + "]"})
	provider := &budgetTestProvider{overhead: overhead}
	session := NewSession(Config{Model: "test"}, provider)
	session.migrationDiagnostics = []MigrationDiagnostic{{CreationState: "unknown", Code: "response_unavailable"}}
	if _, err := session.Reply(context.Background(), request); err == nil {
		t.Fatal("expected notice budget rejection")
	}
	_, histories := provider.calls()
	if len(histories) != 0 {
		t.Fatal("provider called despite insufficient budget")
	}
}

func TestScheduledSummaryRetryDoesNotRetainAttempt(t *testing.T) {
	path := filepath.Join(t.TempDir(), "session.json")
	original := numberedHistory(maxSessionMessages - 1)
	if err := NewSessionStoreAt(path).Save(original); err != nil {
		t.Fatal(err)
	}
	provider := &summaryTestProvider{summarize: func() (string, error) { return "", context.DeadlineExceeded }}
	session, err := NewPersistentSessionAt(Config{Model: "test"}, provider, path)
	if err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = session.ReplyScheduled(context.Background(), "scheduled retry"); !errors.Is(err, ErrSessionBusy) {
		t.Fatalf("expected retry: %v", err)
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(before) != string(after) || fmt.Sprint(session.history) != fmt.Sprint(original) {
		t.Fatal("retry changed history or disk")
	}
	provider.summarize = func() (string, error) { return "summary", nil }
	if _, err = session.ReplyScheduled(context.Background(), "scheduled retry"); err != nil {
		t.Fatal(err)
	}
	_, histories := provider.calls()
	count := 0
	for _, msg := range histories[len(histories)-1] {
		if msg.Role == "user" && msg.Content == "scheduled retry" {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("scheduled request appeared %d times", count)
	}
}

func TestInflightCodexHandlerContextFailureRedactsCause(t *testing.T) {
	for _, tc := range []struct {
		name           string
		contextErr     error
		classification string
	}{
		{"deadline", context.DeadlineExceeded, requestFailureTimeout},
		{"canceled", context.Canceled, requestFailureInterrupted},
	} {
		t.Run(tc.name, func(t *testing.T) {
			requests, actions := 0, 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests++
				args, _ := json.Marshal(`{}`)
				json.NewEncoder(w).Encode(codexResponse{Output: []codexOutputItem{
					{Type: "function_call", CallID: "1", Name: "shell_execute", Arguments: args},
					{Type: "function_call", CallID: "2", Name: "shell_execute", Arguments: args},
				}})
			}))
			defer server.Close()
			provider := runtimeTestProvider(t, server.Client(), server.URL)
			private := "https://private.example/search?q=private-query"
			cause := errors.Join(fmt.Errorf("GET %s: %w", private, tc.contextErr), errors.New("private-joined-cause"))
			provider.tools.byName["shell_execute"] = func(context.Context, string) (string, error) {
				actions++
				return "", cause
			}
			path := filepath.Join(t.TempDir(), "session.json")
			session, err := NewPersistentSessionAt(Config{Model: DefaultModel}, provider, path)
			if err != nil {
				t.Fatal(err)
			}
			var replyErr error
			log := captureChatDiagnostics(t, func() { _, replyErr = session.Reply(context.Background(), "test request") })
			if !errors.Is(replyErr, cause) || !errors.Is(replyErr, tc.contextErr) {
				t.Fatalf("lost cause: %v", replyErr)
			}
			terminal := requestFailureDiagnostic(replyErr)
			agent := ModelStreamMessageForError(replyErr, "")
			if !strings.Contains(terminal, tc.classification) {
				t.Fatalf("classification: %q", terminal)
			}
			if !strings.Contains(agent, "Tool handler failed; its outcome is uncertain.") || !strings.Contains(log, "ERROR operation=chat.session.reply") {
				t.Fatalf("agent message or diagnostics missing: agent=%q log=%q", agent, log)
			}
			for name, value := range map[string]string{"terminal": terminal, "agent": agent, "diagnostics": log} {
				for _, secret := range []string{private, "private-query", "private-joined-cause"} {
					if strings.Contains(value, secret) {
						t.Fatalf("%s exposed cause: %q", name, value)
					}
				}
			}
			if requests != 1 || actions != 1 {
				t.Fatalf("requests=%d actions=%d", requests, actions)
			}
			loaded, err := NewSessionStoreAt(path).Load()
			if err != nil {
				t.Fatal(err)
			}
			disk := fmt.Sprint(loaded.Messages)
			if !strings.Contains(disk, "shell_execute started; outcome uncertain") || strings.Contains(disk, "shell_execute completed") {
				t.Fatalf("disk evidence: %s", disk)
			}
		})
	}
}
