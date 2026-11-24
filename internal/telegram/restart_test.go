package telegram

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestRestartOwnerBusyAndOrdering(t *testing.T) {
	var events []string
	a := adapter{pairing: &pairing{userID: 42}, apiBase: "https://example.invalid", controlSecret: "secret"}
	a.client = doerFunc(func(r *http.Request) (*http.Response, error) {
		var body struct {
			Text string `json:"text"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		events = append(events, body.Text)
		return response(200, `{"ok":true,"result":{}}`), nil
	})
	a.agent = doerFunc(func(r *http.Request) (*http.Response, error) {
		if r.URL.Path == "/internal/restart-guard" {
			if r.Header.Get(telegramControlAuthorizationHeader) != "secret" {
				t.Fatal("missing guard authentication")
			}
			events = append(events, "guard")
			resp := response(204, "")
			resp.Header = make(http.Header)
			resp.Header.Set("X-Restart-Guard", "12345678901234567890123456789012")
			return resp, nil
		}
		events = append(events, "health")
		return response(200, "ok\n"), nil
	})
	a.restartService = func(context.Context) error { events = append(events, "systemctl"); return nil }
	a.handle(context.Background(), privateMessage(99, "/restart"))
	if len(events) != 0 {
		t.Fatal(events)
	}
	a.handle(context.Background(), privateMessage(42, "/restart now"))
	if len(events) != 1 {
		t.Fatal(events)
	}
	events = nil
	a.queuedWork = 1
	a.handle(context.Background(), privateMessage(42, "/restart"))
	if len(events) != 1 || events[0] != "Agent is busy. Use /stop for active work and try again when idle." {
		t.Fatal(events)
	}
	events = nil
	a.queuedWork = 0
	a.activeTurns = map[telegramTurnKey][]telegramActiveTurn{{chatID: 99}: {{}}}
	a.handle(context.Background(), privateMessage(42, "/restart"))
	if len(events) != 1 || !strings.Contains(events[0], "busy") {
		t.Fatal(events)
	}
	a.activeTurns = nil
	events = nil
	a.handle(context.Background(), privateMessage(42, "/restart"))
	want := []string{"guard", "Restarting the agent", "guard", "systemctl", "health", "Agent ready", "guard"}
	if !reflect.DeepEqual(events, want) {
		t.Fatalf("got %q want %q", events, want)
	}
}

func TestRestartFailuresDoNotReportReady(t *testing.T) {
	for _, status := range []int{http.StatusConflict, http.StatusUnauthorized, http.StatusNoContent} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			var sent []string
			calls := 0
			a := adapter{controlSecret: "secret", apiBase: "https://example.invalid", client: doerFunc(func(r *http.Request) (*http.Response, error) {
				var body struct {
					Text string `json:"text"`
				}
				_ = json.NewDecoder(r.Body).Decode(&body)
				sent = append(sent, body.Text)
				return response(200, `{"ok":true,"result":{}}`), nil
			}), agent: doerFunc(func(r *http.Request) (*http.Response, error) {
				if status == http.StatusNoContent {
					resp := response(204, "")
					resp.Header = make(http.Header)
					resp.Header.Set("X-Restart-Guard", "12345678901234567890123456789012")
					return resp, nil
				}
				return response(status, ""), nil
			}), restartService: func(context.Context) error { calls++; return errors.New("failed") }}
			a.restart(context.Background(), 42)
			if (status == http.StatusNoContent && calls != 1) || (status != http.StatusNoContent && calls != 0) {
				t.Fatal(calls)
			}
			for _, text := range sent {
				if text == "Agent ready" {
					t.Fatal(sent)
				}
			}
		})
	}
}

func TestRestartRejectsConcurrentDuplicate(t *testing.T) {
	entered := make(chan struct{})
	release := make(chan struct{})
	finished := make(chan struct{})
	var mu sync.Mutex
	var messages []string
	calls := 0
	a := adapter{controlSecret: "secret", apiBase: "https://example.invalid"}
	a.client = doerFunc(func(r *http.Request) (*http.Response, error) {
		var body struct {
			Text string `json:"text"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			return nil, err
		}
		mu.Lock()
		messages = append(messages, body.Text)
		mu.Unlock()
		return response(200, `{"ok":true,"result":{}}`), nil
	})
	a.agent = doerFunc(func(r *http.Request) (*http.Response, error) {
		if r.URL.Path == "/internal/restart-guard" {
			resp := response(204, "")
			resp.Header = make(http.Header)
			resp.Header.Set("X-Restart-Guard", "12345678901234567890123456789012")
			return resp, nil
		}
		return response(200, "ok\n"), nil
	})
	a.restartService = func(context.Context) error { calls++; close(entered); <-release; return nil }
	go func() { defer close(finished); a.restart(context.Background(), 42) }()
	<-entered
	a.restart(context.Background(), 42)
	close(release)
	<-finished
	if calls != 1 {
		t.Fatalf("restart calls = %d", calls)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(messages) != 3 || messages[1] != "Agent is busy. Use /stop for active work and try again when idle." {
		t.Fatalf("messages = %q", messages)
	}
}

func TestRestartReleasesGuardOnAckAndCommandFailure(t *testing.T) {
	for _, ackFails := range []bool{true, false} {
		t.Run(fmt.Sprint(ackFails), func(t *testing.T) {
			releases, calls := 0, 0
			a := adapter{controlSecret: "secret", apiBase: "https://example.invalid"}
			a.client = doerFunc(func(r *http.Request) (*http.Response, error) {
				if ackFails {
					return nil, errors.New("ack failed")
				}
				return response(200, `{"ok":true,"result":{}}`), nil
			})
			a.agent = doerFunc(func(r *http.Request) (*http.Response, error) {
				if r.Method == http.MethodDelete {
					releases++
					if r.Header.Get("X-Restart-Guard") != "7" {
						t.Fatal("missing guard ID")
					}
					resp := response(204, "")
					resp.Header = make(http.Header)
					resp.Header.Set("X-Restart-Guard", "12345678901234567890123456789012")
					return resp, nil
				}
				resp := response(204, "")
				resp.Header = make(http.Header)
				resp.Header.Set("X-Restart-Guard", "7")
				return resp, nil
			})
			a.restartService = func(context.Context) error { calls++; return predispatchError{errors.New("failed")} }
			a.restart(context.Background(), 42)
			if releases != 1 || (ackFails && calls != 0) || (!ackFails && calls != 1) {
				t.Fatalf("releases %d calls %d", releases, calls)
			}
		})
	}
}

func TestRestartHealthTimeout(t *testing.T) {
	oldTimeout, oldInterval := restartReadyTimeout, restartReadyInterval
	restartReadyTimeout, restartReadyInterval = 10*time.Millisecond, time.Millisecond
	defer func() { restartReadyTimeout, restartReadyInterval = oldTimeout, oldInterval }()
	var sent []string
	a := adapter{controlSecret: "secret", apiBase: "https://example.invalid", restartService: func(context.Context) error { return nil }}
	a.client = doerFunc(func(r *http.Request) (*http.Response, error) {
		var body struct {
			Text string `json:"text"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		sent = append(sent, body.Text)
		return response(200, `{"ok":true,"result":{}}`), nil
	})
	a.agent = doerFunc(func(r *http.Request) (*http.Response, error) {
		if r.URL.Path == "/healthz" {
			return response(503, ""), nil
		}
		resp := response(204, "")
		resp.Header = make(http.Header)
		resp.Header.Set("X-Restart-Guard", "12345678901234567890123456789012")
		return resp, nil
	})
	a.restart(context.Background(), 42)
	if len(sent) != 2 || sent[1] != "Agent restarted but is not ready. Check the service status." {
		t.Fatal(sent)
	}
}

func TestRestartUnavailable(t *testing.T) {
	old := systemdAvailable
	systemdAvailable = func() bool { return false }
	defer func() { systemdAvailable = old }()
	var sent []string
	a := adapter{apiBase: "https://example.invalid", client: doerFunc(func(r *http.Request) (*http.Response, error) {
		var body struct {
			Text string `json:"text"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		sent = append(sent, body.Text)
		return response(200, `{"ok":true,"result":{}}`), nil
	})}
	a.restart(context.Background(), 42)
	if len(sent) != 1 || sent[0] != "Restart is unavailable: systemd is not running." {
		t.Fatal(sent)
	}
	a.restartService = func(context.Context) error { t.Fatal("unexpected restart"); return nil }
	a.restart(context.Background(), 42)
	if len(sent) != 2 || sent[1] != "Restart unavailable: could not check agent activity." {
		t.Fatal(sent)
	}
}

func TestRestartPollingSkipsStaleBeyondFirstPageAndDuplicate(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	finished := make(chan struct{})
	polls, calls := 0, 0
	a := adapter{botUsername: "ExampleBot", pairing: &pairing{userID: 42}, apiBase: "https://example.invalid", controlSecret: "secret"}
	a.restartService = func(context.Context) error { calls++; return nil }
	a.agent = doerFunc(func(r *http.Request) (*http.Response, error) {
		if r.URL.Path == "/internal/restart-guard" {
			resp := response(204, "")
			resp.Header = make(http.Header)
			resp.Header.Set("X-Restart-Guard", "12345678901234567890123456789012")
			return resp, nil
		}
		return response(200, "ok\n"), nil
	})
	a.client = doerFunc(func(r *http.Request) (*http.Response, error) {
		if strings.HasSuffix(r.URL.Path, "/setMyCommands") {
			return response(200, `{"ok":true,"result":true}`), nil
		}
		if strings.HasSuffix(r.URL.Path, "/sendMessage") {
			var body struct {
				Text string `json:"text"`
			}
			_ = json.NewDecoder(r.Body).Decode(&body)
			if body.Text == "Agent ready" {
				close(finished)
			}
			return response(200, `{"ok":true,"result":{}}`), nil
		}
		polls++
		switch polls {
		case 1:
			var updates []map[string]any
			for i := 1; i <= 100; i++ {
				if i == 1 {
					updates = append(updates, map[string]any{"update_id": i, "message": map[string]any{"date": time.Now().Add(time.Second).Unix(), "text": "/restart@ExampleBot", "from": map[string]any{"id": 42}, "chat": map[string]any{"id": 42, "type": "private"}}})
				} else {
					updates = append(updates, map[string]any{"update_id": i})
				}
			}
			data, _ := json.Marshal(map[string]any{"ok": true, "result": updates})
			return response(200, string(data)), nil
		case 2:
			return response(200, fmt.Sprintf(`{"ok":true,"result":[{"update_id":101,"message":{"date":%d,"text":"/restart@ExampleBot","from":{"id":42},"chat":{"id":42,"type":"private"}}}]}`, time.Now().Add(-time.Hour).Unix())), nil
		case 3:
			return response(200, fmt.Sprintf(`{"ok":true,"result":[{"update_id":102,"message":{"date":%d,"text":"/restart@ExampleBot","from":{"id":42},"chat":{"id":42,"type":"private"}}},{"update_id":103,"message":{"date":%d,"text":"/restart@ExampleBot","from":{"id":42},"chat":{"id":42,"type":"private"}}}]}`, time.Now().Add(time.Second).Unix(), time.Now().Add(time.Second).Unix())), nil
		default:
			<-finished
			cancel()
			return nil, context.Canceled
		}
	})
	if err := a.run(ctx, nil); err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Fatalf("restart calls = %d", calls)
	}
}

