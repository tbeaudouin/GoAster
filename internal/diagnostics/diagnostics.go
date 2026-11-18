// Package diagnostics writes bounded runtime failure records without exposing
// arbitrary error messages, paths, URLs, or remote content.
package diagnostics

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"syscall"
)

type Field struct{ Key, Value string }

var outputMu sync.Mutex
var output io.Writer = os.Stderr

func SetOutput(writer io.Writer) func() {
	outputMu.Lock()
	previous := output
	if writer == nil {
		output = os.Stderr
	} else {
		output = writer
	}
	outputMu.Unlock()
	return func() { outputMu.Lock(); output = previous; outputMu.Unlock() }
}

// Error records explicitly recognized causes and bounded protocol metadata.
// Unknown error messages are never copied.
func Error(operation string, err error, fields ...Field) {
	if err == nil || expectedOnly(err, 0) {
		return
	}
	// Attempt histories can be joined with other failures. Give each attempt
	// its own bounded record so later attempts cannot lose fields to the cap.
	var attempts []error
	otherLeaf := false
	var separate func(error, int)
	separate = func(e error, depth int) {
		if e == nil || depth > 32 {
			return
		}
		if _, ok := e.(interface{ DiagnosticSeparateRecord() }); ok {
			attempts = append(attempts, e)
			return
		}
		if joined, ok := e.(interface{ Unwrap() []error }); ok {
			for _, child := range joined.Unwrap() {
				separate(child, depth+1)
			}
			return
		}
		if child := errors.Unwrap(e); child != nil {
			separate(child, depth+1)
		} else {
			otherLeaf = true
		}
	}
	separate(err, 0)
	if len(attempts) > 0 {
		for _, attempt := range attempts {
			errorRecord(operation, attempt, nil, false)
		}
		if !otherLeaf {
			return
		}
	}
	errorRecord(operation, err, fields, len(attempts) > 0)
}

