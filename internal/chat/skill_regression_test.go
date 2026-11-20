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

	"goaster/internal/diagnostics"
)

func TestSkillHistoryRestartReloadAndErrors(t *testing.T) {
	setupSkillFixture(t)
	home, _ := os.UserHomeDir()
	root := filepath.Join(home, ".goaster", "artefacts", "skills")
	for i := 0; i < 11; i++ {
		name := fmt.Sprintf("skill-%02d", i)
		dir := filepath.Join(root, name)
		writeFixture(t, filepath.Join(dir, "META.md"), "name: "+name+"\ndescription: Test skill.\n")
		writeFixture(t, filepath.Join(dir, "SKILL.md"), "FULL CONTENT "+name)
		writeFixture(t, filepath.Join(dir, "AUTH.md"), "authentication")
	}
	var next, seen string
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req codexRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Error(err)
		}
		data, _ := json.Marshal(req.Input)
		seen = string(data)
		calls++
		if next != "" {
			arg := next
			next = ""
			encoded, _ := json.Marshal(arg)
			fmt.Fprintf(w, `{"output":[{"type":"function_call","call_id":"load","name":"load_skill","arguments":%s}]}`, encoded)
			return
		}
		fmt.Fprint(w, `{"output_text":"done"}`)
	}))
	defer server.Close()
	creds := CredentialStore{Path: filepath.Join(t.TempDir(), "credentials")}
	if err := creds.Save(Credential{AccessToken: "test", ExpiresAt: time.Now().Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}
	provider := NewCodexProvider(creds)
	provider.backendURL = server.URL
	provider.httpClient = server.Client()
	path := filepath.Join(t.TempDir(), "session.json")
	session, err := NewPersistentSessionAt(Config{Model: DefaultModel}, provider, path)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 11; i++ {
		next = fmt.Sprintf(`{"name":"skill-%02d"}`, i)
		if _, err = session.Reply(context.Background(), "load"); err != nil {
			t.Fatal(err)
		}
	}
	restarted, err := NewPersistentSessionAt(Config{Model: DefaultModel}, provider, path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = restarted.Reply(context.Background(), "continue"); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 11; i++ {
		if !strings.Contains(seen, fmt.Sprintf("FULL CONTENT skill-%02d", i)) {
			t.Fatalf("missing skill %d after restart", i)
		}
	}
	writeFixture(t, filepath.Join(root, "skill-00", "SKILL.md"), "CHANGED CONTENT")
	next = `{"name":"skill-00"}`
	if _, err = restarted.Reply(context.Background(), "reload"); err != nil {
		t.Fatal(err)
	}
	if _, err = restarted.Reply(context.Background(), "continue"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(seen, "FULL CONTENT skill-00") || !strings.Contains(seen, "CHANGED CONTENT") {
		t.Fatal("reload did not preserve both versions")
	}
	for _, bad := range []string{`{"name":"missing"}`, `{bad`} {
		next = bad
		if _, err = restarted.Reply(context.Background(), "bad skill"); err == nil {
			t.Fatal("expected skill load failure")
		}
		if _, err = restarted.Reply(context.Background(), "next turn"); err != nil {
			t.Fatal(err)
		}
	}
	if calls < 27 {
		t.Fatalf("unexpected requests: %d", calls)
	}
}

func TestSkillGuidanceCompactionAndLegacyMarkers(t *testing.T) {
	path := filepath.Join(t.TempDir(), "session.json")
	messages := numberedHistory(254)
	messages[2] = ChatMessage{Role: "guidance", Content: "complete workflow for compaction"}
	state := map[string]any{"version": sessionFormatVersion, "summary": "previous summary", "messages": messages, "selected_skills": []any{map[string]any{"name": "missing"}, map[string]any{}}}
	data, _ := json.Marshal(state)
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	provider := &summaryTestProvider{}
	session, err := NewPersistentSessionAt(Config{Model: DefaultModel}, provider, path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = session.Reply(context.Background(), "continue"); err != nil {
		t.Fatal(err)
	}
	summaries, _ := provider.calls()
	if len(summaries) == 0 {
		t.Fatal("compaction did not run")
	}
	found := false
	for _, m := range summaries[0].messages {
		if m.Role == "guidance" && m.Content == "complete workflow for compaction" {
			found = true
		}
	}
	if !found {
		t.Fatal("compaction omitted guidance")
	}
	restarted, err := NewPersistentSessionAt(Config{Model: DefaultModel}, provider, path)
	if err != nil || restarted.summary == "" {
		t.Fatalf("summary lost on restart: %v", err)
	}
}

func TestSkillFailureDiagnosticsRedactPrivateDetails(t *testing.T) {
	err := skillRecovery(errors.New("private-name /private/path secret arguments"), skillResetMessage)
	log := captureChatDiagnostics(t, func() { diagnostics.Error("chat.session.reply", err) })
	if !strings.Contains(log, `skill_stage="load"`) || !strings.Contains(log, `skill_reason="invalid_or_unavailable"`) {
		t.Fatalf("diagnostic = %q", log)
	}
	for _, private := range []string{"private-name", "/private/path", "secret", "arguments"} {
		if strings.Contains(log, private) {
			t.Fatalf("private detail in diagnostic: %q", log)
		}
	}
}

func TestManyLoadsInOneTurnAndCheckpointOrdering(t *testing.T) {
	setupSkillFixture(t)
	step := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req codexRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Error(err)
		}
		step++
		if step == 1 {
			calls := make([]codexOutputItem, 0, 11)
			for i := 0; i < 11; i++ {
				args, _ := json.Marshal(`{"name":"fixture-skill"}`)
				calls = append(calls, codexOutputItem{Type: "function_call", CallID: fmt.Sprint(i), Name: "load_skill", Arguments: args})
			}
			json.NewEncoder(w).Encode(codexResponse{OutputText: "thinking before load", Output: calls})
			return
		}
		data, _ := json.Marshal(req.Input)
		text := string(data)
		if !strings.Contains(text, "thinking before load") || strings.Count(text, "PRIVATE WORKFLOW TEXT") != 11 {
			t.Errorf("continuation lost assistant text or skill outputs")
		}
		fmt.Fprint(w, `{"output_text":"done"}`)
	}))
	defer server.Close()
	creds := CredentialStore{Path: filepath.Join(t.TempDir(), "credentials")}
	if err := creds.Save(Credential{AccessToken: "test", ExpiresAt: time.Now().Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}
	provider := NewCodexProvider(creds)
	provider.backendURL = server.URL
	provider.httpClient = server.Client()
	path := filepath.Join(t.TempDir(), "session.json")
	session, err := NewPersistentSessionAt(Config{Model: DefaultModel}, provider, path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = session.Reply(context.Background(), "current user request"); err != nil {
		t.Fatal(err)
	}
	restarted, err := NewPersistentSessionAt(Config{Model: DefaultModel}, provider, path)
	if err != nil {
		t.Fatal(err)
	}
	if len(restarted.history) != 13 || restarted.history[0].Role != "user" || restarted.history[0].Content != "current user request" || restarted.history[1].Role != "guidance" || restarted.history[12].Role != "assistant" {
		t.Fatalf("unexpected checkpoint order: %#v", restarted.history)
	}
}

func TestWebBudgetsExcludeSuppressedSkillBatch(t *testing.T) {
	setupSkillFixture(t)
	step, searches, fetches := 0, 0, 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		step++
		calls := []codexOutputItem{}
		add := func(id, name, args string) {
			encoded, _ := json.Marshal(args)
			calls = append(calls, codexOutputItem{Type: "function_call", CallID: id, Name: name, Arguments: encoded})
		}
		if step == 1 {
			add("load", "load_skill", `{"name":"fixture-skill"}`)
		}
		n, m := 3, 5
		if step == 1 {
			n, m = 4, 6
		}
		if step == 3 {
			n, m = 1, 0
		}
		for i := 0; i < n; i++ {
			add(fmt.Sprintf("s%d-%d", step, i), "web_search", `{}`)
		}
		for i := 0; i < m; i++ {
			add(fmt.Sprintf("f%d-%d", step, i), "web_fetch", `{}`)
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
	provider.tools.byName["web_search"] = func(context.Context, string) (string, error) { searches++; return "search", nil }
	provider.tools.byName["web_fetch"] = func(context.Context, string) (string, error) { fetches++; return "fetch", nil }
	_, err := provider.Reply(context.Background(), DefaultModel, "search")
	if err == nil || !strings.Contains(err.Error(), "too many web_search") || searches != 3 || fetches != 5 || step != 3 {
		t.Fatalf("err=%v searches=%d fetches=%d requests=%d", err, searches, fetches, step)
	}
}

func TestSkillCheckpointSaveFailureStopsContinuation(t *testing.T) {
	setupSkillFixture(t)
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		writeCodexFunctionCall(t, w, "load", "load_skill", `{"name":"fixture-skill"}`)
	}))
	defer server.Close()
	creds := CredentialStore{Path: filepath.Join(t.TempDir(), "credentials")}
	if err := creds.Save(Credential{AccessToken: "test", ExpiresAt: time.Now().Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}
	provider := NewCodexProvider(creds)
	provider.backendURL = server.URL
	provider.httpClient = server.Client()
	session, err := NewPersistentSessionAt(Config{Model: DefaultModel}, provider, filepath.Join(t.TempDir(), "session.json"))
	if err != nil {
		t.Fatal(err)
	}
	cause := errors.New("private checkpoint failure")
	session.store.syncDirectory = func(string) error { return cause }
	log := captureChatDiagnostics(t, func() { _, err = session.Reply(context.Background(), "private prompt") })
	if !errors.Is(err, cause) || !errors.Is(err, errFatalRequest) || requests != 1 {
		t.Fatalf("failure=%v requests=%d", err, requests)
	}
	if !strings.Contains(log, "ERROR") || strings.Contains(log, "private checkpoint failure") || strings.Contains(log, "private prompt") || strings.Contains(log, "PRIVATE WORKFLOW TEXT") {
		t.Fatalf("unsafe or missing diagnostic: %q", log)
	}
}
