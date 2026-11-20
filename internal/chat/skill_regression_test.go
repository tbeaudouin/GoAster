package chat

import (
	"bytes"
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
	"sync/atomic"
	"testing"

	"goaster/internal/artefacts"
	"goaster/internal/diagnostics"
	"goaster/internal/localenv"
)

func TestSelectedSkillChangesRefreshBetweenTurnsAndMidBatch(t *testing.T) {
	setupSkillFixture(t)
	root, err := artefacts.Root()
	if err != nil {
		t.Fatal(err)
	}
	workflow := filepath.Join(root, "skills", "fixture-skill", "SKILL.md")
	phase, executions := "direct", 0
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request codexRequest
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Error(err)
			return
		}
		if !strings.Contains(request.Instructions, "fixture-skill") || strings.Contains(request.Instructions, "PRIVATE WORKFLOW TEXT") || strings.Contains(request.Instructions, "FIXTURE AUTHENTICATION") {
			t.Error("request did not restrict unselected skills to metadata")
		}
		if phase == "change" {
			writeFixture(t, workflow, "CHANGED WORKFLOW")
			phase = "done"
		}
		if phase == "batch" {
			fmt.Fprint(w, `{"output":[{"type":"function_call","call_id":"first","name":"shell_execute","arguments":"{}"},{"type":"function_call","call_id":"second","name":"shell_execute","arguments":"{}"}]}`)
			return
		}
		if phase == "done" {
			fmt.Fprint(w, `{"output_text":"done"}`)
			return
		}
		writeCodexFunctionCall(t, w, "action", "shell_execute", `{}`)
		if phase == "direct" {
			phase = "done"
		}
	}))
	defer backend.Close()
	provider := newShellTestProvider(t, backend)
	provider.tools.byName["shell_execute"] = func(context.Context, string) (string, error) { executions++; return "ok", nil }
	if _, err := provider.Reply(context.Background(), "gpt-test", "perform a task"); err != nil || executions != 1 {
		t.Fatalf("direct tool: executions=%d error=%v", executions, err)
	}
	content, err := artefacts.Load("fixture-skill")
	if err != nil {
		t.Fatal(err)
	}
	session := NewSession(Config{Model: "gpt-test"}, provider)
	session.selectedSkills = []selectedSkill{{Name: "fixture-skill", Digest: skillDigest(content)}}
	writeFixture(t, workflow, "CHANGED WORKFLOW")
	phase = "direct"
	if reply, err := session.ReplyScheduled(context.Background(), "perform task"); err != nil || reply != "done" || executions != 2 {
		t.Fatalf("changed selected skill was not refreshed: executions=%d reply=%q error=%v", executions, reply, err)
	}
	content, err = artefacts.Load("fixture-skill")
	if err != nil || session.selectedSkills[0].Digest != skillDigest(content) {
		t.Fatalf("refreshed selected digest = %+v, content error=%v", session.selectedSkills, err)
	}
	writeFixture(t, workflow, "PRIVATE WORKFLOW TEXT")
	phase = "batch"
	provider.tools.byName["shell_execute"] = func(context.Context, string) (string, error) {
		executions++
		writeFixture(t, workflow, "CHANGED DURING FIRST ACTION")
		phase = "done"
		return "ok", nil
	}
	if reply, err := session.ReplyScheduled(context.Background(), "perform two tasks"); err != nil || reply != "done" || executions != 3 {
		t.Fatalf("changed guidance did not continue safely: executions=%d reply=%q error=%v", executions, reply, err)
	}

}