func TestRestartUncertainBlocksFurtherDispatch(t *testing.T) {
	var sent []string
	calls, releases := 0, 0
	a := adapter{controlSecret: "secret", apiBase: "https://example.invalid"}
	a.client = doerFunc(func(r *http.Request) (*http.Response, error) {
		var body struct {
			Text string `json:"text"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		sent = append(sent, body.Text)
		return response(200, `{"ok":true,"result":{}}`), nil
	})
	a.agent = doerFunc(func(r *http.Request) (*http.Response, error) {
		if r.Method == http.MethodDelete {
			releases++
		}
		resp := response(204, "")
		resp.Header = make(http.Header)
		resp.Header.Set("X-Restart-Guard", "12345678901234567890123456789012")
		return resp, nil
	})
	a.restartService = func(context.Context) error { calls++; return context.DeadlineExceeded }
	a.restart(context.Background(), 42)
	a.restart(context.Background(), 42)
	if calls != 1 || releases != 0 || !a.restarting || len(sent) != 3 || !strings.Contains(sent[1], "Manually verify") || !strings.Contains(sent[2], "busy") {
		t.Fatalf("calls=%d releases=%d blocked=%v messages=%q", calls, releases, a.restarting, sent)
	}
}

func TestRestartFailureDiagnostics(t *testing.T) {
	for _, tc := range []struct {
		name, method, reason, status, id, detail string
		nilResponse, transport                   bool
	}{
		{"guard absent", http.MethodPost, "http_status", "503", "", "absent", false, false},
		{"guard malicious", http.MethodPost, "http_status", "503", "token=malicious-secret\nforged", "unrecognized", false, false},
		{"guard valid", http.MethodPost, "http_status", "503", "req_guard123", "present", false, false},
		{"guard transport", http.MethodPost, "transport_failure", "absent", "", "absent", false, true},
		{"commit HTTP", http.MethodPut, "http_status", "503", "req_commit123", "present", false, false},
		{"commit malicious", http.MethodPut, "http_status", "503", "token=malicious-secret\nforged", "unrecognized", false, false},
		{"commit missing", http.MethodPut, "missing_response", "absent", "", "absent", true, false},
		{"release HTTP", http.MethodDelete, "http_status", "503", "req_release123", "present", false, false},
		{"release malicious", http.MethodDelete, "http_status", "503", "token=malicious-secret\nforged", "unrecognized", false, false},
		{"release missing", http.MethodDelete, "missing_response", "absent", "", "absent", true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a := adapter{controlSecret: "secret", apiBase: "https://example.invalid", restartService: func(context.Context) error { return predispatchError{errors.New("password=malicious-secret\nforged")} }}
			a.client = doerFunc(func(*http.Request) (*http.Response, error) { return response(200, `{"ok":true,"result":{}}`), nil })
			a.agent = doerFunc(func(r *http.Request) (*http.Response, error) {
				if r.Method == tc.method {
					if tc.transport {
						return nil, errors.New("transport failed")
					}
					if tc.nilResponse {
						return nil, nil
					}
					resp := response(503, "")
					resp.Header = make(http.Header)
					resp.Header.Set("X-Request-ID", tc.id)
					return resp, nil
				}
				resp := response(204, "")
				resp.Header = make(http.Header)
				resp.Header.Set("X-Restart-Guard", "guard")
				return resp, nil
			})
			logs := captureTelegramDiagnostics(t, func() { a.restart(context.Background(), 42) })
			if !strings.Contains(logs, "restart_reason="+tc.reason) || !strings.Contains(logs, "status=\""+tc.status+"\"") || !strings.Contains(logs, "request_id_detail=\""+tc.detail+"\"") || strings.Contains(logs, "malicious-secret") || strings.Contains(logs, "forged") {
				t.Fatalf("diagnostic: %s", logs)
			}
			if tc.detail == "present" {
				if !strings.Contains(logs, "request_id=\""+tc.id+"\"") {
					t.Fatalf("missing request ID: %s", logs)
				}
			} else if strings.Contains(logs, "request_id=\"") {
				t.Fatalf("unexpected request ID: %s", logs)
			}
		})
	}
}

func TestRestartSendFailureLoggedOnce(t *testing.T) {
	a := adapter{controlSecret: "secret", apiBase: "https://example.invalid", restartService: func(context.Context) error { t.Fatal("dispatched"); return nil }}
	a.client = doerFunc(func(*http.Request) (*http.Response, error) { return nil, errors.New("token=malicious-secret\nforged") })
	a.agent = doerFunc(func(r *http.Request) (*http.Response, error) {
		resp := response(204, "")
		resp.Header = make(http.Header)
		resp.Header.Set("X-Restart-Guard", "guard")
		return resp, nil
	})
	logs := captureTelegramDiagnostics(t, func() { a.restart(context.Background(), 42) })
	if strings.Count(logs, "ERROR ") != 1 || strings.Contains(logs, "malicious-secret") || strings.Contains(logs, "forged") {
		t.Fatalf("diagnostics: %s", logs)
	}
}

func TestRestartTimeoutLaterCompletionKeepsAdmissionClosed(t *testing.T) {
	completed := make(chan struct{})
	finish := make(chan struct{})
	calls, releases, work := 0, 0, 0
	a := adapter{pairing: &pairing{userID: 42}, controlSecret: "secret", apiBase: "https://example.invalid"}
	a.client = doerFunc(func(*http.Request) (*http.Response, error) { return response(200, `{"ok":true,"result":{}}`), nil })
	a.agent = doerFunc(func(r *http.Request) (*http.Response, error) {
		if r.Method == http.MethodDelete {
			releases++
		}
		if r.URL.Path == "/chat" {
			work++
		}
		resp := response(204, "")
		resp.Header = make(http.Header)
		resp.Header.Set("X-Restart-Guard", "guard")
		return resp, nil
	})
	a.restartService = func(context.Context) error {
		calls++
		go func() { <-finish; close(completed) }()
		return context.DeadlineExceeded
	}
	a.restart(context.Background(), 42)
	close(finish)
	<-completed
	a.restart(context.Background(), 42)
	a.handle(context.Background(), privateMessage(42, "new work"))
	if calls != 1 || releases != 0 || work != 0 || !a.restarting {
		t.Fatalf("calls=%d releases=%d work=%d blocked=%v", calls, releases, work, a.restarting)
	}
}

func TestSystemdRestartPreDispatchFailures(t *testing.T) {
	old := systemdCheck
	defer func() { systemdCheck = old }()
	cause := errors.New("stat failed")
	systemdCheck = func() error { return cause }
	err := systemdRestart(context.Background())
	var before predispatchError
	if !errors.As(err, &before) || !errors.Is(err, cause) {
		t.Fatalf("stat error: %v", err)
	}
	systemdCheck = func() error { return nil }
	t.Setenv("PATH", t.TempDir())
	err = systemdRestart(context.Background())
	if !errors.As(err, &before) {
		t.Fatalf("start error: %v", err)
	}
}

func TestRestartCommandFailureReasons(t *testing.T) {
	for _, tc := range []struct {
		name, reason string
		err          error
		releases     int
	}{
		{"before dispatch", "predispatch_failure", predispatchError{errors.New("token=malicious-secret\nforged")}, 1},
		{"uncertain completion", "completion_uncertain", errors.New("token=malicious-secret\nforged"), 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			releases := 0
			a := adapter{controlSecret: "secret", apiBase: "https://example.invalid", restartService: func(context.Context) error { return tc.err }}
			a.client = doerFunc(func(*http.Request) (*http.Response, error) { return response(200, `{"ok":true,"result":{}}`), nil })
			a.agent = doerFunc(func(r *http.Request) (*http.Response, error) {
				if r.Method == http.MethodDelete {
					releases++
				}
				resp := response(204, "")
				resp.Header = make(http.Header)
				resp.Header.Set("X-Restart-Guard", "guard")
				return resp, nil
			})
			logs := captureTelegramDiagnostics(t, func() { a.restart(context.Background(), 42) })
			if releases != tc.releases || !strings.Contains(logs, "restart_reason="+tc.reason) || strings.Contains(logs, "malicious-secret") || strings.Contains(logs, "forged") {
				t.Fatalf("releases=%d diagnostic=%s", releases, logs)
			}
		})
	}
}
