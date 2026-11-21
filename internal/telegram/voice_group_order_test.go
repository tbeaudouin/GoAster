package telegram

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestVoiceGroupTopicAndUnauthorizedSender(t *testing.T) {
	for _, tc := range []struct {
		name, chatType string
		sender         int64
		allowed        bool
	}{
		{"group", "group", 42, true}, {"supergroup", "supergroup", 42, true}, {"unauthorized", "supergroup", 99, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls, agentCalls := 0, 0
			var forwarded struct {
				Message  string `json:"message"`
				ChatID   int64  `json:"chat_id"`
				ThreadID int64  `json:"thread_id"`
			}
			var replyChat, replyThread int64
			a := &adapter{token: testToken, pairing: &pairing{userID: 42}, apiBase: "https://example.invalid", voiceKey: func() (string, error) { calls++; return "test-key", nil }}
			a.client = doerFunc(func(r *http.Request) (*http.Response, error) {
				calls++
				switch {
				case strings.HasSuffix(r.URL.Path, "/getFile"):
					return response(200, `{"ok":true,"result":{"file_path":"voice/a.ogg"}}`), nil
				case strings.HasPrefix(r.URL.Path, "/file/bot"):
					return response(200, "audio"), nil
				case strings.HasSuffix(r.URL.Path, "/sendMessage"):
					var p struct {
						Chat   int64 `json:"chat_id"`
						Thread int64 `json:"message_thread_id"`
					}
					if err := json.NewDecoder(r.Body).Decode(&p); err != nil {
						t.Fatal(err)
					}
					replyChat, replyThread = p.Chat, p.Thread
				}
				return response(200, `{"ok":true,"result":{}}`), nil
			})
			a.voiceClient = doerFunc(func(*http.Request) (*http.Response, error) {
				calls++
				return response(200, `{"text":"voice text"}`), nil
			})
			a.agent = doerFunc(func(r *http.Request) (*http.Response, error) {
				agentCalls++
				if err := json.NewDecoder(r.Body).Decode(&forwarded); err != nil {
					t.Fatal(err)
				}
				return response(200, "{\"type\":\"final\",\"reply\":\"ok\"}\n"), nil
			})
			var m message
			raw := `{"voice":{"file_id":"v"},"message_thread_id":7,"chat":{"id":-100,"type":"` + tc.chatType + `"},"from":{"id":` + map[bool]string{true: "42", false: "99"}[tc.allowed] + `}}`
			if err := json.Unmarshal([]byte(raw), &m); err != nil {
				t.Fatal(err)
			}
			a.handle(context.Background(), &m)
			if !tc.allowed {
				if calls != 0 || agentCalls != 0 {
					t.Fatal("unauthorized sender reached services")
				}
				return
			}
			if agentCalls != 1 || forwarded.Message != "voice text" || forwarded.ChatID != -100 || forwarded.ThreadID != 7 || replyChat != -100 || replyThread != 7 {
				t.Fatalf("incorrect group route: agent calls %d, forwarded %+v, reply %d/%d", agentCalls, forwarded, replyChat, replyThread)
			}
		})
	}
}

func TestVoicePollingPreservesChatOrdering(t *testing.T) {
	paths := testPaths(t)
	setupTest(t, paths)
	paired, err := loadPairing(paths)
	if err != nil {
		t.Fatal(err)
	}
	paired.userID = 42
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	entered := make(chan struct{})
	release := make(chan struct{})
	forwarded := make(chan string, 2)
	a := adapter{token: testToken, pairing: paired, apiBase: "https://example.invalid", voiceKey: func() (string, error) { return "test-key", nil }}
	a.voiceClient = doerFunc(func(r *http.Request) (*http.Response, error) {
		close(entered)
		select {
		case <-release:
		case <-r.Context().Done():
			return nil, r.Context().Err()
		}
		return response(200, `{"text":"first voice"}`), nil
	})
	a.agent = doerFunc(func(r *http.Request) (*http.Response, error) {
		if r.URL.Path == "/livez" {
			return response(200, "ok\n"), nil
		}
		var p struct {
			Message string `json:"message"`
		}
		if err := json.NewDecoder(r.Body).Decode(&p); err != nil {
			return nil, err
		}
		forwarded <- p.Message
		return response(200, "{\"type\":\"final\",\"reply\":\"ok\"}\n"), nil
	})
	a.client = doerFunc(func(r *http.Request) (*http.Response, error) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/getUpdates"):
			var p struct {
				Offset int64 `json:"offset"`
			}
			_ = json.NewDecoder(r.Body).Decode(&p)
			if p.Offset == 0 {
				return response(200, `{"ok":true,"result":[{"update_id":1,"message":{"voice":{"file_id":"v"},"message_thread_id":7,"from":{"id":42},"chat":{"id":-100,"type":"supergroup"}}},{"update_id":2,"message":{"text":"second text","message_thread_id":8,"from":{"id":42},"chat":{"id":-100,"type":"supergroup"}}}]}`), nil
			}
			<-r.Context().Done()
			return nil, r.Context().Err()
		case strings.HasSuffix(r.URL.Path, "/getFile"):
			return response(200, `{"ok":true,"result":{"file_path":"voice/a.ogg"}}`), nil
		case strings.HasPrefix(r.URL.Path, "/file/bot"):
			return response(200, "audio"), nil
		}
		return response(200, `{"ok":true,"result":{}}`), nil
	})
	done := make(chan error, 1)
	go func() { done <- a.run(ctx, nil) }()
	defer func() {
		cancel()
		select {
		case <-done:
		case <-time.After(3 * time.Second):
			t.Error("polling did not stop")
		}
	}()
	select {
	case <-entered:
	case <-time.After(3 * time.Second):
		t.Fatal("voice transcription did not start")
	}
	select {
	case got := <-forwarded:
		t.Fatalf("message forwarded before voice completed: %q", got)
	case <-time.After(150 * time.Millisecond):
	}
	close(release)
	for _, want := range []string{"first voice", "second text"} {
		select {
		case got := <-forwarded:
			if got != want {
				t.Fatalf("got %q, want %q", got, want)
			}
		case <-time.After(3 * time.Second):
			t.Fatalf("timed out waiting for %q", want)
		}
	}
}
