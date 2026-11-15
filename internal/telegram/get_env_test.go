package telegram

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"goaster/internal/localenv"
)

func telegramDocumentContent(t *testing.T, contentType string, body []byte) []byte {
	t.Helper()
	mediaType, params, err := mime.ParseMediaType(contentType)
	if err != nil || mediaType != "multipart/form-data" || params["boundary"] == "" {
		t.Fatal("sendDocument used an invalid multipart content type")
	}
	reader := multipart.NewReader(bytes.NewReader(body), params["boundary"])
	for {
		part, err := reader.NextPart()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal("could not read sendDocument multipart body")
		}
		content, err := io.ReadAll(part)
		if err != nil {
			t.Fatal("could not read sendDocument document")
		}
		if part.FormName() == "document" {
			if part.FileName() != telegramEnvironmentFilename {
				t.Fatal("sendDocument used an unexpected filename")
			}
			return content
		}
	}
	t.Fatal("sendDocument did not include a document")
	return nil
}

func TestTelegramGetEnvironmentSendsFileWithoutAgentContext(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	store := localenv.NewStoreAt(filepath.Join(home, ".goaster", "env"))
	if err := store.Save(map[string]string{
		"API_TOKEN":      "synthetic-secret",
		"SERVICE_REGION": "test-region",
	}); err != nil {
		t.Fatal(err)
	}

	var documentBodies [][]byte
	var documentContentTypes []string
	messageCount := 0
	a := adapter{
		token:   testToken,
		pairing: &pairing{userID: 42},
		apiBase: "https://example.invalid",
		client: doerFunc(func(r *http.Request) (*http.Response, error) {
			switch {
			case strings.HasSuffix(r.URL.Path, "/sendDocument"):
				var err error
				documentBody, err := io.ReadAll(r.Body)
				if err != nil {
					t.Fatal(err)
				}
				if !strings.HasPrefix(r.Header.Get("Content-Type"), "multipart/form-data; boundary=") {
					t.Fatalf("sendDocument content type = %q", r.Header.Get("Content-Type"))
				}
				documentBodies = append(documentBodies, documentBody)
				documentContentTypes = append(documentContentTypes, r.Header.Get("Content-Type"))
				return response(http.StatusOK, `{"ok":true,"result":true}`), nil
			case strings.HasSuffix(r.URL.Path, "/sendMessage"):
				messageCount++
				return response(http.StatusOK, `{"ok":true,"result":{}}`), nil
			default:
				t.Fatalf("unexpected Telegram request %s", r.URL.Path)
				return nil, nil
			}
		}),
		agent: doerFunc(func(r *http.Request) (*http.Response, error) {
			body, err := io.ReadAll(r.Body)
			if err != nil {
				t.Fatal(err)
			}
			if bytes.Contains(body, []byte("synthetic-secret")) || bytes.Contains(body, []byte("test-region")) {
				t.Fatal("environment contents entered agent context")
			}
			t.Fatalf("/get-env made an agent request: %s", r.URL.Path)
			return nil, nil
		}),
	}

	a.handle(context.Background(), privateMessage(42, telegramGetEnvironmentCommand))

	if messageCount != 0 {
		t.Fatalf("/get-env sent %d text messages, want only the document", messageCount)
	}
	if len(documentBodies) != 1 || len(documentBodies[0]) == 0 || !bytes.Contains(documentBodies[0], []byte(`name="chat_id"`)) || !bytes.Contains(documentBodies[0], []byte("\r\n\r\n42\r\n")) {
		t.Fatalf("sendDocument did not target the authorized private chat; bodies = %d", len(documentBodies))
	}
	if !bytes.Contains(documentBodies[0], []byte(`filename="goaster.env"`)) {
		t.Fatal("sendDocument did not use the goaster.env filename")
	}
	savedFileBytes, err := os.ReadFile(filepath.Join(home, ".goaster", "env"))
	if err != nil {
		t.Fatal("could not read saved environment file for comparison")
	}
	if !bytes.Equal(telegramDocumentContent(t, documentContentTypes[0], documentBodies[0]), savedFileBytes) {
		t.Fatal("sendDocument did not preserve the saved file bytes")
	}

	if err := os.RemoveAll(filepath.Join(home, ".goaster")); err != nil {
		t.Fatal(err)
	}
	a.handle(context.Background(), privateMessage(42, telegramGetEnvironmentCommand))
	if len(documentBodies) != 2 {
		t.Fatalf("sendDocument calls after missing file = %d, want 2", len(documentBodies))
	}
	missingDocument := documentBodies[1]
	if !bytes.Contains(missingDocument, []byte("\r\n\r\n42\r\n")) {
		t.Fatal("missing environment export did not target the authorized private chat")
	}
	if !bytes.Equal(telegramDocumentContent(t, documentContentTypes[1], missingDocument), []byte("# Add your environment variables below.\n")) {
		t.Fatal("missing environment export did not contain the starter comment")
	}
	if _, err := os.Stat(filepath.Join(home, ".goaster", "env")); !os.IsNotExist(err) {
		t.Fatal("missing environment export wrote a starter file to disk")
	}

	if err := os.MkdirAll(filepath.Join(home, ".goaster"), 0700); err != nil {
		t.Fatal("could not create environment directory for empty-file case")
	}
	emptyFilePath := filepath.Join(home, ".goaster", "env")
	if err := os.WriteFile(emptyFilePath, nil, 0600); err != nil {
		t.Fatal("could not create empty environment file")
	}
	a.handle(context.Background(), privateMessage(42, telegramGetEnvironmentCommand))
	if len(documentBodies) != 3 {
		t.Fatalf("sendDocument calls after zero-byte file = %d, want 3", len(documentBodies))
	}
	if !bytes.Equal(telegramDocumentContent(t, documentContentTypes[2], documentBodies[2]), []byte("# Add your environment variables below.\n")) {
		t.Fatal("zero-byte environment export did not contain the starter comment")
	}
	info, err := os.Stat(emptyFilePath)
	if err != nil || info.Size() != 0 {
		t.Fatal("zero-byte environment export changed the saved file")
	}
	if messageCount != 0 {
		t.Fatalf("environment export sent %d text messages, want only documents", messageCount)
	}
}

