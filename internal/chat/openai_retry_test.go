package chat

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

type temporaryCodexNetworkError struct{}

func (temporaryCodexNetworkError) Error() string   { return "temporary network detail must stay private" }
func (temporaryCodexNetworkError) Timeout() bool   { return false }
func (temporaryCodexNetworkError) Temporary() bool { return true }

type failedCodexStreamReader struct{}

func (failedCodexStreamReader) Read([]byte) (int, error) {
	return 0, errors.New("private stream detail")
}
func (failedCodexStreamReader) Close() error { return nil }

func requestCodexWithoutTools(ctx context.Context, provider *CodexProvider) error {
	_, _, err := provider.requestCodex(ctx, Credential{AccessToken: "private-access-token"}, "gpt-test", nil, false)
	return err
}

func TestCodexCredentialLookupFailureDoesNotClaimAuthentication(t *testing.T) {
	credentialPath := t.TempDir()
	if err := os.Chmod(credentialPath, 0600); err != nil {
		t.Fatal(err)
	}
	provider := NewCodexProvider(CredentialStore{Path: credentialPath})
	var requestErr error
	log := captureChatDiagnostics(t, func() {
		_, requestErr = provider.Reply(context.Background(), DefaultModel, "private prompt")
	})
	if requestErr == nil || errors.Is(requestErr, ErrLoginRequired) || errors.Is(requestErr, errRequestAuthentication) {
		t.Fatalf("credential lookup error = %v, want a non-authentication failure", requestErr)
	}
	if diagnostic := requestFailureDiagnostic(requestErr); strings.Contains(diagnostic, requestFailureAuthentication) {
		t.Fatalf("credential lookup diagnostic misdiagnosed authentication: %q", diagnostic)
	}
	if !strings.Contains(log, "ERROR operation=chat.provider") || strings.Contains(log, "private prompt") || strings.Contains(log, credentialPath) {
		t.Fatalf("credential lookup log = %q", log)
	}
}

func TestCodexTransientRetrySuccessDiagnosticsAreSafe(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if calls.Add(1) == 1 {
			writer.Header().Set("X-Request-ID", "req-retry-first")
			writer.WriteHeader(http.StatusServiceUnavailable)
			_, _ = io.WriteString(writer, `{"error":{"type":"server_error","code":"overloaded","message":"private provider body and prompt"}}`)
			return
		}
		_, _ = io.WriteString(writer, `{"output_text":"retried answer"}`)
	}))
	defer server.Close()

	provider := runtimeTestProvider(t, server.Client(), server.URL+"/responses")
	var reply string
	var requestErr error
	log := captureChatDiagnostics(t, func() {
		reply, requestErr = ReplyWithoutTools(context.Background(), provider, DefaultModel, "private prompt content")
	})
	if requestErr != nil || reply != "retried answer" || calls.Load() != 2 {
		t.Fatalf("retry success = reply %q, error %v, calls %d", reply, requestErr, calls.Load())
	}
	for _, want := range []string{
		`status="503"`, `request_id="req-retry-first"`, `backend_type="server_error"`, `backend_code="overloaded"`,
		`attempt="1"`, `attempts="2"`, `retry_decision="retry_scheduled"`, `reason="temporary HTTP status"`,
	} {
		if !strings.Contains(log, want) {
			t.Fatalf("retry diagnostic = %q, missing %q", log, want)
		}
	}
	for _, secret := range []string{"private prompt content", "private provider body and prompt", "private-access-token", server.URL} {
		if strings.Contains(log, secret) {
			t.Fatalf("retry diagnostic exposed %q: %q", secret, log)
		}
	}
}