func errorRecord(operation string, err error, fields []Field, skipAttempts bool) {
	record := "ERROR operation=" + token(operation)
	var causes []string
	var walk func(error, int)
	walk = func(e error, depth int) {
		if e == nil || depth > 32 || len(causes) >= 32 {
			return
		}
		if _, ok := e.(interface{ DiagnosticSeparateRecord() }); ok && skipAttempts {
			return
		}
		name := reflect.TypeOf(e).String()
		// These labels are fixed strings; never copy the error's message.
		switch {
		case e == context.DeadlineExceeded:
			name += ": deadline exceeded"
		case e == io.ErrUnexpectedEOF:
			name += ": unexpected EOF"
		}
		for _, label := range []string{"encode agent request", "build agent request", "agent transport", "decode agent stream event", "scan agent response", "Telegram transport", "read Telegram response", "decode Telegram response", "Codex request", "read Codex response"} {
			if strings.HasPrefix(e.Error(), label+":") {
				name += ": " + label
				break
			}
		}
		if number, ok := e.(syscall.Errno); ok {
			name += ": " + number.Error()
		}
		switch cause := e.(type) {
		case *json.SyntaxError:
			name += fmt.Sprintf(": invalid JSON at byte %d", cause.Offset)
		case *json.UnmarshalTypeError:
			name += fmt.Sprintf(": JSON type mismatch at byte %d", cause.Offset)
		case *net.DNSError:
			switch {
			case cause.IsNotFound:
				name += ": DNS name not found"
			case cause.IsTimeout:
				name += ": DNS lookup timed out"
			case cause.IsTemporary:
				name += ": temporary DNS failure"
			default:
				switch cause.Err {
				case "server misbehaving", "no such host", "no suitable address found", "server failure":
					name += ": " + cause.Err
				default:
					name += ": DNS lookup failed"
				}
			}
		case *net.OpError:
			if cause.Timeout() {
				name += ": transport timed out"
			}
		case tls.RecordHeaderError:
			name += ": invalid TLS record header"
		case x509.UnknownAuthorityError:
			name += ": TLS certificate signed by unknown authority"
		case x509.HostnameError:
			name += ": TLS certificate hostname mismatch"
		case x509.CertificateInvalidError:
			name += ": invalid TLS certificate"
		}
		if description := knownDescription(e.Error()); description != "" {
			name += ": " + description
		}
		if detail, ok := e.(interface{ DiagnosticFields() []Field }); ok {
			// These fields use the same validation as caller context below.
			fields = append(fields, detail.DiagnosticFields()...)
		}
		causes = append(causes, name)
		if joined, ok := e.(interface{ Unwrap() []error }); ok {
			for _, child := range joined.Unwrap() {
				walk(child, depth+1)
			}
			return
		}
		walk(errors.Unwrap(e), depth+1)
	}
	walk(err, 0)
	for i, field := range fields {
		if i >= 32 {
			break
		}
		value := ""
		switch field.Key {
		case "status":
			if len(field.Value) >= 3 {
				if n, e := strconv.Atoi(field.Value[:3]); e == nil && n >= 100 && n <= 599 {
					value = field.Value[:3]
				}
			}
		case "request_id":
			value = RequestID(field.Value)
		case "configured_model":
			if len(field.Value) <= 96 && token(field.Value) == field.Value {
				value = field.Value
			}
		case "model_detail", "status_detail", "backend_type_detail", "backend_code_detail", "request_id_detail":
			value = safeDetailState(field.Value)
		case "attempt", "attempts":
			value = safeAttempt(field.Value)
		case "retry_decision":
			value = safeRetryDecision(field.Value)
		case "reason":
			value = knownDescription(field.Value)
		case "backend_message":
			value = RemoteMessage(field.Value)
		case "provider_message":
			value = MigrationProviderMessage(field.Value)
		case "provider_field":
			value = MigrationProviderFieldDetail(field.Value)
		case "backend_type":
			value = BackendValue(field.Value)
		case "backend_code":
			value = BackendValue(field.Value)
			if n, e := strconv.Atoi(field.Value); e == nil && n >= 100 && n <= 599 {
				value = strconv.Itoa(n)
			}
		case "stage":
			value = MigrationStage(field.Value)
		case "code":
			value = MigrationCode(field.Value)
		case "creation_state":
			value = MigrationCreationState(field.Value)
		case "event":
			value = safeEvent(field.Value)
		case "phase":
			value = safePhase(field.Value)
		case "dispatch_state":
			value = safeInfoField(field.Key, field.Value)
		case "handoff_delivery":
			value = safeInfoField(field.Key, field.Value)
		case "method":
			switch field.Value {
			case "agent probe", "getUpdates", "setMyCommands", "sendMessage", "editMessageText", "sendDocument", "getFile", "downloadAttachment", "downloadFile", "deleteMessage", "sendChatAction":
				value = field.Value
			}
		}
		if value != "" {
			record += " " + field.Key + "=" + strconv.Quote(value)
		}
	}

	record += " causes=" + strconv.Quote(strings.Join(causes, " | ")) + "\n"
	outputMu.Lock()
	_, _ = io.WriteString(output, record)
	outputMu.Unlock()
}

// Info records only fixed lifecycle metadata. It intentionally has no error
// message or arbitrary-value path; callers use it for bounded state journals.
func Info(operation string, fields ...Field) {
	record := "INFO operation=" + token(operation)
	for i, field := range fields {
		if i >= 16 {
			break
		}
		value := safeInfoField(field.Key, field.Value)
		if value != "" {
			record += " " + field.Key + "=" + strconv.Quote(value)
		}
	}
	record += "\n"
	outputMu.Lock()
	_, _ = io.WriteString(output, record)
	outputMu.Unlock()
}

func safeInfoField(key, value string) string {
	switch key {
	case "event":
		return safeEvent(value)
	case "phase":
		return safePhase(value)
	case "dispatch_state":
		switch value {
		case "pending", "dispatched", "returned":
			return value
		}
	case "creation_state":
		return MigrationCreationState(value)
	case "code":
		if value == "none" {
			return value
		}
		return MigrationCode(value)
	case "stage":
		if value == "none" {
			return value
		}
		return MigrationStage(value)
	case "handoff_delivery":
		switch value {
		case "not_attempted", "pending", "delivered", "failed", "unknown":
			return value
		}
	}
	return ""
}