func TestSelectedSkillRefreshPersistsThroughCompactionAndRestart(t *testing.T) {
	setupSkillFixture(t)
	root, err := artefacts.Root()
	if err != nil {
		t.Fatal(err)
	}
	workflow := filepath.Join(root, "skills", "fixture-skill", "SKILL.md")
	original, err := artefacts.Load("fixture-skill")
	if err != nil {
		t.Fatal(err)
	}
	pathName := filepath.Join(t.TempDir(), "session.json")
	provider := &summaryTestProvider{}
	session, err := NewPersistentSessionAt(Config{Model: "gpt-test"}, provider, pathName)
	if err != nil {
		t.Fatal(err)
	}
	session.selectedSkills = []selectedSkill{{Name: "fixture-skill", Digest: skillDigest(original)}}
	session.history = numberedHistory(maxSessionMessages - 1)
	session.summary = "prior durable summary"
	version, err := session.store.saveStateWithVersion(session.summary, session.history, session.selectedSkills, session.storeVersion, true)
	if err != nil {
		t.Fatal(err)
	}
	session.storeVersion = version
	writeFixture(t, workflow, "REFRESHED WORKFLOW")

	if reply, err := session.Reply(context.Background(), "continue after the refresh"); err != nil || !strings.Contains(reply, "answer to") {
		t.Fatalf("compacted refreshed turn = %q, %v", reply, err)
	}
	loaded, err := NewSessionStoreAt(pathName).Load()
	if err != nil {
		t.Fatal(err)
	}
	refreshed, err := artefacts.Load("fixture-skill")
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Summary == "" || len(loaded.selectedSkills) != 1 || loaded.selectedSkills[0].Digest != skillDigest(refreshed) {
		t.Fatalf("compaction lost refreshed durable state: summary=%q skills=%+v", loaded.Summary, loaded.selectedSkills)
	}

	restarted, err := NewPersistentSessionAt(Config{Model: "gpt-test"}, provider, pathName)
	if err != nil {
		t.Fatal(err)
	}
	if len(restarted.selectedSkills) != 1 || restarted.selectedSkills[0].Digest != skillDigest(refreshed) || restarted.summary == "" {
		t.Fatalf("restart lost refreshed selection or summary: summary=%q skills=%+v", restarted.summary, restarted.selectedSkills)
	}
}

func TestMidTurnSkillRefreshIsBoundedWithoutExecutingStaleCalls(t *testing.T) {
	setupSkillFixture(t)
	root, err := artefacts.Root()
	if err != nil {
		t.Fatal(err)
	}
	workflow := filepath.Join(root, "skills", "fixture-skill", "SKILL.md")
	content, err := artefacts.Load("fixture-skill")
	if err != nil {
		t.Fatal(err)
	}
	var requests atomic.Int32
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		step := requests.Add(1)
		writeFixture(t, workflow, fmt.Sprintf("CHURNED WORKFLOW %d", step))
		fmt.Fprint(w, `{"output":[{"type":"function_call","call_id":"stale","name":"shell_execute","arguments":"{}"}]}`)
	}))
	defer backend.Close()
	provider := newShellTestProvider(t, backend)
	var executions atomic.Int32
	provider.tools.byName["shell_execute"] = func(context.Context, string) (string, error) {
		executions.Add(1)
		return "must not run", nil
	}
	session := NewSession(Config{Model: "gpt-test"}, provider)
	session.selectedSkills = []selectedSkill{{Name: "fixture-skill", Digest: skillDigest(content)}}
	reply, err := session.Reply(context.Background(), "run the selected workflow")
	if err != nil || reply != skillRefreshLimitReply {
		t.Fatalf("bounded refresh = %q, %v", reply, err)
	}
	if requests.Load() > int32(maxSkillRefreshesPerTurn+1) || executions.Load() != 0 {
		t.Fatalf("refresh churn was not bounded safely: requests=%d executions=%d", requests.Load(), executions.Load())
	}
	if strings.Contains(reply, "/clear") || SkillRecoveryMessage(err) != "" {
		t.Fatalf("routine refresh churn became a manual recovery: reply=%q error=%v", reply, err)
	}
	current, loadErr := artefacts.Load("fixture-skill")
	if loadErr != nil || session.selectedSkills[0].Digest != skillDigest(current) || session.skillCheckpointFailed || len(session.history) != 2 || session.history[1].Content != reply {
		t.Fatalf("churn did not retain the current checkpoint and turn: load error=%v, checkpoint failed=%v, history=%+v", loadErr, session.skillCheckpointFailed, session.history)
	}
}

