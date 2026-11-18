package chat

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"goaster/internal/diagnostics"
)

func sendFileToolHandler(t testing.TB, send SendFileOperation) ToolHandler {
	t.Helper()
	provider := NewCodexProvider(CredentialStore{})
	if err := RegisterSendFileTool(provider, send); err != nil {
		t.Fatal(err)
	}
	handler, ok := provider.tools.handler(sendFileToolName)
	if !ok {
		t.Fatal("send_file handler was not registered")
	}
	return handler
}

func sendFileTelegramContext(kind string, chatID, threadID int64) context.Context {
	ctx := WithConversationType(context.Background(), kind)
	ctx = WithConversationID(ctx, chatID)
	ctx = WithConversationThreadID(ctx, threadID)
	return ctx
}

func TestSendFileToolEndToEndUsesBoundBinaryTelegramDestination(t *testing.T) {
	root := setupFileToolWorkspace(t)
	data := []byte{0x50, 0x4b, 0x03, 0x04, 0x00, 0xff, 0x10, 0x80, 0x00}
	if err := os.WriteFile(filepath.Join(root, "archive.zip"), data, 0600); err != nil {
		t.Fatal(err)
	}
	store := CredentialStore{Path: filepath.Join(t.TempDir(), "auth.json")}
	if err := store.Save(Credential{AccessToken: "access-token", AccountID: "account-123", ExpiresAt: time.Now().Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}

	var calls int
	var sentChatID, sentThreadID int64
	var sentFilename string
	var sentData []byte
	sender := func(_ context.Context, chatID, threadID int64, filename string, content []byte) error {
		calls++
		sentChatID, sentThreadID, sentFilename, sentData = chatID, threadID, filename, append([]byte(nil), content...)
		return nil
	}
	provider := NewCodexProvider(store)
	if err := RegisterSendFileTool(provider, sender); err != nil {
		t.Fatal(err)
	}
	var backendCalls int
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request codexRequest
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Errorf("decode Codex request: %v", err)
			return
		}
		switch backendCalls {
		case 0:
			found := false
			for _, tool := range request.Tools {
				if tool.Name == sendFileToolName {
					found = true
					if _, ok := tool.Parameters["properties"]; !ok {
						t.Errorf("send_file schema omitted properties: %+v", tool.Parameters)
					}
				}
			}
			if !found {
				t.Error("model request omitted send_file")
			}
			writeCodexFunctionCall(t, w, "send-file", sendFileToolName, `{"path":"archive.zip","filename":"bundle.zip"}`)
		case 1:
			if len(request.Input) == 0 || !strings.Contains(request.Input[len(request.Input)-1].Output, "Telegram confirmed delivery") {
				t.Errorf("model did not receive confirmed send_file result: %+v", request.Input)
			}
			_, _ = w.Write([]byte(`{"output_text":"file delivered"}`))
		}
		backendCalls++
	}))
	defer backend.Close()
	provider.backendURL = backend.URL
	provider.httpClient = backend.Client()

	reply, err := provider.Reply(sendFileTelegramContext("supergroup", -100123, 77), "gpt-test", "send the archive")
	if err != nil || reply != "file delivered" {
		t.Fatalf("reply = %q, error = %v", reply, err)
	}
	if calls != 1 || sentChatID != -100123 || sentThreadID != 77 || sentFilename != "bundle.zip" || !bytes.Equal(sentData, data) {
		t.Fatalf("send_file delivery = calls %d, chat %d, thread %d, filename %q, bytes %v", calls, sentChatID, sentThreadID, sentFilename, sentData)
	}
}

func TestSendFileToolBindsPrivateGroupAndTopicAndRejectsForgedDestination(t *testing.T) {
	root := setupFileToolWorkspace(t)
	if err := os.WriteFile(filepath.Join(root, "notes.bin"), []byte{0x00, 0x01, 0xfe}, 0600); err != nil {
		t.Fatal(err)
	}
	var calls []struct {
		chatID   int64
		threadID int64
	}
	handler := sendFileToolHandler(t, func(_ context.Context, chatID, threadID int64, _ string, _ []byte) error {
		calls = append(calls, struct {
			chatID   int64
			threadID int64
		}{chatID, threadID})
		return nil
	})
	for _, test := range []struct {
		name     string
		kind     string
		chatID   int64
		threadID int64
	}{
		{name: "private", kind: "private", chatID: 42},
		{name: "group", kind: "group", chatID: -10042},
		{name: "topic", kind: "supergroup", chatID: -10042, threadID: 19},
	} {
		t.Run(test.name, func(t *testing.T) {
			result, err := handler(sendFileTelegramContext(test.kind, test.chatID, test.threadID), `{"path":"notes.bin"}`)
			if err != nil || !strings.Contains(result, "Telegram confirmed delivery") {
				t.Fatalf("result = %q, error = %v", result, err)
			}
			got := calls[len(calls)-1]
			if got.chatID != test.chatID || got.threadID != test.threadID {
				t.Fatalf("destination = %+v, want chat %d thread %d", got, test.chatID, test.threadID)
			}
		})
	}
	before := len(calls)
	result, err := handler(sendFileTelegramContext("group", -10042, 19), `{"path":"notes.bin","chat_id":999,"thread_id":1,"token":"forged"}`)
	if err != nil || !strings.Contains(result, "only path and optional filename") || len(calls) != before {
		t.Fatalf("forged destination result = %q, error = %v, calls = %d", result, err, len(calls))
	}
}

