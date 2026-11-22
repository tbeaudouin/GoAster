package chat

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"testing"
)

func TestModelHTTPErrorBodyDiagnostic(t *testing.T) {
	for _, tc := range []struct {
		name, body, state string
		fail, oversize    bool
	}{
		{name: "json", body: `{"error":{"message":"raw secret"}}`, state: "complete"},
		{name: "plain controls", body: "plain\n\x1b\x00raw secret", state: "complete"},
		{name: "malformed", body: `{"broken":raw secret`, state: "complete"},
		{name: "empty", state: "empty"},
		{name: "read failure", body: "raw secret partial", state: "read_failed", fail: true},
		{name: "oversize", state: "oversize", oversize: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body := tc.body
			if tc.oversize {
				body = strings.Repeat("x", maxResponseBody+1)
			}
			client := &http.Client{Transport: errorLoggingRoundTripper(func(*http.Request) (*http.Response, error) {
				var reader io.Reader = strings.NewReader(body)
				if tc.fail {
					reader = io.MultiReader(reader, fixedReadError{err: errors.New("private read failure")})
				}
				return &http.Response{StatusCode: 400, Header: make(http.Header), Body: io.NopCloser(reader)}, nil
			})}
			provider := runtimeTestProvider(t, client, "http://example.test")
			var requestErr error
			log := captureChatDiagnostics(t, func() { _, requestErr = provider.Reply(context.Background(), DefaultModel, "private prompt") })
			if requestErr == nil {
				t.Fatal("expected error")
			}
			if !strings.Contains(log, `operation=chat.provider`) || !strings.Contains(log, `status="400"`) || !strings.Contains(log, `openai_http_error_body_state=`+strconv.Quote(tc.state)) {
				t.Fatalf("missing context: %q", log)
			}
			if tc.oversize && strings.Contains(log, `openai_http_error_body=`) {
				t.Fatal("oversized body was logged")
			}
			if !tc.oversize && !strings.Contains(log, `openai_http_error_body=`+strconv.Quote(tc.body)) {
				t.Fatalf("missing quoted body: %q", log)
			}
			if strings.Count(log, "\n") != 1 {
				t.Fatalf("log injection: %q", log)
			}
			var rawAccessor interface{ DiagnosticOpenAIHTTPErrorBody() (string, string) }
			if errors.As(requestErr, &rawAccessor) {
				t.Fatal("raw body exposed through public error interface")
			}
			if strings.Contains(requestErr.Error(), "raw secret") || strings.Contains(requestFailureDiagnostic(requestErr), "raw secret") || strings.Contains(modelStreamCauseDetail(requestErr), "raw secret") || strings.Contains(ModelStreamMessageForError(requestErr, ""), "raw secret") || strings.Contains(log, "private prompt") || strings.Contains(log, "access-token-should-never-be-logged") {
				t.Fatal("private content escaped its boundary")
			}
		})
	}
}
