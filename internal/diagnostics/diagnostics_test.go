package diagnostics

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"testing"
)

func TestErrorCapturesCauseChainAndSafeContext(t *testing.T) {
	var output bytes.Buffer
	restore := SetOutput(&output)
	defer restore()

	transport := errors.New("dial tcp backend.example:443: connection refused")
	Error("provider.http", errors.Join(errors.New("request failed"), transport),
		Field{Key: "model", Value: "gpt-test"},
		Field{Key: "status", Value: "502"},
		Field{Key: "request_id", Value: "req-123"},
	)

	log := output.String()
	for _, want := range []string{"ERROR operation=provider.http", `status="502"`, "*errors.joinError", "*errors.errorString"} {
		if !strings.Contains(log, want) {
			t.Fatalf("diagnostic = %q, missing %q", log, want)
		}
	}
}

func TestErrorRedactsCredentialsPrivateContentAndTerminalInjection(t *testing.T) {
	var output bytes.Buffer
	restore := SetOutput(&output)
	defer restore()

	Error("telegram.api", errors.Join(
		errors.New(`Post "https://api.telegram.org/bot123456:ABCSECRET/getUpdates?access_token=oauth-secret": private prompt content`),
		errors.New("private tool output\n\x1b[31mforged journal line"),
	), Field{Key: "status", Value: "502\nforged=true"})

	log := output.String()
	for _, unwanted := range []string{"123456:ABCSECRET", "oauth-secret", "private prompt content", "private tool output", "\x1b[31m", "forged journal line"} {
		if strings.Contains(log, unwanted) {
			t.Fatalf("diagnostic exposed %q: %q", unwanted, log)
		}
	}
	if strings.Count(log, "\n") != 1 {
		t.Fatalf("diagnostic = %q, want one bounded safe record", log)
	}
}

func TestErrorSkipsExpectedCancellationAndEOF(t *testing.T) {
	var output bytes.Buffer
	restore := SetOutput(&output)
	defer restore()

	Error("request", context.Canceled)
	Error("stream", fmt.Errorf("read stream: %w", io.EOF))

	if output.Len() != 0 {
		t.Fatalf("diagnostic = %q, want expected cancellation/EOF to remain quiet", output.String())
	}
}

func TestUntrustedMetadataAndErrorMessagesAreNeverCopied(t *testing.T) {
	var output bytes.Buffer
	restore := SetOutput(&output)
	defer restore()
	Error("provider.http", fmt.Errorf("transport: %w", errors.New(`{"token":"quoted-secret"} Bearer first Bearer second https://host/private/path?query=private-value`)),
		Field{Key: "request_id", Value: "private-request-id"}, Field{Key: "model", Value: "private-model"}, Field{Key: "status", Value: "502 injected-private"}, Field{Key: "backend_code", Value: "private-code"})
	log := output.String()
	for _, secret := range []string{"quoted-secret", "first", "second", "/private/path", "private-value", "private-request-id", "private-model", "private-code", "injected-private"} {
		if strings.Contains(log, secret) {
			t.Fatalf("diagnostic leaked %q: %q", secret, log)
		}
	}
	if !strings.Contains(log, `status="502"`) {
		t.Fatalf("missing safe status: %q", log)
	}
}

func TestRemoteMetadataAllowlist(t *testing.T) {
	var output bytes.Buffer
	restore := SetOutput(&output)
	defer restore()
	Error("provider.http", errors.New("unknown private failure"),
		Field{Key: "backend_type", Value: "invalid_request_error"}, Field{Key: "backend_code", Value: "model_not_found"},
		Field{Key: "request_id", Value: "req_abc12345"}, Field{Key: "backend_message", Value: "Unauthorized"},
		Field{Key: "backend_code", Value: "sk-private"}, Field{Key: "request_id", Value: "req-sk-private"},
		Field{Key: "request_id", Value: "req-abc\nforged"}, Field{Key: "backend_message", Value: "Unauthorized private prompt"})
	log := output.String()
	for _, want := range []string{`backend_type="invalid_request_error"`, `backend_code="model_not_found"`, `request_id="req_abc12345"`, `backend_message="Unauthorized"`} {
		if !strings.Contains(log, want) {
			t.Fatalf("missing %q: %q", want, log)
		}
	}
	for _, secret := range []string{"private", "forged", "prompt"} {
		if strings.Contains(log, secret) {
			t.Fatalf("leaked %q: %q", secret, log)
		}
	}
	if strings.Count(log, "\n") != 1 {
		t.Fatalf("injected log: %q", log)
	}
}

func TestMigrationDiagnosticMetadataAllowlist(t *testing.T) {
	var output bytes.Buffer
	restore := SetOutput(&output)
	defer restore()

	Error("migration.create", errors.New("private response body"),
		Field{Key: "stage", Value: "create"},
		Field{Key: "code", Value: "invalid_request"},
		Field{Key: "creation_state", Value: "not_created"},
		Field{Key: "stage", Value: "private-stage"},
		Field{Key: "code", Value: "private-code"},
		Field{Key: "creation_state", Value: "private-state"})

	log := output.String()
	for _, want := range []string{`stage="create"`, `code="invalid_request"`, `creation_state="not_created"`} {
		if !strings.Contains(log, want) {
			t.Fatalf("migration diagnostic = %q, missing %q", log, want)
		}
	}
	for _, unwanted := range []string{"private-stage", "private-code", "private-state", "/tmp/private", "private response body"} {
		if strings.Contains(log, unwanted) {
			t.Fatalf("migration diagnostic leaked %q: %q", unwanted, log)
		}
	}
}

func TestInfoKeepsLifecycleFieldsFixedAndSafe(t *testing.T) {
	var output bytes.Buffer
	restore := SetOutput(&output)
	defer restore()

	Info("migration.create",
		Field{Key: "event", Value: "http_dispatch"},
		Field{Key: "phase", Value: "dispatch"},
		Field{Key: "dispatch_state", Value: "dispatched"},
		Field{Key: "creation_state", Value: "unknown"},
		Field{Key: "code", Value: "response_unavailable"},
		Field{Key: "stage", Value: "service"},
		Field{Key: "handoff_delivery", Value: "not_attempted"},
		Field{Key: "server_name", Value: "goaster-migration-0123456789abcdef"},
		Field{Key: "result", Value: `{"token":"private"}`},
		Field{Key: "phase", Value: "private phase"})

	log := output.String()
	for _, want := range []string{`INFO operation=migration.create`, `event="http_dispatch"`, `phase="dispatch"`, `dispatch_state="dispatched"`, `creation_state="unknown"`, `code="response_unavailable"`, `stage="service"`, `handoff_delivery="not_attempted"`} {
		if !strings.Contains(log, want) {
			t.Fatalf("lifecycle info = %q, missing %q", log, want)
		}
	}
	for _, unwanted := range []string{"goaster-migration-0123456789abcdef", "private", "token", "result"} {
		if strings.Contains(log, unwanted) {
			t.Fatalf("lifecycle info leaked %q: %q", unwanted, log)
		}
	}
}

func TestMigrationTargetCodesAreAllowlisted(t *testing.T) {
	for _, code := range []string{"target_exists", "provider_lookup_failure"} {
		if got := MigrationCode(code); got != code {
			t.Fatalf("MigrationCode(%q) = %q", code, got)
		}
	}
	for _, code := range []string{"monitor_started", "monitor_stopped", "retry_confirmation_required"} {
		if got := MigrationCode(code); got != "" {
			t.Fatalf("retired MigrationCode(%q) = %q", code, got)
		}
	}
}