func TestMidTurnSkillRefreshKeepsCompletedOutputAndDropsUnexecutedBatch(t *testing.T) {
	setupSkillFixture(t)
	root, err := artefacts.Root()
	if err != nil {
		t.Fatal(err)
	}
	workflow := filepath.Join(root, "skills", "fixture-skill", "SKILL.md")
	content, err := artefacts.Load("fixture-skill")
	if err != nil {
		t.Fatal(err)
	}
	var requests atomic.Int32
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		step := requests.Add(1)
		if step == 1 {
			fmt.Fprint(w, `{"output":[{"type":"function_call","call_id":"call_goaster_restored_skill_action","name":"shell_execute","arguments":"{}"},{"type":"function_call","call_id":"second","name":"shell_execute","arguments":"{}"}]}`)
			return
		}
		var request codexRequest
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Error(err)
			return
		}
		data, err := json.Marshal(request.Input)
		if err != nil {
			t.Error(err)
			return
		}
		text := string(data)
		if !strings.Contains(text, "first result") || !strings.Contains(text, "REFRESHED WORKFLOW") || step == 2 && strings.Contains(text, `"call_id":"second"`) {
			t.Errorf("continuation input replayed stale batch or lost completed output: %s", text)
		}
		if step == 2 {
			fmt.Fprint(w, `{"output":[{"type":"function_call","call_id":"second","name":"shell_execute","arguments":"{}"}]}`)
			return
		}
		if !strings.Contains(text, "stale tool batch discarded") {
			t.Error("repeated stale call did not receive a rejection output")
		}
		fmt.Fprint(w, `{"output_text":"continued with refreshed guidance"}`)
	}))
	defer backend.Close()
	provider := newShellTestProvider(t, backend)
	var calls atomic.Int32
	provider.tools.byName["shell_execute"] = func(_ context.Context, arguments string) (string, error) {
		calls.Add(1)
		if arguments != "{}" {
			t.Errorf("unexpected tool arguments: %s", arguments)
		}
		writeFixture(t, workflow, "REFRESHED WORKFLOW")
		return "first result", nil
	}
	session := NewSession(Config{Model: "gpt-test"}, provider)
	session.selectedSkills = []selectedSkill{{Name: "fixture-skill", Digest: skillDigest(content)}}
	reply, err := session.Reply(context.Background(), "run two steps")
	if err != nil || reply != "continued with refreshed guidance" {
		t.Fatalf("mid-turn continuation = %q, %v", reply, err)
	}
	if calls.Load() != 1 || requests.Load() != 3 {
		t.Fatalf("tool replay counts = calls %d requests %d", calls.Load(), requests.Load())
	}
}

