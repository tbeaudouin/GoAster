package telegram

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestVoiceNoteRoutesTranscriptToAgent(t *testing.T) {
	var forwarded struct {
		Message  string `json:"message"`
		ChatID   int64  `json:"chat_id"`
		ThreadID int64  `json:"thread_id"`
	}
	var sent []string
	a := &adapter{token: testToken, pairing: &pairing{userID: 42}, apiBase: "https://example.invalid",
		voiceKey: func() (string, error) { return "test-key", nil },
		client: doerFunc(func(r *http.Request) (*http.Response, error) {
			switch {
			case strings.HasSuffix(r.URL.Path, "/sendChatAction"):
				return response(200, `{"ok":true,"result":true}`), nil
			case strings.HasSuffix(r.URL.Path, "/getFile"):
				return response(200, `{"ok":true,"result":{"file_size":5,"file_path":"voice/file.ogg"}}`), nil
			case strings.HasPrefix(r.URL.Path, "/file/bot"):
				return response(200, "audio"), nil
			case strings.HasSuffix(r.URL.Path, "/sendMessage"):
				var p struct {
					Text     string `json:"text"`
					ThreadID int64  `json:"message_thread_id"`
				}
				if err := json.NewDecoder(r.Body).Decode(&p); err != nil {
					t.Fatal(err)
				}
				if p.ThreadID != 7 {
					t.Errorf("thread %d", p.ThreadID)
				}
				sent = append(sent, p.Text)
				return response(200, `{"ok":true,"result":{}}`), nil
			default:
				t.Fatalf("unexpected telegram request %s", r.URL.Path)
				return nil, nil
			}
		}),
		voiceClient: doerFunc(func(r *http.Request) (*http.Response, error) {
			if r.URL.String() != groqTranscriptionURL || r.Header.Get("Authorization") != "Bearer test-key" {
				t.Fatal("wrong provider request")
			}
			if err := r.ParseMultipartForm(1024); err != nil {
				t.Fatal(err)
			}
			if r.FormValue("model") != "whisper-large-v3-turbo" {
				t.Fatal("wrong model")
			}
			f, _, err := r.FormFile("file")
			if err != nil {
				t.Fatal(err)
			}
			defer f.Close()
			b, _ := io.ReadAll(f)
			if string(b) != "audio" {
				t.Fatal("wrong audio")
			}
			return response(200, `{"text":"bonjour à tous"}`), nil
		}),
		agent: doerFunc(func(r *http.Request) (*http.Response, error) {
			if err := json.NewDecoder(r.Body).Decode(&forwarded); err != nil {
				t.Fatal(err)
			}
			return response(200, `{"type":"final","reply":"hello back"}`+"\n"), nil
		}),
	}
	var m message
	if err := json.Unmarshal([]byte(`{"voice":{"file_id":"v","file_size":5},"message_thread_id":7,"chat":{"id":42,"type":"private"},"from":{"id":42}}`), &m); err != nil {
		t.Fatal(err)
	}
	a.handle(context.Background(), &m)
	if forwarded.Message != "bonjour à tous" || forwarded.ChatID != 42 || forwarded.ThreadID != 7 || (len(sent) != 2 || sent[0] != "Transcript: bonjour à tous" || sent[1] != "hello back") {
		t.Fatalf("route failed: %+v, replies %q", forwarded, sent)
	}
}

func TestVoiceUnauthorizedAndInvalidDoNotReachProvider(t *testing.T) {
	calls := 0
	a := &adapter{token: testToken, pairing: &pairing{userID: 42}, voiceKey: func() (string, error) { calls++; return "key", nil }, client: doerFunc(func(*http.Request) (*http.Response, error) {
		calls++
		return response(200, `{"ok":true,"result":{}}`), nil
	})}
	var m message
	_ = json.Unmarshal([]byte(`{"voice":{"file_id":"v"},"chat":{"id":99,"type":"private"},"from":{"id":99}}`), &m)
	a.handle(context.Background(), &m)
	if calls != 0 {
		t.Fatal("unauthorized request reached services")
	}
	if _, err := a.transcribeVoice(context.Background(), &voiceNote{FileID: "v", FileSize: voiceMaxBytes + 1}); err == nil || calls != 0 {
		t.Fatal("oversized audio accepted")
	}
}

func TestVoiceProviderFailureDoesNotExposeResponse(t *testing.T) {
	a := &adapter{token: testToken, apiBase: "https://example.invalid", voiceKey: func() (string, error) { return "test-key", nil }, client: doerFunc(func(r *http.Request) (*http.Response, error) {
		if strings.HasSuffix(r.URL.Path, "/getFile") {
			return response(200, `{"ok":true,"result":{"file_path":"voice/a.ogg"}}`), nil
		}
		return response(200, "audio"), nil
	}), voiceClient: doerFunc(func(*http.Request) (*http.Response, error) { return response(401, "sensitive provider details"), nil })}
	_, err := a.transcribeVoice(context.Background(), &voiceNote{FileID: "v"})
	if err == nil || strings.Contains(err.Error(), "sensitive") || strings.Contains(err.Error(), "test-key") {
		t.Fatal("provider failure not safely handled")
	}
}