func TestCodexTransientRetryExhaustionDiagnosticsIncludeBothAttempts(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		call := calls.Add(1)
		writer.Header().Set("X-Request-ID", fmt.Sprintf("req-retry-%d", call))
		writer.WriteHeader(http.StatusBadGateway)
		_, _ = io.WriteString(writer, `{"error":{"type":"gateway_rejected","code":"private_code","message":"private provider body"}}`)
	}))
	defer server.Close()

	provider := runtimeTestProvider(t, server.Client(), server.URL+"/responses")
	var requestErr error
	log := captureChatDiagnostics(t, func() {
		_, requestErr = ReplyWithoutTools(context.Background(), provider, DefaultModel, "private prompt")
	})
	if requestErr == nil || calls.Load() != 2 {
		t.Fatalf("retry exhaustion = error %v, calls %d", requestErr, calls.Load())
	}
	if strings.Contains(requestFailureDiagnostic(requestErr), requestFailureAuthentication) {
		t.Fatalf("non-authentication retry failure got authentication advice: %v", requestErr)
	}
	for _, want := range []string{
		`attempt="1"`, `attempt="2"`, `attempts="2"`, `retry_decision="retry_scheduled"`, `retry_decision="retry_exhausted"`,
		`request_id="req-retry-1"`, `request_id="req-retry-2"`, `status="502"`, `backend_type="gateway_rejected"`, `backend_code_detail="unrecognized"`,
	} {
		if !strings.Contains(log, want) {
			t.Fatalf("exhaustion diagnostic = %q, missing %q", log, want)
		}
	}
	for _, secret := range []string{"private provider body", "private_code", "private prompt", "private-access-token", server.URL} {
		if strings.Contains(log, secret) {
			t.Fatalf("exhaustion diagnostic exposed %q: %q", secret, log)
		}
	}
}

func TestCodexTransientRetryCancellationStopsBeforeSecondDispatch(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		calls.Add(1)
		cancel()
		writer.WriteHeader(http.StatusServiceUnavailable)
		_, _ = io.WriteString(writer, `{"error":{"type":"server_error","code":"overloaded"}}`)
	}))
	defer server.Close()

	provider := runtimeTestProvider(t, server.Client(), server.URL+"/responses")
	requestErr := requestCodexWithoutTools(ctx, provider)
	if !errors.Is(requestErr, context.Canceled) || calls.Load() != 1 {
		t.Fatalf("canceled retry = error %v, calls %d", requestErr, calls.Load())
	}

	var deadlineCalls atomic.Int32
	deadlineServer := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		deadlineCalls.Add(1)
		writer.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer deadlineServer.Close()
	deadlineProvider := runtimeTestProvider(t, deadlineServer.Client(), deadlineServer.URL+"/responses")
	deadlineContext, deadlineCancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer deadlineCancel()
	deadlineErr := requestCodexWithoutTools(deadlineContext, deadlineProvider)
	if !errors.Is(deadlineErr, context.DeadlineExceeded) || deadlineCalls.Load() != 0 {
		t.Fatalf("deadline retry = error %v, calls %d", deadlineErr, deadlineCalls.Load())
	}
}

func TestCodexNoRetryStatusesAndQuota(t *testing.T) {
	for _, test := range []struct {
		name   string
		status int
		body   string
	}{
		{name: "ordinary 400", status: http.StatusBadRequest, body: `{"error":{"type":"invalid_request_error","code":"invalid_input"}}`},
		{name: "authentication 401", status: http.StatusUnauthorized, body: `{"error":{"type":"authentication_error","code":"invalid_token"}}`},
		{name: "authentication 403", status: http.StatusForbidden, body: `{"error":{"type":"permission_error","code":"forbidden"}}`},
		{name: "quota 429", status: http.StatusTooManyRequests, body: `{"error":{"type":"rate_limit_error","code":"insufficient_quota"}}`},
	} {
		t.Run(test.name, func(t *testing.T) {
			var calls atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
				calls.Add(1)
				writer.WriteHeader(test.status)
				_, _ = io.WriteString(writer, test.body)
			}))
			defer server.Close()
			provider := runtimeTestProvider(t, server.Client(), server.URL+"/responses")
			if err := requestCodexWithoutTools(context.Background(), provider); err == nil {
				t.Fatal("non-retryable request unexpectedly succeeded")
			}
			if calls.Load() != 1 {
				t.Fatalf("non-retryable status %d dispatched %d times", test.status, calls.Load())
			}
		})
	}
}