func TestSelectedSkillRefreshFailsClosedForMalformedOrDeletedFiles(t *testing.T) {
	setupSkillFixture(t)
	root, err := artefacts.Root()
	if err != nil {
		t.Fatal(err)
	}
	authPath := filepath.Join(root, "skills", "fixture-skill", "AUTH.md")
	content, err := artefacts.Load("fixture-skill")
	if err != nil {
		t.Fatal(err)
	}
	calls := 0
	provider := providerFunc(func(context.Context, string, string) (string, error) {
		calls++
		return "unexpected", nil
	})
	session := NewSession(Config{Model: "gpt-test"}, provider)
	session.selectedSkills = []selectedSkill{{Name: "fixture-skill", Digest: skillDigest(content)}}
	if err := os.WriteFile(authPath, []byte("invalid UTF-8: \xff"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := session.Reply(context.Background(), "continue"); err == nil || SkillRecoveryMessage(err) != skillResetMessage || calls != 0 {
		t.Fatalf("malformed selected skill was not rejected safely: calls=%d error=%v", calls, err)
	}
	writeFixture(t, authPath, "FIXTURE AUTHENTICATION")
	if err := os.Remove(authPath); err != nil {
		t.Fatal(err)
	}
	if _, err := session.Reply(context.Background(), "continue"); err == nil || SkillRecoveryMessage(err) != skillResetMessage || calls != 0 {
		t.Fatalf("deleted selected skill was not rejected safely: calls=%d error=%v", calls, err)
	}
}

func TestSelectedSkillRefreshSaveFailureStopsBeforeTheModel(t *testing.T) {
	setupSkillFixture(t)
	root, err := artefacts.Root()
	if err != nil {
		t.Fatal(err)
	}
	workflow := filepath.Join(root, "skills", "fixture-skill", "SKILL.md")
	content, err := artefacts.Load("fixture-skill")
	if err != nil {
		t.Fatal(err)
	}
	pathName := filepath.Join(t.TempDir(), "session.json")
	calls := 0
	provider := providerFunc(func(context.Context, string, string) (string, error) {
		calls++
		return "unexpected", nil
	})
	session, err := NewPersistentSessionAt(Config{Model: "gpt-test"}, provider, pathName)
	if err != nil {
		t.Fatal(err)
	}
	session.selectedSkills = []selectedSkill{{Name: "fixture-skill", Digest: skillDigest(content)}}
	version, err := session.store.saveStateWithVersion("summary", nil, session.selectedSkills, session.storeVersion, true)
	if err != nil {
		t.Fatal(err)
	}
	session.storeVersion = version
	writeFixture(t, workflow, "UPDATED WORKFLOW")
	session.store.syncDirectory = func(string) error { return errors.New("refresh checkpoint unavailable") }
	if _, err := session.Reply(context.Background(), "continue"); err == nil || SkillRecoveryMessage(err) != skillSaveMessage || calls != 0 {
		t.Fatalf("refresh save failure was not actionable and safe: calls=%d error=%v", calls, err)
	}
}

func TestUnselectedSkillContentsAreLoadedOnlyOnDemand(t *testing.T) {
	setupSkillFixture(t)
	root, err := artefacts.Root()
	if err != nil {
		t.Fatal(err)
	}
	writeFixture(t, filepath.Join(root, "skills", "fixture-skill", "AUTH.md"), "bad UTF-8: \xff")
	for _, name := range []string{"large-a", "large-b", "large-c"} {
		dir := filepath.Join(root, "skills", name)
		writeFixture(t, filepath.Join(dir, "META.md"), "name: "+name+"\ndescription: large fixture\n")
		writeFixture(t, filepath.Join(dir, "AUTH.md"), "required authentication for "+name)
		writeFixture(t, filepath.Join(dir, "SKILL.md"), strings.Repeat(name, (256<<10)/len(name)))
	}
	backendCalls := 0
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		backendCalls++
		data, err := io.ReadAll(r.Body)
		if err != nil {
			t.Error(err)
		}
		if strings.Contains(string(data), "required authentication for large-a") || strings.Contains(string(data), "bad UTF-8") {
			t.Error("unselected skill contents were injected")
		}
		fmt.Fprint(w, `{"output_text":"done"}`)
	}))
	defer backend.Close()
	provider := newShellTestProvider(t, backend)
	if _, err := provider.Reply(context.Background(), "gpt-test", "hello"); err != nil || backendCalls != 1 {
		t.Fatalf("unselected skills blocked request: calls=%d error=%v", backendCalls, err)
	}
	if _, err := executeLoadSkill(context.Background(), `{"name":"fixture-skill"}`); SkillRecoveryMessage(err) != skillResetMessage {
		t.Fatalf("invalid selected authentication accepted: %v", err)
	}
}