func TestVoiceDurationBoundBeforeNetwork(t *testing.T) {
	calls := 0
	a := &adapter{voiceKey: func() (string, error) { calls++; return "", nil }}
	for _, duration := range []int64{-1, voiceMaxDurationSeconds + 1} {
		if _, err := a.transcribeVoice(context.Background(), &voiceNote{FileID: "v", Duration: duration}); err == nil {
			t.Errorf("accepted duration %d", duration)
		}
	}
	if calls != 0 {
		t.Fatal("invalid duration reached key or network")
	}
}

func TestVoiceProviderErrorsAndCancellation(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status int
		body   string
	}{
		{"rate limited", 429, "private provider error"},
		{"server error", 500, "private provider error"},
		{"empty transcript", 200, `{"text":" "}`},
		{"invalid response", 200, `not json`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			a := &adapter{token: testToken, apiBase: "https://example.invalid", voiceKey: func() (string, error) { return "test-key", nil },
				client: doerFunc(func(r *http.Request) (*http.Response, error) {
					if strings.HasSuffix(r.URL.Path, "/getFile") {
						return response(200, `{"ok":true,"result":{"file_path":"voice/a.ogg"}}`), nil
					}
					return response(200, "audio"), nil
				}),
				voiceClient: doerFunc(func(r *http.Request) (*http.Response, error) { calls++; return response(tc.status, tc.body), nil }),
			}
			text, err := a.transcribeVoice(context.Background(), &voiceNote{FileID: "v"})
			if err == nil || text != "" || calls != 1 || strings.Contains(err.Error(), "private") {
				t.Fatal("unsafe failure or retry")
			}
		})
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	a := &adapter{token: testToken, apiBase: "https://example.invalid", voiceKey: func() (string, error) { return "key", nil }, client: doerFunc(func(r *http.Request) (*http.Response, error) { return nil, r.Context().Err() })}
	if text, err := a.transcribeVoice(ctx, &voiceNote{FileID: "v"}); err == nil || text != "" {
		t.Fatal("cancelled request succeeded")
	}
}

func TestVoiceFailureDoesNotDispatchToAgent(t *testing.T) {
	agentCalls := 0
	var sent string
	a := &adapter{token: testToken, pairing: &pairing{userID: 42}, apiBase: "https://example.invalid",
		voiceKey: func() (string, error) { return "", nil },
		client: doerFunc(func(r *http.Request) (*http.Response, error) {
			if strings.HasSuffix(r.URL.Path, "/sendMessage") {
				var p struct {
					Text string `json:"text"`
				}
				if err := json.NewDecoder(r.Body).Decode(&p); err != nil {
					t.Fatal(err)
				}
				sent = p.Text
			}
			return response(200, `{"ok":true,"result":{}}`), nil
		}),
		agent: doerFunc(func(*http.Request) (*http.Response, error) { agentCalls++; return response(200, ""), nil }),
	}
	var m message
	if err := json.Unmarshal([]byte(`{"voice":{"file_id":"v"},"chat":{"id":42,"type":"private"},"from":{"id":42}}`), &m); err != nil {
		t.Fatal(err)
	}
	a.handle(context.Background(), &m)
	if agentCalls != 0 || !strings.Contains(sent, "GROQ_API_KEY is missing") {
		t.Fatal("failure dispatched or not reported")
	}
}

func TestVoiceTranscriptDeliveryFailureStopsAgent(t *testing.T) {
	calls := 0
	a := &adapter{token: testToken, pairing: &pairing{userID: 42}, apiBase: "https://example.invalid", voiceKey: func() (string, error) { return "test-key", nil }}
	a.client = doerFunc(func(r *http.Request) (*http.Response, error) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/getFile"):
			return response(200, `{"ok":true,"result":{"file_path":"voice/a.ogg"}}`), nil
		case strings.HasPrefix(r.URL.Path, "/file/bot"):
			return response(200, "audio"), nil
		case strings.HasSuffix(r.URL.Path, "/sendMessage"):
			return response(500, `{"ok":false}`), nil
		default:
			return response(200, `{"ok":true,"result":true}`), nil
		}
	})
	a.voiceClient = doerFunc(func(*http.Request) (*http.Response, error) {
		return response(200, `{"text":"voice text"}`), nil
	})
	a.agent = doerFunc(func(*http.Request) (*http.Response, error) {
		calls++
		return response(200, ""), nil
	})
	var m message
	if err := json.Unmarshal([]byte(`{"voice":{"file_id":"v"},"chat":{"id":42,"type":"private"},"from":{"id":42}}`), &m); err != nil {
		t.Fatal(err)
	}
	a.handle(context.Background(), &m)
	if calls != 0 {
		t.Fatal("agent called despite failed transcript delivery")
	}
}

func TestVoiceMissingKeyAndKeyStoreFailure(t *testing.T) {
	for _, tc := range []struct {
		name    string
		key     string
		keyErr  error
		missing bool
	}{
		{"empty", "", nil, true},
		{"whitespace", "  ", nil, true},
		{"store failure", "", errors.New("private store error"), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			a := &adapter{
				voiceKey:    func() (string, error) { return tc.key, tc.keyErr },
				client:      doerFunc(func(*http.Request) (*http.Response, error) { calls++; return nil, nil }),
				voiceClient: doerFunc(func(*http.Request) (*http.Response, error) { calls++; return nil, nil }),
			}
			_, err := a.transcribeVoice(context.Background(), &voiceNote{FileID: "v"})
			if err == nil || errors.Is(err, errMissingGroqKey) != tc.missing || strings.Contains(err.Error(), "private") || calls != 0 {
				t.Fatal("key failure was incorrectly classified or reached the network")
			}
		})
	}
}
