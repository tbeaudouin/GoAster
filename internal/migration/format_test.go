package migration

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestFormatAttemptSafeDetailsAndScheduling(t *testing.T) {
	result := OneShotResult{Status: "retry_later", CreationState: "not_created", Code: "no_capacity", Stage: "provider", HTTPStatus: 409, ProviderCode: "invalid_input", ProviderMessage: "invalid input", ProviderFields: []ProviderField{{Field: "server_type", Messages: []string{"is not available"}}}, ServerName: "goaster-migration-0123456789abcdef", ServerID: 42, ServerLabel: "label", IP: "192.0.2.1", Location: "fsn1", Expires: 2000000000, Attempt: "attempt1", Handoff: "failed"}
	text := FormatAttempt(result, "30m")
	for _, want := range []string{"⏳ Migration waiting for capacity", result.ServerName, "every 30 minutes", "Status: retry_later", "Creation: not_created", "Reason (code): no_capacity", "Stage: provider", "HTTP status: 409", "Provider code: invalid_input", "Provider message: invalid input", "Provider field server_type: is not available", "Server ID: 42", "Server label: label", "IP: 192.0.2.1", "Location: fsn1", "Expires: 2000000000", "Attempt: attempt1", "Handoff: failed"} {
		if !strings.Contains(text, want) {
			t.Errorf("missing %q in %q", want, text)
		}
	}
}

func TestFormatAttemptHonestyAndRedaction(t *testing.T) {
	tests := []struct {
		result                 OneShotResult
		retry, want, forbidden string
	}{
		{OneShotResult{Status: "retry_later", CreationState: "not_created", Code: "no_capacity"}, "30m", "No server was created", "secret"},
		{OneShotResult{Status: "retry_later", CreationState: "not_created", Code: "no_capacity", ServerID: 42}, "unknown", "Server ID: 42", "No server was created"},
		{OneShotResult{Status: "retry_later", CreationState: "unknown", Code: "no_capacity"}, "scheduled", "next scheduled run", "No server was created"},
		{OneShotResult{Status: "retry_later", CreationState: "created", Code: "no_capacity", Handoff: "failed"}, "30m", "delivery incomplete", "waiting for capacity"},
		{OneShotResult{Status: "success", CreationState: "created", Handoff: "delivered"}, "stopped", "Server created", "Migration completed"},
		{OneShotResult{Status: "stop"}, "stopped", "Migration stopped", "every 30 minutes"},
		{OneShotResult{Status: "strange", Code: "no_capacity"}, "unknown", "Status: unknown", "every 30 minutes"},
		{OneShotResult{Status: "schedule_failed", CreationState: "unknown"}, "failed", "could not be scheduled", "every 30 minutes"},
		{OneShotResult{Status: "retry_later", ServerName: "secret\nNext step: injected", ServerLabel: "https://secret", ProviderMessage: "private secret", Attempt: "key\nsecret"}, "unknown", "Status: retry_later", "secret"},
	}
	for _, tc := range tests {
		text := FormatAttempt(tc.result, tc.retry)
		if !strings.Contains(text, tc.want) || strings.Contains(text, tc.forbidden) {
			t.Errorf("got %q, want %q, forbidden %q", text, tc.want, tc.forbidden)
		}
	}
	for _, tc := range []struct{ input, want string }{{`{"status":"retry_later","message":"Migration retry scheduled every 30 minutes."}`, "every 30 minutes"}, {`{"status":"schedule_failed"}`, "could not be scheduled"}, {`broken`, "Status: unknown"}} {
		if got := FormatToolCompletion(tc.input); !strings.Contains(got, tc.want) {
			t.Errorf("got %q, want %q", got, tc.want)
		}
	}
}

func TestFormatAttemptFailureReasons(t *testing.T) {
	for _, tc := range []struct{ code, state, reason, creation string }{
		{"provider_auth_rejected", "not_created", "Provider authentication failed.", "No server was created."},
		{"provider_lookup_failure", "not_created", "Provider lookup failed.", "No server was created."},
		{"provider_http_error", "not_created", "Provider request failed.", "No server was created."},
		{"provider_timeout", "unknown", "Provider request timed out.", "It is unknown whether a server was created."},
		{"secret-provider-token", "unknown", "Migration attempt outcome", "It is unknown whether a server was created."},
	} {
		text := FormatAttempt(OneShotResult{Status: "retry_later", Code: tc.code, CreationState: tc.state, ServerName: "secret-provider-token"}, "30m")
		for _, want := range []string{tc.reason, tc.creation, "Status: retry_later", "Creation: " + tc.state, "Retry automatically every 30 minutes"} {
			if !strings.Contains(text, want) {
				t.Errorf("missing %q in %q", want, text)
			}
		}
		if strings.Contains(text, "secret-provider-token") {
			t.Fatal(text)
		}
	}
}

func TestFormatCreationMetadataBoundaries(t *testing.T) {
	for _, tc := range []struct {
		name, code, state string
		id                int64
		ip, want, absent  string
	}{
		{"capacity label only", "no_capacity", "not_created", 0, "", "No server was created.", "Server creation details conflict"},
		{"generic label only", "provider_http_error", "not_created", 0, "", "No server was created.", "Server creation details conflict"},
		{"capacity ID conflict", "no_capacity", "not_created", 42, "", "Server creation details conflict", "No server was created."},
		{"capacity IP conflict", "no_capacity", "not_created", 0, "192.0.2.1", "Server creation details conflict", "No server was created."},
		{"generic ID conflict", "provider_http_error", "not_created", 42, "", "Server creation details conflict", "No server was created."},
		{"generic IP conflict", "provider_http_error", "not_created", 0, "192.0.2.1", "Server creation details conflict", "No server was created."},
		{"unknown label only", "no_capacity", "unknown", 0, "", "It is unknown whether a server was created.", "No server was created."},
	} {
		t.Run(tc.name, func(t *testing.T) {
			result := OneShotResult{Status: "retry_later", CreationState: tc.state, Code: tc.code, Stage: "provider", HTTPStatus: 409, ProviderCode: "resource_unavailable", ServerID: tc.id, IP: tc.ip}
			if tc.id == 0 && tc.ip == "" {
				result.ServerLabel = "planned-label"
			}
			encoded, err := json.Marshal(struct {
				OneShotResult
				Message string `json:"message"`
			}{result, "Migration retry scheduled every 30 minutes."})
			if err != nil {
				t.Fatal(err)
			}
			for boundary, output := range map[string]string{
				"initial":   FormatToolCompletion(string(encoded)),
				"scheduled": FormatAttempt(result, "scheduled"),
			} {
				for _, want := range []string{tc.want, "Provider code: resource_unavailable", "Status: retry_later", "Creation: " + tc.state, "Retry automatically"} {
					if !strings.Contains(output, want) {
						t.Errorf("%s: missing %q in %q", boundary, want, output)
					}
				}
				if result.ServerLabel != "" && !strings.Contains(output, "Server label: planned-label") {
					t.Errorf("%s: missing label in %q", boundary, output)
				}
				if strings.Contains(output, tc.absent) {
					t.Errorf("%s: unexpected %q in %q", boundary, tc.absent, output)
				}
			}
		})
	}
}
