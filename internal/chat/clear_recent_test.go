package chat

import (
	"context"
	"errors"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestClearRecentTurnsPersistenceAndBoundaries(t *testing.T) {
	path := filepath.Join(t.TempDir(), "session.json")
	store := NewSessionStoreAt(path)
	history := []ChatMessage{{Role: "user", Content: "old"}, {Role: "assistant", Content: "answer"}, {Role: "user", Content: "new"}, {Role: "assistant", Content: "tool evidence"}, {Role: "guidance", Content: "skill guidance"}, {Role: "assistant", Content: "interrupted"}}
	if err := store.saveState("older summary", history); err != nil {
		t.Fatal(err)
	}
	session, err := NewPersistentSessionAt(Config{Model: "test"}, providerFunc(func(context.Context, string, string) (string, error) { t.Fatal("provider called"); return "", nil }), path)
	if err != nil {
		t.Fatal(err)
	}
	for _, command := range []string{"/clear x", "/clear 0", "/clear -1", "/clear +1", "/clear 999999999999999999999999999", "/clear 1 2"} {
		reply, err := session.Reply(context.Background(), command)
		if err != nil || reply != "Usage: /clear [n]" || !reflect.DeepEqual(session.history, history) {
			t.Fatalf("%q: %q %v %#v", command, reply, err, session.history)
		}
	}
	reply, err := session.Reply(context.Background(), "/clear 1")
	if err != nil || !strings.Contains(reply, "Removed 1 ") || !strings.Contains(reply, "summarized content remains") {
		t.Fatalf("reply %q: %v", reply, err)
	}
	loaded, err := store.Load()
	if err != nil || loaded.Summary != "older summary" || !reflect.DeepEqual(loaded.Messages, history[:2]) {
		t.Fatalf("reload: %#v %v", loaded, err)
	}
	reply, err = session.Reply(context.Background(), "/clear 9")
	if err != nil || !strings.Contains(reply, "Removed 1 ") {
		t.Fatalf("excess: %q %v", reply, err)
	}
	reply, err = session.Reply(context.Background(), "/clear 2")
	if err != nil || !strings.Contains(reply, "Removed 0 ") {
		t.Fatalf("empty: %q %v", reply, err)
	}
	loaded, err = store.Load()
	if err != nil || loaded.Summary != "older summary" || len(loaded.Messages) != 0 {
		t.Fatalf("reload empty: %#v %v", loaded, err)
	}
}

func TestClearRecentSaveFailureKeepsAcknowledgedHistory(t *testing.T) {
	path := filepath.Join(t.TempDir(), "session.json")
	session, err := NewPersistentSessionAt(Config{Model: "test"}, providerFunc(func(context.Context, string, string) (string, error) { return "ok", nil }), path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = session.Reply(context.Background(), "hello"); err != nil {
		t.Fatal(err)
	}
	before := cloneChatMessages(session.history)
	cause := errors.New("private failure detail")
	session.store.syncDirectory = func(string) error { return cause }
	_, err = session.Reply(context.Background(), "/clear 1")
	if !errors.Is(err, cause) || !errors.Is(err, errConversationSaveFailed) || !reflect.DeepEqual(session.history, before) {
		t.Fatalf("save failure: %v %#v", err, session.history)
	}
	if strings.Contains(requestFailureDiagnostic(err), cause.Error()) {
		t.Fatal("failure diagnostic exposed private detail")
	}
}

func TestTerminalClearRecentRoutesWithoutProvider(t *testing.T) {
	path := filepath.Join(t.TempDir(), "session.json")
	store := NewSessionStoreAt(path)
	if err := store.Save([]ChatMessage{{Role: "user", Content: "one"}, {Role: "assistant", Content: "reply"}}); err != nil {
		t.Fatal(err)
	}
	session, err := NewSessionWithStore(defaultConfig(t), providerFunc(func(context.Context, string, string) (string, error) { t.Fatal("provider called"); return "", nil }), NewSessionStoreAt(path))
	if err != nil {
		t.Fatal(err)
	}
	var output strings.Builder
	if err := runWithTestEnvStore(t, context.Background(), strings.NewReader("/clear 1\n"), &output, session); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output.String(), "Removed 1 recent user turn") {
		t.Fatal(output.String())
	}
	loaded, err := store.Load()
	if err != nil || len(loaded.Messages) != 0 {
		t.Fatalf("reload: %#v %v", loaded, err)
	}
}

func TestClearThreeTurnsLeavesOlderTurn(t *testing.T) {
	session := NewSession(Config{Model: "test"}, providerFunc(func(context.Context, string, string) (string, error) { t.Fatal("provider called"); return "", nil }))
	session.history = []ChatMessage{{Role: "user", Content: "keep"}, {Role: "assistant", Content: "keep reply"}, {Role: "user", Content: "one"}, {Role: "assistant", Content: "tool"}, {Role: "guidance", Content: "guidance"}, {Role: "user", Content: "two"}, {Role: "assistant", Content: "answer"}, {Role: "user", Content: "three"}, {Role: "assistant", Content: "interrupted"}}
	want := cloneChatMessages(session.history[:2])
	reply, err := session.Reply(context.Background(), "/clear 3")
	if err != nil || !strings.Contains(reply, "Removed 3 ") || !reflect.DeepEqual(session.history, want) {
		t.Fatalf("reply %q, error %v, history %#v", reply, err, session.history)
	}
}

func TestParseClearCommand(t *testing.T) {
	for _, tt := range []struct {
		input      string
		count      int
		all, valid bool
	}{
		{"/clear", 0, true, true}, {" \t/clear\n", 0, true, true},
		{"/clear 1", 1, false, true}, {"/clear 003", 3, false, true},
		{"", 0, false, false}, {"/clearer", 0, false, false},
		{"/clear 0", 0, false, false}, {"/clear -1", 0, false, false},
		{"/clear +1", 0, false, false}, {"/clear １", 0, false, false},
		{"/clear 1 2", 0, false, false}, {"/clear x", 0, false, false},
		{"/clear 999999999999999999999999999", 0, false, false},
	} {
		t.Run(tt.input, func(t *testing.T) {
			count, all, valid := parseClearCommand(tt.input)
			if valid != tt.valid || (valid && (count != tt.count || all != tt.all)) || ValidClearCommand(tt.input) != tt.valid {
				t.Fatalf("parse = %d, %t, %t", count, all, valid)
			}
		})
	}
}

func TestClearRecentActualToolGuidanceAndInterruption(t *testing.T) {
	session := NewSession(Config{Model: "test"}, evidenceSequenceProvider{})
	if _, err := session.Reply(context.Background(), "keep"); err != nil {
		t.Fatal(err)
	}
	want := cloneChatMessages(session.history)
	if _, err := session.Reply(context.Background(), "remove tool turn"); err != nil {
		t.Fatal(err)
	}
	foundTool, foundGuidance := false, false
	for _, message := range session.history[len(want):] {
		foundTool = foundTool || message.Role == "assistant" && strings.Contains(message.Content, "custom_tool completed")
		foundGuidance = foundGuidance || message.Role == "guidance" && strings.Contains(message.Content, "guidance remains")
	}
	if !foundTool || !foundGuidance {
		t.Fatalf("missing checkpoints: %#v", session.history)
	}
	session.provider = noActionFailureProvider{}
	if _, err := session.Reply(context.Background(), "remove interrupted turn"); err == nil {
		t.Fatal("expected interruption")
	}
	if !strings.Contains(session.history[len(session.history)-1].Content, "Turn interrupted") {
		t.Fatalf("missing interruption: %#v", session.history)
	}
	if _, err := session.Reply(context.Background(), "/clear 2"); err != nil || !reflect.DeepEqual(session.history, want) {
		t.Fatalf("clear: %v, history: %#v", err, session.history)
	}
}

func TestClearRecentPreservesRecoveryAndMigrationGuards(t *testing.T) {
	for _, tt := range []struct {
		name                string
		recovery, migration bool
		want                error
	}{
		{"recovery", true, false, ErrConversationRecovery},
		{"migration", false, true, errMigrationDiagnosticsPending},
	} {
		t.Run(tt.name, func(t *testing.T) {
			session := NewSession(Config{Model: "test"}, noActionFailureProvider{})
			session.history = []ChatMessage{{Role: "user", Content: "retained"}}
			session.recoveryPending, session.migrationDiagnosticsPending = tt.recovery, tt.migration
			_, err := session.Reply(context.Background(), "/clear 1")
			if !errors.Is(err, tt.want) || len(session.history) != 1 || session.history[0].Content != "retained" {
				t.Fatalf("guard: %v, history: %#v", err, session.history)
			}
		})
	}
}