func TestSendFileToolRejectsMissingContextAndUnsafeFiles(t *testing.T) {
	root := setupFileToolWorkspace(t)
	outside := filepath.Join(filepath.Dir(root), "outside-private.bin")
	secret := []byte("private-file-content")
	if err := os.WriteFile(outside, secret, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "regular.bin"), []byte{0x00, 0x01, 0x02}, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(root, "directory"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "large.bin"), bytes.Repeat([]byte{0x7f}, int(sendFileMaxBytes+1)), 0600); err != nil {
		t.Fatal(err)
	}
	symlinkPath := filepath.Join(root, "linked.bin")
	if err := os.Symlink(outside, symlinkPath); err != nil {
		symlinkPath = ""
	}

	calls := 0
	handler := sendFileToolHandler(t, func(context.Context, int64, int64, string, []byte) error {
		calls++
		return nil
	})
	var diagnostic bytes.Buffer
	restore := diagnostics.SetOutput(&diagnostic)
	defer restore()
	absoluteOutside, err := json.Marshal(map[string]string{"path": outside})
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name string
		args string
	}{
		{name: "terminal", args: `{"path":"regular.bin"}`},
		{name: "missing type", args: `{"path":"regular.bin"}`},
		{name: "outside", args: string(absoluteOutside)},
		{name: "directory", args: `{"path":"directory"}`},
		{name: "oversize", args: `{"path":"large.bin"}`},
		{name: "unsafe filename", args: `{"path":"regular.bin","filename":"../private.bin"}`},
		{name: "empty filename", args: `{"path":"regular.bin","filename":""}`},
		{name: "duplicate path", args: `{"path":"regular.bin","path":"regular.bin"}`},
		{name: "duplicate filename", args: `{"path":"regular.bin","filename":"a","filename":"b"}`},
	}
	if symlinkPath != "" {
		cases = append(cases, struct {
			name string
			args string
		}{name: "symlink", args: `{"path":"linked.bin"}`})
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			ctx := sendFileTelegramContext("group", -10042, 3)
			if test.name == "terminal" {
				ctx = context.Background()
			}
			if test.name == "missing type" {
				ctx = WithConversationID(context.Background(), -10042)
			}
			result, err := handler(ctx, test.args)
			if err != nil || strings.Contains(result, outside) || strings.Contains(result, string(secret)) {
				t.Fatalf("result = %q, error = %v", result, err)
			}
		})
	}
	if calls != 0 {
		t.Fatalf("invalid send_file calls = %d, want 0", calls)
	}
	if strings.Contains(diagnostic.String(), outside) || strings.Contains(diagnostic.String(), string(secret)) {
		t.Fatalf("diagnostics exposed private data: %q", diagnostic.String())
	}
}

func TestSendFileToolDoesNotReplayUncertainDelivery(t *testing.T) {
	root := setupFileToolWorkspace(t)
	if err := os.WriteFile(filepath.Join(root, "uncertain.bin"), []byte("payload"), 0600); err != nil {
		t.Fatal(err)
	}
	calls := 0
	handler := sendFileToolHandler(t, func(context.Context, int64, int64, string, []byte) error {
		calls++
		return errors.New("private transport detail")
	})
	result, err := handler(sendFileTelegramContext("private", 42, 0), `{"path":"uncertain.bin"}`)
	if err != nil || !strings.Contains(result, "did not confirm delivery") || !strings.Contains(result, "do not retry") || calls != 1 {
		t.Fatalf("uncertain delivery result = %q, error = %v, calls = %d", result, err, calls)
	}
	if strings.Contains(result, "private transport detail") {
		t.Fatalf("tool feedback exposed sender detail: %q", result)
	}
}