func safeEvent(value string) string {
	switch value {
	case "entered", "http_dispatch", "result", "model_recovery", "tool_call", "post_tool_authentication", "skill_recovery", "error", "final":
		return value
	default:
		return ""
	}
}

func safePhase(value string) string {
	switch value {
	case "pre-stream", "stream", "post-stream", "create", "dispatch", "result", "handoff", "initial_notice", "model_command", "tool_observer", "session_error", "final_reply":
		return value
	default:
		return ""
	}
}

// MigrationStage retains only fixed stages from the active one-shot create
// contract.
func MigrationStage(value string) string {
	switch value {
	case "input", "configuration", "storage", "recipient", "request", "create", "handoff", "provider", "service", "tool", "unknown":
		return value
	default:
		return ""
	}
}

// MigrationCode retains only fixed codes from the active one-shot create
// contract.
func MigrationCode(value string) string {
	switch value {
	case "credentials_unavailable", "key_generation_failed", "configuration_invalid", "request_invalid", "handoff_failed", "provider_timeout", "provider_dns_failure", "provider_tls_failure", "provider_transport_failure", "invalid_request", "operation_conflict", "creation_uncertain", "storage_unavailable", "rate_limited", "unauthorized", "unavailable",
		"no_capacity", "target_exists", "provider_lookup_failure", "invalid_input", "response_unavailable", "response_unusable", "invalid_arguments", "capability_unavailable", "private_conversation_required", "owner_unavailable", "request_cancelled", "already_attempted", "claim_failed", "missing_hcloud_token", "credential_storage_unreadable", "invalid_hcloud_token", "protected_storage_failure", "recipient_unavailable", "recipient_changed", "handoff_failure", "cancelled", "timeout", "dns_failure", "tls_failure", "transport_failure", "invalid_response", "web_auth_rejected", "web_http_error", "provider_auth_rejected", "provider_http_error", "upstream_unknown", "created", "created_delivery_failed", "manual_review", "unknown_failure":
		return value
	default:
		return ""
	}
}

// MigrationCreationState retains only the bounded creation states used by
// migration diagnostics.
func MigrationCreationState(value string) string {
	switch value {
	case "created", "not_created", "unknown":
		return value
	default:
		return ""
	}
}

// MigrationProviderMessage retains only fixed, safe provider explanations
// supplied by the migration Web contract. Arbitrary provider text is never
// copied into a model result, persisted diagnostic, or runtime log.
func MigrationProviderMessage(value string) string {
	if len(value) > 160 {
		return ""
	}
	switch value {
	case "invalid", "invalid input", "invalid_input", "invalid request",
		"invalid server type", "invalid location", "validation failed", "provider rejected request",
		"server type not found", "server type is invalid", "server type is not available",
		"server type is unavailable", "server type unavailable", "location not found", "location is invalid",
		"location is not available", "location unavailable", "no compatible location", "no compatible locations",
		"no capacity", "capacity unavailable", "name already exists",
		"resource not found", "resource unavailable", "unauthorized", "forbidden",
		"rate limit exceeded", "request timed out", "internal server error",
		"the requested server type is not available", "the requested location is not available",
		"the requested server type or location is invalid",
		"the requested server type is not available in the requested location",
		"the requested server type is not available in the requested locations",
		"the requested location is not available for the requested server type",
		"the requested server type is not available in any requested location",
		"the requested location is not available for the server type":
		return value
	default:
		return ""
	}
}

// MigrationProviderFieldName retains the small fixed set of request fields
// whose provider validation details are safe and useful to expose.
func MigrationProviderFieldName(value string) string {
	if len(value) > 64 {
		return ""
	}
	switch value {
	case "server_type", "server_name", "name", "location", "locations", "image", "ssh_keys", "login_public_key", "datacenter", "public_net", "server":
		return value
	default:
		return ""
	}
}

