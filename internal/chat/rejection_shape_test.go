package chat

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"goaster/internal/diagnostics"
)

func TestRejectionShapeAndRedaction(t *testing.T) {
	secret := "sk-secret-private-prompt\n\x1b[31mhttps://example.invalid/credential"
	input := []codexInputItem{{Type: "message", Role: "user", Content: []codexInputContent{{Text: secret}}}, {Type: "function_call", Arguments: secret}, {Type: "function_call_output", Output: secret}, {Type: secret, Name: secret}}
	shape := requestShape(context.WithValue(context.Background(), codexSummarySizeKey{}, 42), []byte(strings.Repeat(secret, 10000)), input)
	cause := newBackendError("Codex backend request failed", 400, []byte(`{"error":{"type":"`+secret+`","code":"`+secret+`","message":"`+secret+`","param":"`+secret+`"}}`), "req-secret-token")
	failure := codexAttemptFailure{cause: cause, shape: shape}
	if !errors.Is(failure, cause) {
		t.Fatal("lost cause")
	}
	output := failure.Error()
	for _, field := range append(shape.fields(), cause.(backendError).DiagnosticFields()...) {
		output += " " + field.Key + "=" + field.Value
	}
	for _, forbidden := range []string{secret, "sk-secret", "example.invalid", "credential", "param=", "backend_message="} {
		if strings.Contains(output, forbidden) {
			t.Fatalf("private data exposed: %q", forbidden)
		}
	}
	for _, expected := range []string{"request bytes=", "input items=4", "messages=1", "tool calls=1", "tool outputs=1", "summary=42 bytes"} {
		if !strings.Contains(output, expected) {
			t.Errorf("missing %q in %s", expected, output)
		}
	}
	if diagnostics.RequestID("req-secret-token") != "" {
		t.Fatal("credential-shaped request ID accepted")
	}
}

func TestBackendRejectionStates(t *testing.T) {
	for _, tc := range []struct{ body, want string }{{`{"error":{}}`, "backend_code_detail=absent"}, {`{"error":{"code":null}}`, "backend_code_detail=unrecognized"}, {`{"error":{"code":"private prompt"}}`, "backend_code_detail=unrecognized"}, {`{"error":{"type":"invalid_request_error","code":"context_length_exceeded","message":"Unauthorized"}}`, "backend_code=context_length_exceeded"}} {
		err := newBackendError("Codex backend request failed", 400, []byte(tc.body), "req_abc12345")
		if !strings.Contains(err.Error(), tc.want) || strings.Contains(err.Error(), "Unauthorized") {
			t.Errorf("unexpected safe error: %s", err)
		}
	}
}

func TestHTTPRejectionDiagnosticsRedactBackendAndRequest(t *testing.T) {
	secret := "private-prompt sk-secret https://example.invalid/credential\n\x1b[31m"
	body, err := json.Marshal(map[string]any{"error": map[string]any{"type": secret, "code": secret, "message": secret, "param": secret, "extra": secret}})
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Request-ID", "req_abc12345")
		w.WriteHeader(400)
		_, _ = w.Write(body)
	}))
	defer server.Close()
	provider := runtimeTestProvider(t, server.Client(), server.URL)
	var requestErr error
	log := captureChatDiagnostics(t, func() {
		_, requestErr = provider.Reply(context.Background(), DefaultModel, "outgoing-prompt-only"+strings.Repeat("x", 100000))
	})
	if requestErr == nil {
		t.Fatal("expected rejection")
	}
	if !strings.Contains(log, "openai_http_error_body=") {
		t.Fatal("missing raw body diagnostic")
	}
	for _, output := range []string{requestErr.Error()} {
		for _, forbidden := range []string{"private-prompt", "sk-secret", "example.invalid", "credential", "\x1b", "param="} {
			if strings.Contains(output, forbidden) {
				t.Fatalf("leaked %q", forbidden)
			}
		}
		for _, expected := range []string{"unrecognized", "req_abc12345"} {
			if !strings.Contains(output, expected) {
				t.Errorf("missing %q in %s", expected, output)
			}
		}
	}
	if strings.Contains(log, "outgoing-prompt-only") || strings.Contains(log, "\x1b") {
		t.Fatal("request or unescaped control leaked into log")
	}
	if !strings.Contains(log, `request_bytes="`) || !strings.Contains(log, `message_count="`) {
		t.Fatalf("missing request shape: %s", log)
	}
}

func TestStreamRejectionFieldStates(t *testing.T) {
	for _, tc := range []struct{ event, want string }{
		{`{"type":"error","error":{"code":null,"type":"private prompt","message":"sk-secret"}}`, "backend_code_detail=unrecognized"},
		{`{"type":"error","error":{}}`, "backend_code_detail=absent"},
	} {
		_, err := parseCodexResponseResult([]byte("data: " + tc.event + "\n\n"))
		if err == nil {
			t.Fatal("expected stream failure")
		}
		var backend backendError
		if !errors.As(err, &backend) {
			t.Fatalf("missing backend cause: %v", err)
		}
		if !strings.Contains(backend.Error(), tc.want) || strings.Contains(backend.Error(), "private prompt") || strings.Contains(backend.Error(), "sk-secret") {
			t.Fatalf("unsafe state: %s", backend.Error())
		}
	}
}

