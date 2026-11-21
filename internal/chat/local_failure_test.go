package chat

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"goaster/internal/diagnostics"
)

func TestLocalFailureDiagnosticAndMessageRedactCause(t *testing.T) {
	secret := "private-arguments private-output secret-token\nINJECTED"
	cause := errors.New(secret)
	for _, tc := range []struct{ reason, message string }{
		{"tool_free_request", "tool-free request"}, {"unsupported_tool", "unsupported tool"},
		{"web_search_limit", "too many web_search"}, {"web_fetch_limit", "too many web_fetch"},
		{"skill_budget", "conversation budget"}, {"skill_checkpoint", "checkpoint could not be saved"},
		{"evidence_save", "Tool evidence could not be saved"},
	} {
		t.Run(tc.reason, func(t *testing.T) {
			local := localFailure{reason: tc.reason, cause: fmt.Errorf("%s: %w", secret, cause)}
			err := ModelStreamError{Classification: modelRequestFailureClassification, Cause: local}
			log := captureChatDiagnostics(t, func() { diagnostics.Error("chat.session.reply", err) })
			for _, value := range []string{err.Error(), ModelStreamMessageForError(err, ""), requestFailureDiagnostic(err), requestFailureDiagnostic(local), log} {
				if strings.Contains(value, "private-") || strings.Contains(value, "secret-token") || strings.Contains(value, "INJECTED") {
					t.Fatalf("private cause leaked: %q", value)
				}
			}
			if !strings.Contains(log, `ERROR operation=chat.session.reply`) || !strings.Contains(log, `local_reason="`+tc.reason+`"`) || !strings.Contains(log, `local_stage="post_stream"`) {
				t.Fatalf("diagnostic = %q", log)
			}
			if !strings.Contains(ModelStreamMessageForError(err, ""), tc.message) || !errors.Is(err, cause) {
				t.Fatalf("message or wrapped cause missing: %v", err)
			}
		})
	}
	handler := ModelStreamError{Classification: modelRequestFailureClassification, Cause: toolHandlerError{cause: cause}}
	log := captureChatDiagnostics(t, func() { diagnostics.Error("chat.session.reply", handler) })
	if !strings.Contains(log, `local_stage="tool_handler"`) || !strings.Contains(log, `local_reason="handler_failed"`) || strings.Contains(log, secret) || !strings.Contains(ModelStreamMessageForError(handler, ""), "Tool handler failed") {
		t.Fatalf("handler diagnostic/message: %q %q", log, ModelStreamMessageForError(handler, ""))
	}
	save := conversationSaveError{operation: "save tool evidence", cause: cause}
	log = captureChatDiagnostics(t, func() { diagnostics.Error("chat.session.reply", save) })
	if !strings.Contains(log, `save_operation="save tool evidence"`) || strings.Contains(log, secret) || strings.Contains(requestFailureDiagnostic(save), secret) || !errors.Is(save, cause) {
		t.Fatalf("save diagnostic/message: %q %q", log, requestFailureDiagnostic(save))
	}
}