// MigrationProviderFieldMessage retains fixed provider validation messages.
func MigrationProviderFieldMessage(value string) string {
	if len(value) > 160 {
		return ""
	}
	switch value {
	case "required", "is required", "invalid", "is invalid", "unavailable", "is unavailable", "not available", "is not available", "not found", "does not exist", "already exists",
		"must not be empty", "must be a valid server type", "must be a valid location",
		"must be available", "not available in this location", "not available in the requested location", "not available in the requested locations",
		"cannot be used with the selected server type", "has an invalid format", "is too long",
		"must be unique", "must contain at least one item", "must contain at most 64 items",
		"must be a string", "must be an array", "must be a boolean", "is not supported":
		return value
	default:
		return ""
	}
}

// MigrationProviderFieldValue creates a bounded safe representation for one
// provider field and its fixed messages. The representation is used only for
// diagnostics; it is not a copy of the provider response.
func MigrationProviderFieldValue(field string, messages []string) string {
	safeField := MigrationProviderFieldName(field)
	if safeField == "" || len(messages) == 0 || len(messages) > 8 {
		return ""
	}
	safeMessages := make([]string, 0, len(messages))
	seen := make(map[string]struct{}, len(messages))
	for _, message := range messages {
		safeMessage := MigrationProviderFieldMessage(message)
		if safeMessage == "" {
			continue
		}
		if _, exists := seen[safeMessage]; exists {
			continue
		}
		seen[safeMessage] = struct{}{}
		safeMessages = append(safeMessages, safeMessage)
	}
	if len(safeMessages) == 0 {
		return ""
	}
	return safeField + "=" + strings.Join(safeMessages, "|")
}

// MigrationProviderFieldDetail validates the bounded log representation made
// by MigrationProviderFieldValue before it is copied to an ERROR record.
func MigrationProviderFieldDetail(value string) string {
	if len(value) > 512 {
		return ""
	}
	parts := strings.Split(value, "=")
	if len(parts) != 2 {
		return ""
	}
	return MigrationProviderFieldValue(parts[0], strings.Split(parts[1], "|"))
}

func token(s string) string {
	if len(s) > 96 {
		s = s[:96]
	}
	var b strings.Builder
	for _, c := range s {
		if c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || strings.ContainsRune("._-", c) {
			b.WriteRune(c)
		} else {
			b.WriteByte('_')
		}
	}
	if b.Len() == 0 {
		return "unknown"
	}
	return b.String()
}

func safeDetailState(value string) string {
	switch value {
	case "absent", "unrecognized", "present":
		return value
	default:
		return ""
	}
}

func safeAttempt(value string) string {
	number, err := strconv.Atoi(value)
	if err != nil || number < 1 || number > 2 {
		return ""
	}
	return strconv.Itoa(number)
}

func safeRetryDecision(value string) string {
	switch value {
	case "not_retryable", "retry_scheduled", "retry_exhausted", "retry_canceled", "retry_wait_failed":
		return value
	default:
		return ""
	}
}

func expectedOnly(err error, depth int) bool {
	if err == nil {
		return true
	}
	if depth > 32 {
		return false
	}
	if joined, ok := err.(interface{ Unwrap() []error }); ok {
		children := joined.Unwrap()
		if len(children) == 0 {
			return false
		}
		for _, child := range children {
			if !expectedOnly(child, depth+1) {
				return false
			}
		}
		return true
	}
	if child := errors.Unwrap(err); child != nil {
		return expectedOnly(child, depth+1)
	}
	return err == context.Canceled || err == io.EOF
}

// BackendValue accepts known protocol classifications, never arbitrary strings.
func BackendValue(value string) string {
	switch value {
	case "invalid_request_error", "authentication_error", "auth_error", "permission_error", "not_found_error", "rate_limit_error", "server_error", "api_error", "provider_error", "backend_error", "gateway_rejected",
		"invalid_api_key", "invalid_authentication", "token_expired", "invalid_token", "insufficient_quota", "rate_limit_exceeded", "model_not_found", "model_not_available", "context_length_exceeded", "invalid_json", "content_policy_violation", "content_filter", "request_timeout", "timeout", "overloaded", "internal_error", "invalid_grant", "invalid_client", "access_denied", "authorization_pending", "slow_down", "expired_token", "unsupported_country_region_territory", "invalid_input", "resource_unavailable", "uniqueness_error", "not_found", "unauthorized", "forbidden", "service_error":
		return value
	}
	return ""
}