func TestRejectionMessagePresence(t *testing.T) {
	for _, tc := range []struct{ name, field, state string }{
		{"absent", "", "absent"}, {"allowlisted", `,"message":"Unauthorized"`, "unrecognized"},
		{"private", `,"message":"private message"`, "unrecognized"}, {"null", `,"message":null`, "unrecognized"},
		{"blank", `,"message":" "`, "unrecognized"}, {"wrong type", `,"message":42`, "unrecognized"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			object := `{"code":"model_not_found"` + tc.field + `}`
			httpErr := newBackendError("Codex backend request failed", 400, []byte(`{"error":`+object+`}`), "")
			_, streamErr := parseCodexResponseResult([]byte("data: " + `{"type":"error","error":` + object + `}` + "\n\n"))
			for _, err := range []error{httpErr, streamErr} {
				if err == nil || !strings.Contains(err.Error(), "backend_message_detail="+tc.state) || strings.Contains(err.Error(), "Unauthorized") || strings.Contains(err.Error(), "private message") {
					t.Fatalf("unexpected rejection: %v", err)
				}
				log := captureChatDiagnostics(t, func() { diagnostics.Error("test", err) })
				if !strings.Contains(log, `backend_message_detail="`+tc.state+`"`) || strings.Contains(log, "private message") || strings.Contains(log, "Unauthorized") {
					t.Fatalf("unexpected log: %s", log)
				}
			}
		})
	}
}

func TestHTTPStreamRejectionHeaderInAgentContext(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Request-ID", "req_abc12345")
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"type\":\"error\",\"error\":{\"type\":\"invalid_request_error\",\"code\":\"model_not_found\",\"message\":\"private message\"}}\n\n"))
	}))
	defer server.Close()
	provider := runtimeTestProvider(t, server.Client(), server.URL)
	var err error
	log := captureChatDiagnostics(t, func() { _, err = provider.Reply(context.Background(), DefaultModel, "hello") })
	if err == nil {
		t.Fatal("expected stream rejection")
	}
	var backend backendError
	if !errors.As(err, &backend) {
		t.Fatalf("lost backend cause: %v", err)
	}
	for _, output := range []string{modelStreamCauseDetail(err), requestFailureDiagnostic(err), log} {
		for _, want := range []string{"req_abc12345", "invalid_request_error", "model_not_found"} {
			if !strings.Contains(output, want) {
				t.Errorf("missing %q: %s", want, output)
			}
		}
		if strings.Contains(output, "private message") {
			t.Errorf("message leaked: %s", output)
		}
	}
	if strings.Count(modelStreamCauseDetail(err), "request_id=") != 1 {
		t.Fatalf("duplicate request ID: %s", modelStreamCauseDetail(err))
	}
}

func TestStreamEventTypeIsNotBackendType(t *testing.T) {
	for _, eventType := range []string{"error", "response.failed"} {
		for _, shape := range []string{"top", "error", "response"} {
			for _, field := range []struct{ name, value, state string }{
				{"absent", "", "absent"},
				{"null", `,"type":null`, "unrecognized"},
				{"private", `,"type":"private type"`, "unrecognized"},
				{"recognized", `,"type":"invalid_request_error"`, "recognized"},
			} {
				if shape == "top" && field.name != "absent" {
					continue
				}
				for _, message := range []struct{ name, value, state string }{
					{"absent", "", "absent"},
					{"null", `,"message":null`, "unrecognized"},
					{"private", `,"message":"private message"`, "unrecognized"},
				} {
					t.Run(eventType+"/"+shape+"/"+field.name+"/"+message.name, func(t *testing.T) {
						detail := `"code":"model_not_found"` + field.value + message.value
						event := `{"type":"` + eventType + `",` + detail + `}`
						if shape == "error" {
							event = `{"type":"` + eventType + `","error":{` + detail + `}}`
						} else if shape == "response" {
							event = `{"type":"` + eventType + `","response":{"error":{` + detail + `}}}`
						}
						_, err := parseCodexResponseResult([]byte("data: " + event + "\n\n"))
						var backend backendError
						if !errors.As(err, &backend) {
							t.Fatalf("missing backend cause: %v", err)
						}
						wantType := "backend_type_detail=" + field.state
						if field.state == "recognized" {
							wantType = "backend_type=invalid_request_error"
						}
						output := backend.Error()
						for _, want := range []string{wantType, "backend_code=model_not_found", "backend_message_detail=" + message.state} {
							if !strings.Contains(output, want) {
								t.Errorf("missing %q: %s", want, output)
							}
						}
						if strings.Contains(output, "private type") || strings.Contains(output, "private message") {
							t.Errorf("private detail leaked: %s", output)
						}
					})
				}
			}
		}
	}
}
