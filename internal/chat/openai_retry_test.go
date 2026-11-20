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
		reply, requestErr = ReplyWithoutTools(WithRequestReference(context.Background(), "chat-0123456789abcdef01234567"), provider, DefaultModel, "private prompt content")
	})
	if requestErr != nil || reply != "retried answer" || calls.Load() != 2 {
		t.Fatalf("retry success = reply %q, error %v, calls %d", reply, requestErr, calls.Load())
	}
	for _, want := range []string{
		`status="503"`, `request_id="req-retry-first"`, `backend_type="server_error"`, `backend_code="overloaded"`,
		`attempt="1"`, `attempts="2"`, `retry_decision="retry_scheduled"`, `reason="temporary HTTP status"`,
		`request_reference="chat-0123456789abcdef01234567"`,
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
	if requestErr == nil || calls.Load() != 3 {
		t.Fatalf("retry exhaustion = error %v, calls %d", requestErr, calls.Load())
	}
	if strings.Contains(requestFailureDiagnostic(requestErr), requestFailureAuthentication) {
		t.Fatalf("non-authentication retry failure got authentication advice: %v", requestErr)
	}
	for _, want := range []string{
		`attempt="1"`, `attempt="2"`, `attempt="3"`, `attempts="3"`, `retry_decision="retry_scheduled"`, `retry_decision="retry_exhausted"`,
		`request_id="req-retry-1"`, `request_id="req-retry-2"`, `request_id="req-retry-3"`, `status="502"`, `backend_type="gateway_rejected"`, `backend_code_detail="unrecognized"`,
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
	for _, want := range []string{`attempt="1"`, `attempts="2"`, `retry_decision="retry_scheduled"`, `reason="temporary HTTP status"`, `tool_started_count="1"`, `tool_completed_count="1"`, `tool_started="true"`, `tool_completed="true"`} {
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
		{401, 502, 502, 502},
		{502, 401, 502, 502, 502},
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
			wantAttempts := len(statuses)
			if requestErr == nil || int(calls.Load()) != wantAttempts {
				t.Fatalf("error=%v calls=%d", requestErr, calls.Load())
			}
			lines := strings.Split(strings.TrimSpace(log), "\n")
			if len(lines) != wantAttempts {
				t.Fatalf("expected one record per dispatch: %q", log)
			}
			for index, status := range statuses[:wantAttempts] {
				decision := "retry_scheduled"
				if status == 401 {
					decision = "not_retryable"
				} else if index == wantAttempts-1 {
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

type fixedReadError struct{ err error }

func (reader fixedReadError) Read([]byte) (int, error) { return 0, reader.err }

func (reader fixedReadError) Close() error { return nil }

func TestCodexRetryBackoffAndRetryAfterPlanIsBounded(t *testing.T) {
	now := time.Now()
	first := codexRetryPlanFor(context.Background(), http.StatusServiceUnavailable, errors.New("temporary"), "", now, 1, func(time.Duration) time.Duration { return 0 })
	second := codexRetryPlanFor(context.Background(), http.StatusServiceUnavailable, errors.New("temporary"), "", now, 2, func(time.Duration) time.Duration { return 0 })
	if !first.retry || first.wait != time.Second || first.waitSource != "exponential" {
		t.Fatalf("first retry plan = %+v", first)
	}
	if !second.retry || second.wait != 2*time.Second || second.waitSource != "exponential" {
		t.Fatalf("second retry plan = %+v", second)
	}
	boundedJitter := codexRetryPlanFor(context.Background(), http.StatusServiceUnavailable, errors.New("temporary"), "", now, 1, func(time.Duration) time.Duration {
		return 10 * openAITransientJitterCap
	})
	if !boundedJitter.retry || boundedJitter.wait != time.Second+openAITransientJitterCap {
		t.Fatalf("bounded jitter plan = %+v", boundedJitter)
	}
	negativeJitter := codexRetryPlanFor(context.Background(), http.StatusServiceUnavailable, errors.New("temporary"), "", now, 1, func(time.Duration) time.Duration {
		return -time.Second
	})
	if !negativeJitter.retry || negativeJitter.wait != time.Second {
		t.Fatalf("negative jitter plan = %+v", negativeJitter)
	}

	integer := codexRetryPlanFor(context.Background(), http.StatusServiceUnavailable, errors.New("temporary"), "5", now, 1, func(time.Duration) time.Duration { return 0 })
	if !integer.retry || integer.wait != 5*time.Second || integer.waitSource != "retry_after_integer" {
		t.Fatalf("integer Retry-After plan = %+v", integer)
	}
	invalid := codexRetryPlanFor(context.Background(), http.StatusServiceUnavailable, errors.New("temporary"), "not-a-delay", now, 1, func(time.Duration) time.Duration { return 0 })
	if !invalid.retry || invalid.wait != time.Second || invalid.waitSource != "retry_after_invalid" {
		t.Fatalf("invalid Retry-After plan = %+v", invalid)
	}
	date := now.Add(5 * time.Second).UTC().Format(http.TimeFormat)
	datePlan := codexRetryPlanFor(context.Background(), http.StatusServiceUnavailable, errors.New("temporary"), date, now, 1, func(time.Duration) time.Duration { return 0 })
	if !datePlan.retry || datePlan.wait < 4*time.Second || datePlan.waitSource != "retry_after_date" {
		t.Fatalf("date Retry-After plan = %+v", datePlan)
	}
	capPlan := codexRetryPlanFor(context.Background(), http.StatusServiceUnavailable, errors.New("temporary"), "31", now, 1, func(time.Duration) time.Duration { return 0 })
	if capPlan.retry || capPlan.reason != codexRetryReasonRetryAfterCap {
		t.Fatalf("cap plan = %+v", capPlan)
	}

	deadlineNow := time.Now()
	deadlineContext, cancel := context.WithDeadline(context.Background(), deadlineNow.Add(500*time.Millisecond))
	defer cancel()
	deadlinePlan := codexRetryPlanFor(deadlineContext, http.StatusServiceUnavailable, errors.New("temporary"), "", deadlineNow, 1, func(time.Duration) time.Duration { return 0 })
	if deadlinePlan.retry || deadlinePlan.reason != codexRetryReasonDeadlineWait {
		t.Fatalf("deadline plan = %+v", deadlinePlan)
	}
	canceledContext, cancelCanceled := context.WithCancel(context.Background())
	cancelCanceled()
	canceledPlan := codexRetryPlanFor(canceledContext, http.StatusServiceUnavailable, errors.New("temporary"), "", now, 1, func(time.Duration) time.Duration { return 0 })
	if canceledPlan.retry || canceledPlan.reason != codexRetryReasonCanceled {
		t.Fatalf("canceled plan = %+v", canceledPlan)
	}
}

func TestCodexTemporaryInterruptedReadRetriesWithoutReplayingTools(t *testing.T) {
	var calls atomic.Int32
	client := &http.Client{Transport: errorLoggingRoundTripper(func(request *http.Request) (*http.Response, error) {
		if calls.Add(1) == 1 {
			header := make(http.Header)
			header.Set("X-Request-ID", "req-interrupted")
			return &http.Response{StatusCode: http.StatusOK, Header: header, Body: fixedReadError{err: temporaryCodexNetworkError{}}}, nil
		}
		return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"output_text":"recovered"}`))}, nil
	})}
	provider := runtimeTestProvider(t, client, "https://provider.invalid/responses")
	waits := []time.Duration{}
	provider.retrySleep = func(_ context.Context, delay time.Duration) error {
		waits = append(waits, delay)
		return nil
	}
	var requestErr error
	var reply string
	log := captureChatDiagnostics(t, func() {
		reply, requestErr = ReplyWithoutTools(context.Background(), provider, DefaultModel, "private prompt")
	})
	if requestErr != nil || reply != "recovered" || calls.Load() != 2 || len(waits) != 1 || waits[0] != time.Second {
		t.Fatalf("interrupted read retry = reply %q error %v calls %d waits %v", reply, requestErr, calls.Load(), waits)
	}
	for _, want := range []string{`stream_classification="interrupted_read"`, `reason="interrupted stream read"`, `request_id="req-interrupted"`, `retry_wait_ms="1000"`} {
		if !strings.Contains(log, want) {
			t.Fatalf("interrupted read diagnostic = %q, missing %q", log, want)
		}
	}
}

func TestCodexPrematureEOFRetriesOnlyBeforeUsableCompletion(t *testing.T) {
	partial := "data: {\"type\":\"response.output_text.delta\",\"delta\":\"partial\"}\n\n"
	var calls atomic.Int32
	client := &http.Client{Transport: errorLoggingRoundTripper(func(request *http.Request) (*http.Response, error) {
		if calls.Add(1) == 1 {
			header := make(http.Header)
			header.Set("X-Request-ID", "req-eof-123")
			return &http.Response{StatusCode: http.StatusOK, Header: header, Body: io.NopCloser(io.MultiReader(strings.NewReader(partial), fixedReadError{err: io.ErrUnexpectedEOF}))}, nil
		}
		return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"output_text":"complete"}`))}, nil
	})}
	provider := runtimeTestProvider(t, client, "https://provider.invalid/responses")
	provider.retrySleep = func(context.Context, time.Duration) error { return nil }
	provider.retryJitter = func(time.Duration) time.Duration { return 0 }
	var reply string
	var requestErr error
	log := captureChatDiagnostics(t, func() {
		reply, requestErr = ReplyWithoutTools(context.Background(), provider, DefaultModel, "private prompt")
	})
	if requestErr != nil || reply != "complete" || calls.Load() != 2 {
		t.Fatalf("premature EOF retry = reply %q error %v calls %d", reply, requestErr, calls.Load())
	}
	for _, want := range []string{`stream_classification="premature_eof"`, `reason="premature EOF"`, `request_id="req-eof-123"`} {
		if !strings.Contains(log, want) {
			t.Fatalf("premature EOF diagnostic = %q, missing %q", log, want)
		}
	}
}

func TestCodexCompletedResponseDoesNotRetryTrailingReadError(t *testing.T) {
	const completed = "data: {\"type\":\"response.completed\",\"response\":{\"output_text\":\"complete\"}}\n\n"
	var calls atomic.Int32
	client := &http.Client{Transport: errorLoggingRoundTripper(func(request *http.Request) (*http.Response, error) {
		calls.Add(1)
		body := io.MultiReader(strings.NewReader(completed), fixedReadError{err: temporaryCodexNetworkError{}})
		header := make(http.Header)
		header.Set("X-Request-ID", "req-trailing-read")
		return &http.Response{StatusCode: http.StatusOK, Header: header, Body: io.NopCloser(body)}, nil
	})}
	provider := runtimeTestProvider(t, client, "https://provider.invalid/responses")
	var reply string
	var requestErr error
	log := captureChatDiagnostics(t, func() {
		reply, requestErr = ReplyWithoutTools(context.Background(), provider, DefaultModel, "private prompt")
	})
	if requestErr != nil || reply != "complete" || calls.Load() != 1 {
		t.Fatalf("completed trailing read error = reply %q error %v calls %d", reply, requestErr, calls.Load())
	}
	if !strings.Contains(log, "operation=chat.provider.trailing_read") || strings.Contains(log, "temporary network detail") {
		t.Fatalf("completed response logged a retryable trailing read failure: %q", log)
	}
	for _, field := range []string{`phase="stream"`, `status="200"`, `request_id="req-trailing-read"`, `configured_model="` + DefaultModel + `"`} {
		if !strings.Contains(log, field) {
			t.Errorf("trailing read diagnostic missing %s: %q", field, log)
		}
	}
}

func TestCodexPrematureEOFExhaustsThreeDispatches(t *testing.T) {
	partial := "data: {\"type\":\"response.output_text.delta\",\"delta\":\"partial\"}\n\n"
	var calls atomic.Int32
	client := &http.Client{Transport: errorLoggingRoundTripper(func(request *http.Request) (*http.Response, error) {
		call := calls.Add(1)
		header := make(http.Header)
		header.Set("X-Request-ID", fmt.Sprintf("req-eof-%d", call))
		body := io.MultiReader(strings.NewReader(partial), fixedReadError{err: io.ErrUnexpectedEOF})
		return &http.Response{StatusCode: http.StatusOK, Header: header, Body: io.NopCloser(body)}, nil
	})}
	provider := runtimeTestProvider(t, client, "https://provider.invalid/responses")
	var requestErr error
	log := captureChatDiagnostics(t, func() {
		requestErr = requestCodexWithoutTools(context.Background(), provider)
	})
	if requestErr == nil || calls.Load() != int32(openAIModelAttempts) {
		t.Fatalf("premature EOF exhaustion = error %v calls %d", requestErr, calls.Load())
	}
	if !strings.Contains(log, `attempt="3"`) || !strings.Contains(log, `retry_decision="retry_exhausted"`) {
		t.Fatalf("premature EOF exhaustion diagnostic = %q", log)
	}
}

func TestCodexIncompleteToolDeltaIsDiscarded(t *testing.T) {
	setupSkillFixture(t)
	const incomplete = "data: {\"type\":\"response.output_item.added\",\"item\":{\"type\":\"function_call\",\"call_id\":\"call-incomplete\",\"name\":\"retry_tool\"}}\n\ndata: {\"type\":\"response.function_call_arguments.delta\",\"item_id\":\"call-incomplete\",\"delta\":\"{}\"}\n\n"
	var calls atomic.Int32
	var toolCalls atomic.Int32
	client := &http.Client{Transport: errorLoggingRoundTripper(func(request *http.Request) (*http.Response, error) {
		calls.Add(1)
		return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(incomplete))}, nil
	})}
	provider := runtimeTestProvider(t, client, "https://provider.invalid/responses")
	if err := provider.RegisterTool(Tool{
		Name:        "retry_tool",
		Description: "A deterministic test tool.",
		Parameters:  map[string]any{"type": "object"},
		Handler: func(context.Context, string) (string, error) {
			toolCalls.Add(1)
			return "must not run", nil
		},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := provider.Reply(context.Background(), DefaultModel, "run the incomplete tool"); err == nil {
		t.Fatal("incomplete tool response unexpectedly succeeded")
	}
	if calls.Load() != int32(openAIModelAttempts) || toolCalls.Load() != 0 {
		t.Fatalf("incomplete tool delta = backend calls %d, tool calls %d", calls.Load(), toolCalls.Load())
	}
}

func TestCodexInterruptedRetryDoesNotRepeatEarlierTool(t *testing.T) {
	setupSkillFixture(t)
	partial := "data: {\"type\":\"response.output_text.delta\",\"delta\":\"partial\"}\n\n"
	var backendCalls atomic.Int32
	var toolCalls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch backendCalls.Add(1) {
		case 1:
			_, _ = io.WriteString(writer, `{"output":[{"type":"function_call","call_id":"call-once","name":"retry_tool","arguments":"{}"}]}`)
		case 2:
			writer.Header().Set("X-Request-ID", "req-tool-interrupted")
			writer.WriteHeader(http.StatusOK)
			_, _ = io.Copy(writer, io.MultiReader(strings.NewReader(partial), fixedReadError{err: temporaryCodexNetworkError{}}))
		case 3:
			_, _ = io.WriteString(writer, `{"output_text":"recovered after tool"}`)
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
	if err != nil || reply != "recovered after tool" || backendCalls.Load() != 3 || toolCalls.Load() != 1 {
		t.Fatalf("interrupted tool retry = reply %q error %v backend calls %d tool calls %d", reply, err, backendCalls.Load(), toolCalls.Load())
	}
}

func TestCodexStreamClassificationControlsRetry(t *testing.T) {
	tests := []struct {
		name           string
		body           string
		retry          bool
		classification string
	}{
		{name: "malformed", body: "data: {not-json}\n\n", classification: modelStreamMalformedEvent},
		{name: "unrecognized", body: "data: {\"type\":\"response.future_event\"}\n\n", classification: modelStreamUnrecognizedEvent},
		{name: "incomplete", body: "data: {\"type\":\"response.incomplete\",\"response\":{\"error\":{\"type\":\"server_error\",\"code\":\"overloaded\"}}}\n\n", classification: modelStreamResponseIncomplete},
		{name: "transient provider", body: "data: {\"type\":\"response.failed\",\"error\":{\"type\":\"server_error\",\"code\":\"overloaded\"}}\n\n", retry: true, classification: modelStreamProviderError},
		{name: "unknown provider code", body: "data: {\"type\":\"response.failed\",\"error\":{\"type\":\"server_error\",\"code\":\"private_permanent_code\",\"message\":\"Bearer privatecredential https://user:pass@host/?token=value\\n\\u001b[31m\"}}\n\n", classification: modelStreamProviderError},
		{name: "unknown provider kind", body: "data: {\"type\":\"response.failed\",\"error\":{\"type\":\"private_permanent_kind\",\"code\":\"overloaded\",\"message\":\"Bearer privatecredential\"}}\n\n", classification: modelStreamProviderError},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var calls atomic.Int32
			client := &http.Client{Transport: errorLoggingRoundTripper(func(request *http.Request) (*http.Response, error) {
				if calls.Add(1) == 1 || !test.retry {
					return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(test.body))}, nil
				}
				return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"output_text":"recovered"}`))}, nil
			})}
			provider := runtimeTestProvider(t, client, "https://provider.invalid/responses")
			var waits []time.Duration
			provider.retrySleep = func(_ context.Context, delay time.Duration) error { waits = append(waits, delay); return nil }
			provider.retryJitter = func(time.Duration) time.Duration { return 0 }
			var requestErr error
			var reply string
			log := captureChatDiagnostics(t, func() {
				reply, requestErr = ReplyWithoutTools(context.Background(), provider, DefaultModel, "private prompt")
			})
			if test.retry {
				if requestErr != nil || reply != "recovered" || calls.Load() != 2 || len(waits) != 1 {
					t.Fatalf("transient stream = reply %q error %v calls %d waits %v", reply, requestErr, calls.Load(), waits)
				}
			} else if requestErr == nil || calls.Load() != 1 || len(waits) != 0 {
				t.Fatalf("non-retryable stream = error %v calls %d waits %v", requestErr, calls.Load(), waits)
			}
			if !strings.Contains(log, `stream_classification="`+test.classification+`"`) {
				t.Fatalf("classification diagnostic = %q", log)
			}
			message := log
			if requestErr != nil {
				message += requestErr.Error() + requestFailureDiagnostic(requestErr)
			}
			for _, secret := range []string{"private_permanent_code", "private_permanent_kind", "privatecredential", "user:pass", "token=value", "\x1b[31m"} {
				if strings.Contains(message, secret) {
					t.Errorf("stream failure exposed %q: %q", secret, message)
				}
			}
		})
	}
}

func TestCodexRetriesArePerModelRequestAcrossToolRounds(t *testing.T) {
	calls, actions := 0, 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if calls%3 != 0 {
			w.WriteHeader(http.StatusBadGateway)
			return
		}
		round := calls / 3
		if round <= 4 {
			writeCodexFunctionCall(t, w, fmt.Sprintf("call-%d", round), "record_round", `{}`)
			return
		}
		fmt.Fprint(w, `{"output_text":"done"}`)
	}))
	defer server.Close()
	provider := runtimeTestProvider(t, server.Client(), server.URL)
	if err := provider.RegisterTool(Tool{Name: "record_round", Description: "Record a round", Parameters: map[string]any{"type": "object"}, Handler: func(context.Context, string) (string, error) {
		actions++
		return "ok", nil
	}}); err != nil {
		t.Fatal(err)
	}
	reply, _, err := provider.requestReply(context.Background(), Credential{}, DefaultModel, nil, true)
	if err != nil || reply != "done" || calls != 15 || actions != 4 {
		t.Fatalf("reply=%q error=%v calls=%d actions=%d", reply, err, calls, actions)
	}
}

func TestCodexMalformedBodyWithTransientReadIsNotRetried(t *testing.T) {
	var calls atomic.Int32
	client := &http.Client{Transport: errorLoggingRoundTripper(func(*http.Request) (*http.Response, error) {
		calls.Add(1)
		body := io.MultiReader(strings.NewReader("data: {bad json}\n\n"), fixedReadError{err: io.ErrUnexpectedEOF})
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(body)}, nil
	})}
	provider := runtimeTestProvider(t, client, "https://provider.invalid/responses")
	if err := requestCodexWithoutTools(context.Background(), provider); err == nil || calls.Load() != 1 {
		t.Fatalf("error=%v calls=%d", err, calls.Load())
	}
}

func TestCodexLifecycleAndUnknownEventsPreserveOutput(t *testing.T) {
	body := "data: {\"type\":\"response.created\"}\n\ndata: {\"type\":\"response.future_event\"}\n\ndata: {\"type\":\"response.output_text.delta\",\"delta\":\"hello\"}\n\ndata: {\"type\":\"response.completed\"}\n\n"
	result, err := parseCodexStreamResponse([]byte(body))
	if err != nil || result.Text != "hello" {
		t.Fatalf("result=%+v error=%v", result, err)
	}
}