// RequestID retains standard opaque request IDs only, with no URL/control data.
func RequestID(value string) string {
	if len(value) < 8 || len(value) > 80 {
		return ""
	}
	lower := strings.ToLower(value)
	for _, marker := range []string{"sk-", "sk_", "bearer", "token", "secret", "password"} {
		if strings.Contains(lower, marker) {
			return ""
		}
	}
	if strings.HasPrefix(value, "req_") || strings.HasPrefix(value, "req-") {
		for _, c := range value {
			if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-' || c == '_') {
				return ""
			}
		}
		return value
	}
	if len(value) == 36 {
		for i, c := range value {
			if i == 8 || i == 13 || i == 18 || i == 23 {
				if c != '-' {
					return ""
				}
				continue
			}
			if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f' || c >= 'A' && c <= 'F') {
				return ""
			}
		}
		return value
	}
	return ""
}

func knownDescription(message string) string {
	// Exact matches against application-owned literals. Never emit a suffix or
	// interpolate a value from an arbitrary error string.
	switch message {
	case "Codex backend request failed", "Codex stream failed", "Codex response failed: backend returned an error", "device code request failed", "device authentication failed", "token exchange failed", "saved refresh token was rejected", "token refresh failed", "Telegram rejected message formatting",
		"temporary HTTP status", "transient network error", "quota exhausted", "authentication error", "ordinary HTTP status", "request canceled", "request deadline exceeded", "malformed response", "received stream failure", "TLS or configuration error", "non-transient transport error",
		"OpenAI subscription authentication required: credentials were not found", "OpenAI subscription authentication required", "request authentication failed",
		"OpenAI subscription authentication required: credential file has no access token",
		"OpenAI subscription authentication required: saved credentials have no refresh token",
		"OpenAI subscription authentication required: saved access token is expired and has no refresh token",
		"HCLOUD_TOKEN is missing", "HCLOUD_TOKEN is invalid", "migration credential storage is unreadable",
		"credential path is empty", "credential file must not be a symbolic link", "token response has no access token",
		"authentication checker is unavailable", "chat provider is nil", "too many attachments", "invalid conversation history",
		"Codex response is missing", "Codex response body is missing", "OpenAI response is missing", "OpenAI response body is missing",
		"Codex response failed", "Codex response stream ended before completion", "Codex response contained no output text", "Codex response contained no output",
		"Codex requested a tool in a tool-free request", "request contains multiple JSON values",
		"Telegram client is unavailable", "Telegram response body is missing", "Telegram response contained no update list", "Telegram response body exceeds the limit",
		"remote agent client is unavailable", "agent response body is missing", "agent response contained no final reply",
		"attachment has no Telegram file ID", "attachment exceeds the size limit", "Telegram file metadata is invalid or exceeds the size limit",
		"Telegram file response is missing", "Telegram file response body is missing", "Telegram file download returned an unacceptable response", "Telegram file exceeds the size limit", "HCLOUD_TOKEN is not configured":
		return message
	}
	return ""
}

// RemoteMessage preserves exact common fixed rejection text; messages containing
// interpolated request content are deliberately excluded.
func RemoteMessage(value string) string {
	switch value {
	case "Unauthorized", "Internal server error", "Rate limit exceeded", "Bad Request: chat not found", "Bad Request: message is not modified", "Forbidden: bot was blocked by the user", "Forbidden: bot was kicked from the group chat", "Conflict: terminated by other getUpdates request; make sure that only one bot instance is running", "Conflict: can't use getUpdates method while webhook is active; use deleteWebhook to delete the webhook first":
		return value
	}
	return ""
}