func TestCodexTransientNetworkRetryAndTLSConfigNoRetry(t *testing.T) {
	var calls atomic.Int32
	client := &http.Client{Transport: errorLoggingRoundTripper(func(request *http.Request) (*http.Response, error) {
		if calls.Add(1) == 1 {
			return nil, temporaryCodexNetworkError{}
		}
		return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"output_text":"network retry"}`))}, nil
	})}
	provider := runtimeTestProvider(t, client, "https://provider.invalid/responses")
	if err := requestCodexWithoutTools(context.Background(), provider); err != nil || calls.Load() != 2 {
		t.Fatalf("transient network retry = error %v, calls %d", err, calls.Load())
	}

	var tlsCalls atomic.Int32
	tlsErr := errors.New("tls: private certificate configuration")
	tlsClient := &http.Client{Transport: errorLoggingRoundTripper(func(request *http.Request) (*http.Response, error) {
		tlsCalls.Add(1)
		return nil, tlsErr
	})}
	tlsProvider := runtimeTestProvider(t, tlsClient, "https://provider.invalid/responses")
	requestErr := requestCodexWithoutTools(context.Background(), tlsProvider)
	if !errors.Is(requestErr, tlsErr) || tlsCalls.Load() != 1 {
		t.Fatalf("TLS/config retry = error %v, calls %d", requestErr, tlsCalls.Load())
	}
}

func TestCodexReceivedStreamFailureIsNotRetried(t *testing.T) {
	for _, test := range []struct {
		name   string
		client *http.Client
	}{
		{
			name: "malformed response",
			client: &http.Client{Transport: errorLoggingRoundTripper(func(request *http.Request) (*http.Response, error) {
				return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader("data: {not-valid}\n\n"))}, nil
			})},
		},
		{
			name: "received stream read failure",
			client: &http.Client{Transport: errorLoggingRoundTripper(func(request *http.Request) (*http.Response, error) {
				return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: failedCodexStreamReader{}}, nil
			})},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			provider := runtimeTestProvider(t, test.client, "https://provider.invalid/responses")
			if err := requestCodexWithoutTools(context.Background(), provider); err == nil {
				t.Fatal("stream failure unexpectedly succeeded")
			}
		})
	}
}

func TestCodexTransientRetryDoesNotDuplicateToolExecution(t *testing.T) {
	setupSkillFixture(t)
	var backendCalls atomic.Int32
	var toolCalls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch backendCalls.Add(1) {
		case 1:
			writer.WriteHeader(http.StatusBadGateway)
			_, _ = io.WriteString(writer, `{"error":{"type":"server_error","code":"overloaded"}}`)
		case 2:
			_, _ = io.WriteString(writer, `{"output":[{"type":"function_call","call_id":"call-retry","name":"retry_tool","arguments":"{}"}]}`)
		case 3:
			_, _ = io.WriteString(writer, `{"output_text":"tool completed"}`)
		default:
			t.Errorf("unexpected backend request %d", backendCalls.Load())
		}
	}))
	defer server.Close()

	provider := runtimeTestProvider(t, server.Client(), server.URL+"/responses")
	if err := provider.RegisterTool(Tool{
		Name:        "retry_tool",
		Description: "A deterministic test tool.",
		Parameters:  map[string]any{"type": "object"},
		Handler: func(context.Context, string) (string, error) {
			toolCalls.Add(1)
			return "tool result", nil
		},
	}); err != nil {
		t.Fatal(err)
	}
	reply, err := provider.Reply(context.Background(), DefaultModel, "run the tool once")
	if err != nil || reply != "tool completed" || backendCalls.Load() != 3 || toolCalls.Load() != 1 {
		t.Fatalf("tool retry = reply %q, error %v, backend calls %d, tool calls %d", reply, err, backendCalls.Load(), toolCalls.Load())
	}
}

func TestCodexPostToolTransientRetryDoesNotReplayTool(t *testing.T) {
	setupSkillFixture(t)
	var backendCalls atomic.Int32
	var toolCalls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if backendCalls.Add(1) == 1 {
			_, _ = io.WriteString(writer, `{"output":[{"type":"function_call","call_id":"call-once","name":"retry_tool","arguments":"{}"}]}`)
			return
		}
		if backendCalls.Load() == 2 {
			writer.WriteHeader(http.StatusBadGateway)
			_, _ = io.WriteString(writer, `{"error":{"type":"server_error","code":"overloaded"}}`)
			return
		}
		_, _ = io.WriteString(writer, `{"output_text":"done"}`)
	}))
	defer server.Close()

	provider := runtimeTestProvider(t, server.Client(), server.URL+"/responses")
	if err := provider.RegisterTool(Tool{
		Name:        "retry_tool",
		Description: "A deterministic test tool.",
		Parameters:  map[string]any{"type": "object"},
		Handler: func(context.Context, string) (string, error) {
			toolCalls.Add(1)
			return "tool result", nil
		},
	}); err != nil {
		t.Fatal(err)
	}
	var requestErr error
	log := captureChatDiagnostics(t, func() {
		_, requestErr = provider.Reply(context.Background(), DefaultModel, "run the tool once")
	})
	if requestErr != nil || backendCalls.Load() != 3 || toolCalls.Load() != 1 {
		t.Fatalf("post-tool transient failure = %v, backend calls %d, tool calls %d", requestErr, backendCalls.Load(), toolCalls.Load())
	}
	for _, want := range []string{`attempt="1"`, `attempts="2"`, `retry_decision="retry_scheduled"`, `reason="temporary HTTP status"`} {
		if !strings.Contains(log, want) {
			t.Fatalf("post-tool diagnostic = %q, missing %q", log, want)
		}
	}
}

func TestCodexTransientThenUnauthorizedRefreshSuccessLogsBothFailures(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/oauth/token" {
			_, _ = io.WriteString(w, `{"access_token":"refreshed","refresh_token":"refresh","expires_in":3600}`)
			return
		}
		switch calls.Add(1) {
		case 1:
			w.Header().Set("X-Request-ID", "req-first")
			w.WriteHeader(http.StatusBadGateway)
		case 2:
			w.Header().Set("X-Request-ID", "req-unauthorized")
			w.WriteHeader(http.StatusUnauthorized)
		default:
			_, _ = io.WriteString(w, `{"output_text":"ok"}`)
		}
	}))
	defer server.Close()
	provider := runtimeTestProvider(t, server.Client(), server.URL+"/responses")
	if err := provider.auth.Store.Save(Credential{AccessToken: "access", RefreshToken: "refresh", ExpiresAt: time.Now().Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}
	provider.auth.AuthBaseURL = server.URL
	provider.auth.HTTPClient = server.Client()
	log := captureChatDiagnostics(t, func() {
		_, err := provider.Reply(context.Background(), DefaultModel, "hello")
		if err != nil {
			t.Fatal(err)
		}
	})
	if calls.Load() != 3 || strings.Count(log, `request_id="req-first"`) != 1 || strings.Count(log, `request_id="req-unauthorized"`) != 1 {
		t.Fatalf("retry history: calls=%d log=%q", calls.Load(), log)
	}
}

func TestCodexRefreshThenTransientExhaustionLogsEachAttempt(t *testing.T) {
	for _, statuses := range [][]int{
		{401, 502, 502},
		{502, 401, 502, 502},
	} {
		t.Run(fmt.Sprint(statuses), func(t *testing.T) {
			var calls atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/oauth/token" {
					_, _ = io.WriteString(w, `{"access_token":"refreshed","refresh_token":"refresh","expires_in":3600}`)
					return
				}
				index := int(calls.Add(1)) - 1
				w.Header().Set("X-Request-ID", fmt.Sprintf("req-attempt-%d", index+1))
				w.WriteHeader(statuses[index])
				_, _ = io.WriteString(w, `{"error":{"type":"server_error","code":"overloaded","message":"private response"}}`)
			}))
			defer server.Close()
			provider := runtimeTestProvider(t, server.Client(), server.URL+"/responses")
			if err := provider.auth.Store.Save(Credential{AccessToken: "access", RefreshToken: "refresh", ExpiresAt: time.Now().Add(time.Hour)}); err != nil {
				t.Fatal(err)
			}
			provider.auth.AuthBaseURL = server.URL
			provider.auth.HTTPClient = server.Client()
			var requestErr error
			log := captureChatDiagnostics(t, func() {
				_, requestErr = provider.Reply(context.Background(), DefaultModel, "private prompt")
			})
			if requestErr == nil || int(calls.Load()) != len(statuses) {
				t.Fatalf("error=%v calls=%d", requestErr, calls.Load())
			}
			lines := strings.Split(strings.TrimSpace(log), "\n")
			if len(lines) != len(statuses) {
				t.Fatalf("expected one record per attempt: %q", log)
			}
			for index, status := range statuses {
				decision := "retry_scheduled"
				if status == 401 {
					decision = "not_retryable"
				} else if index == len(statuses)-1 {
					decision = "retry_exhausted"
				}
				for _, want := range []string{fmt.Sprintf(`request_id="req-attempt-%d"`, index+1), fmt.Sprintf(`status="%d"`, status), `backend_type="server_error"`, `backend_code="overloaded"`, `retry_decision="` + decision + `"`, `attempt="`, `attempts="`} {
					if !strings.Contains(lines[index], want) {
						t.Fatalf("record %d missing %s: %q", index, want, lines[index])
					}
				}
			}
			if strings.Contains(log, "private response") || strings.Contains(log, "private prompt") || strings.Contains(log, server.URL) {
				t.Fatalf("private data in diagnostic: %q", log)
			}
		})
	}
}