func TestTelegramGetEnvironmentCommandNamesAndBotMentions(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	store := localenv.NewStoreAt(filepath.Join(home, ".goaster", "env"))
	if err := store.Save(map[string]string{"KEY": "synthetic-value"}); err != nil {
		t.Fatal(err)
	}

	commands := []string{
		"/get-env",
		"/get-env@goaster_bot",
	}
	documentCount := 0
	a := adapter{
		token:   testToken,
		pairing: &pairing{userID: 42},
		apiBase: "https://example.invalid",
		client: doerFunc(func(r *http.Request) (*http.Response, error) {
			if !strings.HasSuffix(r.URL.Path, "/sendDocument") {
				t.Fatalf("unexpected Telegram request %s", r.URL.Path)
			}
			documentCount++
			return response(http.StatusOK, `{"ok":true,"result":true}`), nil
		}),
		agent: doerFunc(func(*http.Request) (*http.Response, error) {
			t.Fatal("get environment command reached the agent")
			return nil, nil
		}),
	}

	for _, command := range commands {
		a.handle(context.Background(), privateMessage(42, command))
	}
	if documentCount != len(commands) {
		t.Fatalf("sendDocument calls = %d, want one for each valid command form: %#v", documentCount, commands)
	}
}

func TestTelegramRemovedCommandsAreIgnored(t *testing.T) {
	var documentCount int
	var agentCount int
	a := adapter{
		token:   testToken,
		pairing: &pairing{userID: 42},
		apiBase: "https://example.invalid",
		client: doerFunc(func(r *http.Request) (*http.Response, error) {
			if strings.HasSuffix(r.URL.Path, "/sendDocument") {
				documentCount++
				return response(http.StatusOK, `{"ok":true,"result":true}`), nil
			}
			if !strings.HasSuffix(r.URL.Path, "/sendMessage") {
				t.Fatalf("unexpected Telegram request %s", r.URL.Path)
			}
			return response(http.StatusOK, `{"ok":true,"result":{}}`), nil
		}),
		agent: doerFunc(func(*http.Request) (*http.Response, error) {
			agentCount++
			return response(http.StatusOK, `{"type":"final","reply":"ordinary reply"}`+"\n"), nil
		}),
	}

	for _, spelling := range []string{"/get_env", "/get_env@goaster_bot", "/get_migration"} {
		a.handle(context.Background(), privateMessage(42, spelling))
	}
	if documentCount != 0 || agentCount != 0 {
		t.Fatalf("removed command sent %d documents and made %d agent requests", documentCount, agentCount)
	}
}