func TestSelectedSkillCountsTowardCompaction(t *testing.T) {
	setupSkillFixture(t)
	home, _ := artefacts.Root()
	writeFixture(t, filepath.Join(home, "skills", "fixture-skill", "SKILL.md"), strings.Repeat("x", 192<<10))
	content, err := artefacts.Load("fixture-skill")
	if err != nil {
		t.Fatal(err)
	}
	selected := []selectedSkill{{Name: "fixture-skill", Digest: skillDigest(content)}}
	summaryRequests, normalRequests := 0, 0
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request codexRequest
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Error(err)
			return
		}
		if len(request.Tools) == 0 {
			summaryRequests++
			fmt.Fprint(w, `{"output_text":"compacted history"}`)
			return
		}
		normalRequests++
		if !strings.Contains(fmt.Sprint(request.Input), content) {
			t.Error("restored skill omitted after compaction")
		}
		if tokens := 4096 + estimateMessageTokens(ChatMessage{Content: request.Instructions}) + codexItemTokens(request.Input); tokens > maxContextTokens {
			t.Errorf("compacted request exceeds budget: %d", tokens)
		}
		fmt.Fprint(w, `{"output_text":"done"}`)
	}))
	defer backend.Close()
	provider := newShellTestProvider(t, backend)
	base, err := provider.skillContextOverhead(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	memoryContext := WithMemoryContext(context.Background(), MemoryContext{DailyNotes: strings.Repeat("daily note ", 4096)})
	withMemory, err := provider.skillContextOverhead(memoryContext, nil)
	if err != nil || withMemory <= base {
		t.Fatalf("daily note overhead base=%d with notes=%d error=%v", base, withMemory, err)
	}
	withSkill, err := provider.skillContextOverhead(context.Background(), selected)
	if err != nil || withSkill <= base {
		t.Fatalf("skill overhead base=%d selected=%d error=%v", base, withSkill, err)
	}
	session := NewSession(Config{Model: "gpt-test"}, provider)
	session.selectedSkills = selected
	historyTokens := summarizeTokens - withSkill
	session.history = append(historyWithTokens(historyTokens), numberedHistory(5)...)
	if _, err := session.Reply(context.Background(), "continue"); err != nil {
		t.Fatal(err)
	}
	if summaryRequests != 1 || normalRequests != 1 {
		t.Fatalf("summary=%d normal=%d", summaryRequests, normalRequests)
	}
}

func TestTerminalShowsSkillRecoveryDetails(t *testing.T) {
	for _, message := range []string{skillResetMessage, skillChangedMessage, skillBudgetMessage, skillSaveMessage} {
		if !IsSkillRecoveryMessage(message) || !strings.Contains(message, skillActionNotice) || strings.Contains(message, "restart the workflow") {
			t.Fatalf("unsafe or unrecognized recovery message: %q", message)
		}
	}
	private := strings.Repeat("private-detail-", 1000)
	privateError := errors.New(private)
	provider := providerFunc(func(context.Context, string, string) (string, error) {
		return "", skillRecovery(privateError, skillResetMessage)
	})
	store := localenv.NewStoreAt(filepath.Join(t.TempDir(), "env"))
	if err := store.Load(); err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	err := runChatLoopWithUI(context.Background(), TerminalInput{reader: strings.NewReader("run\n/clear\n"), interactive: true}, &output,
		NewSession(defaultConfig(t), provider), store,
		EnvironmentFilePickerFunc(func() (string, error) { return "", ErrEnvironmentFilePickerCanceled }), terminalUI{interactive: true, width: 200})
	if err != nil || !strings.Contains(output.String(), "/clear") || !strings.Contains(output.String(), "Conversation history cleared") || !strings.Contains(output.String(), "private-detail") {
		t.Fatalf("terminal output = %q, error=%v", output.String(), err)
	}
	output.Reset()
	err = runChatLoopWithUI(context.Background(), TerminalInput{reader: strings.NewReader("run\n")}, &output,
		NewSession(defaultConfig(t), provider), store,
		EnvironmentFilePickerFunc(func() (string, error) { return "", ErrEnvironmentFilePickerCanceled }), terminalUI{})
	if err == nil || !errors.Is(err, privateError) || !strings.Contains(err.Error(), skillResetMessage) || !strings.Contains(err.Error(), "private-detail") {
		t.Fatalf("line mode error = %v", err)
	}

	path := filepath.Join(t.TempDir(), "session.json")
	calls := 0
	recoveredProvider := providerFunc(func(context.Context, string, string) (string, error) {
		calls++
		return "unexpected", nil
	})
	session, err := NewPersistentSessionAt(Config{Model: "test"}, recoveredProvider, path)
	if err != nil {
		t.Fatal(err)
	}
	writeFixture(t, path, "invalid saved conversation")
	if _, err := session.ReplyScheduled(context.Background(), "scheduled"); !errors.Is(err, ErrSessionBusy) {
		t.Fatalf("scheduler recovery = %v", err)
	}
	output.Reset()
	err = runChatLoopWithUI(context.Background(), TerminalInput{reader: strings.NewReader("continue\n/clear\n"), interactive: true}, &output,
		session, store, EnvironmentFilePickerFunc(func() (string, error) { return "", ErrEnvironmentFilePickerCanceled }), terminalUI{interactive: true, width: 200})
	if err != nil || calls != 0 || !strings.Contains(output.String(), "saved conversation is missing or invalid") || !strings.Contains(output.String(), "Conversation history cleared") {
		t.Fatalf("scheduled recovery output = %q, calls=%d, error=%v", output.String(), calls, err)
	}

}

