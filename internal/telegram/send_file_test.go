package telegram

import (
	"bytes"
	"context"
	"errors"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
)

func TestSendFileWithUsesExactChatAndForumThread(t *testing.T) {
	paths := Paths{Token: filepath.Join(t.TempDir(), "token")}
	if err := writePrivate(paths.Token, []byte(testToken)); err != nil {
		t.Fatal(err)
	}
	data := []byte{0x50, 0x4b, 0x03, 0x04, 0x00, 0xff, 0x80}
	var requests []struct {
		chatID   string
		threadID string
		filename string
		data     []byte
	}
	client := doerFunc(func(request *http.Request) (*http.Response, error) {
		body, err := io.ReadAll(request.Body)
		if err != nil {
			return nil, err
		}
		mediaType, params, err := mime.ParseMediaType(request.Header.Get("Content-Type"))
		if err != nil || mediaType != "multipart/form-data" || params["boundary"] == "" {
			return nil, errors.New("invalid multipart request")
		}
		reader := multipart.NewReader(bytes.NewReader(body), params["boundary"])
		fields := make(map[string]string)
		var document []byte
		filename := ""
		for {
			part, err := reader.NextPart()
			if errors.Is(err, io.EOF) {
				break
			}
			if err != nil {
				return nil, err
			}
			content, err := io.ReadAll(part)
			if err != nil {
				return nil, err
			}
			if part.FormName() == "document" {
				filename = part.FileName()
				document = content
			} else {
				fields[part.FormName()] = string(content)
			}
		}
		requests = append(requests, struct {
			chatID   string
			threadID string
			filename string
			data     []byte
		}{fields["chat_id"], fields["message_thread_id"], filename, document})
		return response(http.StatusOK, `{"ok":true,"result":{"message_id":1}}`), nil
	})
	for _, test := range []struct {
		name       string
		chatID     int64
		threadID   int64
		wantChat   string
		wantThread string
	}{
		{name: "private", chatID: 42, wantChat: "42"},
		{name: "group", chatID: -10042, wantChat: "-10042"},
		{name: "topic", chatID: -10042, threadID: 19, wantChat: "-10042", wantThread: "19"},
	} {
		t.Run(test.name, func(t *testing.T) {
			if err := sendFileWith(context.Background(), paths, client, "https://telegram.synthetic", test.chatID, test.threadID, "bundle.zip", data); err != nil {
				t.Fatal(err)
			}
			got := requests[len(requests)-1]
			if got.chatID != test.wantChat || got.threadID != test.wantThread || got.filename != "bundle.zip" || !bytes.Equal(got.data, data) {
				t.Fatalf("multipart request = %+v, want chat %q thread %q and exact bytes", got, test.wantChat, test.wantThread)
			}
		})
	}
	if len(requests) != 3 {
		t.Fatalf("Telegram sendDocument calls = %d, want 3", len(requests))
	}
}