func TestTelegramGetEnvironmentRequiresAuthorizedPrivateConversation(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	store := localenv.NewStoreAt(filepath.Join(home, ".goaster", "env"))
	if err := store.Save(map[string]string{"KEY": "synthetic-value"}); err != nil {
		t.Fatal(err)
	}

	documentCount := 0
	var messages []struct {
		ChatID int64  `json:"chat_id"`
		Text   string `json:"text"`
	}
	a := adapter{
		token:   testToken,
		pairing: &pairing{userID: 42},
		apiBase: "https://example.invalid",
		client: doerFunc(func(r *http.Request) (*http.Response, error) {
			switch {
			case strings.HasSuffix(r.URL.Path, "/sendDocument"):
				documentCount++
				return response(http.StatusOK, `{"ok":true,"result":true}`), nil
			case strings.HasSuffix(r.URL.Path, "/sendMessage"):
				var payload struct {
					ChatID int64  `json:"chat_id"`
					Text   string `json:"text"`
				}
				if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
					t.Fatal(err)
				}
				messages = append(messages, payload)
				return response(http.StatusOK, `{"ok":true,"result":{}}`), nil
			default:
				t.Fatalf("unexpected Telegram request %s", r.URL.Path)
				return nil, nil
			}
		}),
		agent: doerFunc(func(*http.Request) (*http.Response, error) {
			t.Fatal("/get-env reached the agent")
			return nil, nil
		}),
	}

	unauthorized := privateMessage(99, telegramGetEnvironmentCommand)
	a.handle(context.Background(), unauthorized)
	spoofed := privateMessage(42, telegramGetEnvironmentCommand)
	spoofed.Chat.ID = 99
	a.handle(context.Background(), spoofed)
	a.handle(context.Background(), textMessage(42, -100123, "group", telegramGetEnvironmentCommand))
	a.handle(context.Background(), privateMessage(42, telegramGetEnvironmentCommand))

	if documentCount != 1 {
		t.Fatalf("sendDocument calls = %d, want one authorized private delivery", documentCount)
	}
	if len(messages) != 1 || messages[0].ChatID != -100123 || !strings.Contains(messages[0].Text, "private chat") || strings.Contains(messages[0].Text, "synthetic-value") {
		t.Fatalf("group restriction messages = %#v", messages)
	}
}

func TestTelegramGetEnvironmentDoesNotEchoUnavailableFile(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	path := filepath.Join(home, ".goaster", "env")
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("BROKEN=synthetic-secret\nnot-a-setting\n"), 0600); err != nil {
		t.Fatal(err)
	}

	var sent string
	a := adapter{
		token:   testToken,
		pairing: &pairing{userID: 42},
		apiBase: "https://example.invalid",
		client: doerFunc(func(r *http.Request) (*http.Response, error) {
			if !strings.HasSuffix(r.URL.Path, "/sendMessage") {
				t.Fatalf("unexpected Telegram request %s", r.URL.Path)
			}
			var payload struct {
				Text string `json:"text"`
			}
			if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
				t.Fatal(err)
			}
			sent = payload.Text
			return response(http.StatusOK, `{"ok":true,"result":{}}`), nil
		}),
		agent: doerFunc(func(*http.Request) (*http.Response, error) {
			t.Fatal("unavailable environment reached the agent")
			return nil, nil
		}),
	}

	a.handle(context.Background(), privateMessage(42, telegramGetEnvironmentCommand))

	if sent != "The environment file is unavailable." {
		t.Fatalf("unavailable file response = %q", sent)
	}
	if strings.Contains(sent, "synthetic-secret") {
		t.Fatal("unavailable file response exposed file contents")
	}
}