func TestRefreshReplacesPriorLoadOutputAndKeepsCompletedCalls(t *testing.T) {
	input := []codexInputItem{
		{Type: "function_call", CallID: "call_goaster_restored_skill_0", Name: "load_skill", restoredSkill: true},
		{Type: "function_call_output", CallID: "call_goaster_restored_skill_0", Output: "OLD SNAPSHOT AUTH", restoredSkill: true},
		{Type: "function_call", CallID: "call_goaster_restored_skill_load", Name: "load_skill"},
		{Type: "function_call_output", CallID: "call_goaster_restored_skill_load", Output: "OLD AUTH"},
		{Type: "function_call", CallID: "action", Name: "shell_execute"},
		{Type: "function_call_output", CallID: "action", Output: "completed result"},
	}
	fresh := []codexInputItem{
		{Type: "function_call", CallID: "call_goaster_restored_skill_0", Name: "load_skill", restoredSkill: true},
		{Type: "function_call_output", CallID: "call_goaster_restored_skill_0", Output: "NEW AUTH", restoredSkill: true},
	}
	result := replaceRestoredSkillItems(input, fresh)
	data, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	if strings.Contains(text, "OLD") || strings.Contains(text, "restoredSkill") || !strings.Contains(text, "NEW AUTH") || !strings.Contains(text, "completed result") || len(result) != 6 || result[3].CallID != "call_goaster_restored_skill_load" || result[3].Output == "" {
		t.Fatalf("refresh lost protocol pairs or retained stale guidance: %s", text)
	}
}

func TestRefreshRemovesStaleLoadOutputFromContinuation(t *testing.T) {
	setupSkillFixture(t)
	root, err := artefacts.Root()
	if err != nil {
		t.Fatal(err)
	}
	auth := filepath.Join(root, "skills", "fixture-skill", "AUTH.md")
	var requests atomic.Int32
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch requests.Add(1) {
		case 1:
			fmt.Fprint(w, `{"output":[{"type":"function_call","call_id":"load-original","name":"load_skill","arguments":"{\"name\":\"fixture-skill\"}"}]}`)
		case 2:
			writeFixture(t, auth, "FRESH AUTH GUIDANCE")
			fmt.Fprint(w, `{"output_text":"outdated answer"}`)
		case 3:
			var request codexRequest
			if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
				t.Error(err)
				return
			}
			data, err := json.Marshal(request.Input)
			if err != nil {
				t.Error(err)
				return
			}
			if strings.Contains(string(data), "FIXTURE AUTHENTICATION") || !strings.Contains(string(data), "FRESH AUTH GUIDANCE") {
				t.Error("continuation did not replace old authentication guidance")
			}
			fmt.Fprint(w, `{"output_text":"fresh answer"}`)
		default:
			t.Error("unexpected request")
		}
	}))
	defer backend.Close()
	session := NewSession(Config{Model: "gpt-test"}, newShellTestProvider(t, backend))
	if reply, err := session.Reply(context.Background(), "use the skill"); err != nil || reply != "fresh answer" || requests.Load() != 3 {
		t.Fatalf("refresh continuation = %q, %v, requests=%d", reply, err, requests.Load())
	}
}

func TestLegacyAnonymousSelectionFailsWithSafeRecovery(t *testing.T) {
	_, err := loadSelectedSkillSnapshot([]selectedSkill{{}})
	if err == nil || SkillRecoveryMessage(err) != skillAnonymousMessage || !IsSkillRecoveryMessage(skillAnonymousMessage) {
		t.Fatalf("anonymous recovery = %v, %q", err, SkillRecoveryMessage(err))
	}
	log := captureChatDiagnostics(t, func() { diagnostics.Error("chat.session.reply", err) })
	if !strings.Contains(log, `skill_stage="restore"`) || !strings.Contains(log, `skill_reason="unknown_selection"`) {
		t.Fatalf("diagnostic = %q", log)
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
