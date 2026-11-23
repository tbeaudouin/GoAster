package chat

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestModelDiagnosticProviderWireAndExactText(t *testing.T) {
	const prompt = "Reply with exactly GOASTER_MODEL_TEST_OK and nothing else."
	for _, value := range []string{"GOASTER_MODEL_TEST_OK", " GOASTER_MODEL_TEST_OK", "GOASTER_MODEL_TEST_OK\n", ""} {
		for _, stream := range []bool{false, true} {
			transport := errorLoggingRoundTripper(func(req *http.Request) (*http.Response, error) {
				var request codexRequest
				if err := json.NewDecoder(req.Body).Decode(&request); err != nil {
					t.Fatal(err)
				}
				if request.Model != "candidate-exact" || len(request.Tools) != 0 || len(request.Input) != 1 || request.Input[0].Role != "user" || len(request.Input[0].Content) != 1 || request.Input[0].Content[0].Text != prompt {
					t.Errorf("unexpected request: %+v", request)
				}
				encoded, _ := json.Marshal(value)
				body := `{"output_text":` + string(encoded) + `}`
				if stream {
					body = "data: {\"type\":\"response.output_text.delta\",\"delta\":" + string(encoded) + "}\n\ndata: {\"type\":\"response.completed\"}\n\n"
				}
				return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}, nil
			})
			provider := runtimeTestProvider(t, &http.Client{Transport: transport}, "https://example.invalid/responses")
			got, err := ReplyWithoutTools(context.Background(), provider, "candidate-exact", prompt)
			if value == "" && err != nil {
				continue
			}
			if err != nil || got != value {
				t.Fatalf("stream=%v expected %q got %q: %v", stream, value, got, err)
			}
		}
	}
}