func TestSendFileWithErrorsAreSecretSafeAndNeverReplayed(t *testing.T) {
	paths := Paths{Token: filepath.Join(t.TempDir(), "token")}
	if err := writePrivate(paths.Token, []byte(testToken)); err != nil {
		t.Fatal(err)
	}
	privateBytes := []byte("PRIVATE_FILE_CONTENT")
	privateURL := "https://telegram.synthetic/bot" + testToken + "/sendDocument"
	cases := []struct {
		name   string
		client doerFunc
	}{
		{name: "telegram non-ok", client: func(*http.Request) (*http.Response, error) {
			return telegramDiagnosticResponse(http.StatusBadRequest, `{"ok":false,"error_code":400,"description":"private Telegram response"}`), nil
		}},
		{name: "malformed", client: func(*http.Request) (*http.Response, error) {
			return telegramDiagnosticResponse(http.StatusOK, "private malformed response"), nil
		}},
		{name: "missing result", client: func(*http.Request) (*http.Response, error) {
			return telegramDiagnosticResponse(http.StatusOK, `{"ok":true}`), nil
		}},
		{name: "invalid result", client: func(*http.Request) (*http.Response, error) {
			return telegramDiagnosticResponse(http.StatusOK, `{"ok":true,"result":true}`), nil
		}},
		{name: "http error", client: func(*http.Request) (*http.Response, error) {
			return telegramDiagnosticResponse(http.StatusBadGateway, `{"ok":true,"result":true,"description":"private HTTP response"}`), nil
		}},
		{name: "transport error", client: func(*http.Request) (*http.Response, error) {
			return nil, errors.New(`Post "` + privateURL + `": dial tcp telegram.synthetic:443: connection refused token=TELEGRAM_TOKEN`)
		}},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			calls := 0
			client := doerFunc(func(request *http.Request) (*http.Response, error) {
				calls++
				return test.client(request)
			})
			var deliveryErr error
			log := captureTelegramDiagnostics(t, func() {
				deliveryErr = sendFileWith(context.Background(), paths, client, "https://telegram.synthetic", 42, 11, "private.zip", privateBytes)
			})
			if deliveryErr == nil || deliveryErr.Error() != "Telegram file delivery failed; delivery was not confirmed" {
				t.Fatalf("delivery error = %v", deliveryErr)
			}
			if calls != 1 {
				t.Fatalf("sendDocument calls = %d, want one with no replay", calls)
			}
			if !strings.Contains(log, "ERROR operation=telegram.api") || !strings.Contains(log, `method="sendDocument"`) || !strings.Contains(log, `phase="dispatch"`) {
				t.Fatalf("diagnostic = %q, missing bounded send_file failure context", log)
			}
			for _, secret := range []string{testToken, "TELEGRAM_TOKEN", string(privateBytes), "private Telegram response", "private malformed response", privateURL} {
				if strings.Contains(deliveryErr.Error(), secret) || strings.Contains(log, secret) {
					t.Fatalf("secret or private data %q was exposed: error=%q log=%q", secret, deliveryErr.Error(), log)
				}
			}
		})
	}
}

func TestSendFileWithMissingTokenIsSecretSafe(t *testing.T) {
	paths := Paths{Token: filepath.Join(t.TempDir(), "missing-token")}
	var deliveryErr error
	log := captureTelegramDiagnostics(t, func() {
		deliveryErr = sendFileWith(context.Background(), paths, doerFunc(func(*http.Request) (*http.Response, error) {
			return nil, errors.New("transport must not run")
		}), "https://telegram.synthetic", 42, 0, "file.zip", []byte("private"))
	})
	if deliveryErr == nil || deliveryErr.Error() != "Telegram file delivery failed; delivery was not confirmed" {
		t.Fatalf("missing-token error = %v", deliveryErr)
	}
	if strings.Contains(log, paths.Token) || strings.Contains(log, "private") || strings.Contains(log, testToken) {
		t.Fatalf("missing-token diagnostic exposed private data: %q", log)
	}
}

func TestSendFileWithRejectsOversizeBeforeTransport(t *testing.T) {
	paths := Paths{Token: filepath.Join(t.TempDir(), "missing-token")}
	calls := 0
	data := make([]byte, int(telegramSendFileMaxBytes+1))
	var err error
	log := captureTelegramDiagnostics(t, func() {
		err = sendFileWith(context.Background(), paths, doerFunc(func(*http.Request) (*http.Response, error) {
			calls++
			return response(http.StatusOK, `{"ok":true}`), nil
		}), "https://telegram.synthetic", 42, 0, "file.zip", data)
	})
	if !strings.Contains(log, "Telegram send_file exceeds the size limit") || strings.Contains(log, paths.Token) {
		t.Fatalf("diagnostic = %q", log)
	}
	if err == nil || calls != 0 {
		t.Fatalf("oversize result = %v, transport calls = %d", err, calls)
	}
}

func TestSendFileToolFeedbackWithholdsArguments(t *testing.T) {
	feedback := toolFeedbackText("send_file", `{"path":"/private/artefacts/archive.zip","filename":"archive.zip","token":"secret"}`)
	if !strings.Contains(feedback, "send_file") || strings.Contains(feedback, "/private/artefacts") || strings.Contains(feedback, "archive.zip") || strings.Contains(feedback, "secret") {
		t.Fatalf("send_file feedback exposed arguments: %q", feedback)
	}
}

func TestTelegramSendFileSizeBoundaries(t *testing.T) {
	for _, test := range []struct {
		size    int64
		allowed bool
	}{{(8 << 20) + 1, true}, {50_000_000, true}, {50_000_001, false}, {-1, false}} {
		if got := telegramSendFileSizeAllowed(test.size); got != test.allowed {
			t.Errorf("size %d allowed = %v", test.size, got)
		}
	}
}
