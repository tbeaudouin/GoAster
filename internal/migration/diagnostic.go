package migration

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"time"

	"goaster/internal/diagnostics"
	"goaster/internal/protectedfile"
)

const (
	stageInput         = "input"
	stageConfiguration = "configuration"
	stageStorage       = "storage"
	stageRecipient     = "recipient"
	stageMonitor       = "monitor"
	stageAvailability  = "availability"
	stageCreate        = "create"
	stageHandoff       = "handoff"
	stageProvider      = "provider"
	stageStatus        = "status"
	stageStop          = "stop"
	stageTool          = "tool"
	stageUnknown       = "unknown"

	codeInvalidArguments           = "invalid_arguments"
	codeCapabilityUnavailable      = "capability_unavailable"
	codeMissingToken               = "missing_hcloud_token"
	codeCredentialUnreadable       = "credential_storage_unreadable"
	codeInvalidToken               = "invalid_hcloud_token"
	codeProtectedStorage           = "protected_storage_failure"
	codeRecipientUnavailable       = "recipient_unavailable"
	codeRecipientChanged           = "recipient_changed"
	codeHandoffFailure             = "handoff_failure"
	codeCancelled                  = "cancelled"
	codeTimeout                    = "timeout"
	codeDNSFailure                 = "dns_failure"
	codeTLSFailure                 = "tls_failure"
	codeTransportFailure           = "transport_failure"
	codeInvalidResponse            = "invalid_response"
	codeWebAuthRejected            = "web_auth_rejected"
	codeWebHTTPError               = "web_http_error"
	codeProviderAuthRejected       = "provider_auth_rejected"
	codeProviderHTTPError          = "provider_http_error"
	codeUpstreamUnknown            = "upstream_unknown"
	codeMonitorState               = "monitor_state_failure"
	codeMonitorNotFound            = "monitor_not_found"
	codeMonitorInProgress          = "monitor_in_progress"
	codeMonitorStopUnconfirmed     = "monitor_stop_unconfirmed"
	codePersistedStateInvalid      = "persisted_state_invalid"
	codeMonitorStarted             = "monitor_started"
	codeMonitorStopped             = "monitor_stopped"
	codeRetryConfirmation          = "retry_confirmation_required"
	codeCreated                    = "created"
	codeCreatedDeliveryFailed      = "created_delivery_failed"
	codeAvailabilityRetryExhausted = "availability_retry_exhausted"
	codeManualReview               = "manual_review"
	codeUnknownFailure             = "unknown_failure"

	resultMonitoring      = "monitoring"
	resultCreated         = "created"
	resultManualReview    = "manual_review"
	resultStopped         = "stopped"
	resultInProgress      = "in_progress"
	resultStopUnconfirmed = "stop_unconfirmed"
	resultRejected        = "rejected"
	resultUnavailable     = "unavailable"
	resultError           = "error"

	creationCreated    = "created"
	creationNotCreated = "not_created"
	creationUnknown    = "unknown"

	monitoringRunning    = "running"
	monitoringStopped    = "stopped"
	monitoringUnknown    = "unknown"
	monitoringNotStarted = "not_started"

	retryStateNone      = "none"
	retryStatePending   = "pending"
	retryStateExhausted = "exhausted"

	availabilityRetryLimit            = 3
	retryPendingNextAction            = "wait for the next 30-minute availability check; the bounded availability retry is automatic"
	retryConfirmationNextAction       = "The previous attempt may already have created a server. Trying again could create a duplicate and add charges. I recommend checking your Hetzner account for duplicate servers. Are you sure you want me to try again?"
	legacyRetryConfirmationNextAction = "ask the owner to confirm another create for this exact prior attempt before calling start"
	handoffFailureNextAction          = "preserve the reported handoff delivery state; retrieve a retained protected handoff only if needed; do not resend automatically"
	legacyHandoffFailureNextAction    = "inspect the retained protected handoff manually; do not resend automatically"
	createdDeliveryFailedNextAction   = "the server was already created and handoff delivery failed; retrieve the retained protected handoff only if needed; do not resend automatically"
	legacyCreatedDeliveryFailedAction = "retrieve the retained protected handoff manually; do not resend automatically"
	legacyServiceNextAction           = "inspect status and the named service before any new owner-approved attempt; do not retry an uncertain create"
	legacyInvalidResponseNextAction   = "inspect the counterpart status and provider project before any new owner-approved attempt"
	legacyManualReviewNextAction      = "inspect status and reconcile external state before any new attempt"
	uncertainNextAction               = "Status and provider account checks are optional. If creation is unknown or already created, say: " + retryConfirmationNextAction + " After one yes, start exactly one fresh attempt using the exact prior monitor reference from the current retry-required result; never retry automatically. Respect retained-history, in-flight, and unconfirmed-stop blockers."

	maxDiagnosticBytes      = 4 * 1024
	maxDiagnosticTimestamp  = int64(4102444800) // 2100-01-01 UTC
	diagnosticFileSeparator = ".diagnostic."
	diagnosticFileSuffix    = ".json"
)

// Diagnostic is the bounded, model-safe description of one migration result.
// All free-form causes are deliberately excluded. The runtime may retain the
// wrapped cause for safe ERROR diagnostics, but it never copies that cause to
// this value or to the scheduler's public status.
type Diagnostic struct {
	MonitorID         string `json:"monitor_id,omitempty"`
	Stage             string `json:"stage"`
	Code              string `json:"code"`
	Cause             string `json:"cause"`
	HTTPStatus        int    `json:"http_status,omitempty"`
	ProviderCode      string `json:"provider_code,omitempty"`
	ResultState       string `json:"result_state"`
	CreationState     string `json:"creation_state"`
	MonitoringState   string `json:"monitoring_state"`
	MonitoringStopped bool   `json:"monitoring_stopped"`
	NextAction        string `json:"next_action"`
	RetryCount        int    `json:"retry_count"`
	RetryState        string `json:"retry_state"`
	UpdatedAt         int64  `json:"updated_at,omitempty"`
}

var (
	// These sentinels preserve safe classification while retaining the wrapped
	// storage, transport, or provider cause for diagnostics.Error.
	ErrCredentialStorageUnreadable      = errors.New("migration credential storage is unreadable")
	ErrHetznerTokenInvalid              = errors.New("HCLOUD_TOKEN is invalid")
	ErrProtectedStorage                 = errors.New("migration protected storage failed")
	ErrRecipientUnavailable             = errors.New("migration recipient is unavailable")
	ErrRecipientChanged                 = errors.New("migration recipient changed")
	ErrHandoffFailure                   = errors.New("migration handoff failed")
	ErrInvalidResponse                  = errors.New("migration response is invalid")
	ErrMonitorStopUnconfirmed           = errors.New("migration monitor stop is unconfirmed")
	ErrMonitorNotFound                  = errors.New("migration monitor was not found")
	ErrPersistedStateInvalid            = errors.New("migration persisted diagnostic state is invalid")
	ErrRetryConfirmationRequired        = errors.New("migration retry confirmation is required")
	errAvailabilityRetryBudgetExhausted = errors.New("migration availability retry budget exhausted")
)

type migrationFailure struct {
	diagnostic Diagnostic
	cause      error
}

func (failure migrationFailure) Error() string {
	return failure.diagnostic.Cause
}

func (failure migrationFailure) Unwrap() error { return failure.cause }

func (failure migrationFailure) MigrationDiagnostic() Diagnostic {
	return failure.diagnostic
}

type diagnosticCarrier interface {
	MigrationDiagnostic() Diagnostic
}

// webHTTPError carries only the response status and allowlisted envelope
// fields. The counterpart's body is intentionally never retained or decoded
// into a user-facing cause.
type webHTTPError struct {
	Status       int
	Code         string
	Stage        string
	ProviderCode string
}

func (failure webHTTPError) Error() string { return "migration Web service returned an HTTP status" }

type webTransportError struct{ cause error }

func (failure webTransportError) Error() string { return "migration Web transport failed" }

func (failure webTransportError) Unwrap() error { return failure.cause }

type webResponseError struct{ cause error }

func (failure webResponseError) Error() string { return "migration Web response was invalid" }

func (failure webResponseError) Unwrap() error { return failure.cause }

type providerTransportError struct{ cause error }

func (failure providerTransportError) Error() string { return "provider transport failed" }

func (failure providerTransportError) Unwrap() error { return failure.cause }

type providerResponseError struct{ cause error }

func (failure providerResponseError) Error() string { return "provider response was invalid" }

func (failure providerResponseError) Unwrap() error { return failure.cause }

func failureResult(stage string, err error, result Result) (Result, error) {
	if err == nil {
		return NormalizeResult(result), nil
	}
	diagnostic := diagnosticForFailure(stage, err, result)
	result.Diagnostic = diagnostic
	if result.Status == "" {
		result.Status = diagnostic.ResultState
	}
	return result, migrationFailure{diagnostic: diagnostic, cause: err}
}

func failureError(stage string, err error, result Result) error {
	if err == nil {
		return nil
	}
	return migrationFailure{diagnostic: diagnosticForFailure(stage, err, result), cause: err}
}

func failureWithDiagnostic(diagnostic Diagnostic, cause error) error {
	return migrationFailure{diagnostic: normalizeDiagnostic(diagnostic, diagnostic.Stage, Result{MonitorID: diagnostic.MonitorID, Status: diagnostic.ResultState, Diagnostic: diagnostic}), cause: cause}
}

func logMigrationFailure(operation string, err error, diagnostic Diagnostic) {
	fields := make([]diagnostics.Field, 0, 2)
	if diagnostic.HTTPStatus != 0 {
		fields = append(fields, diagnostics.Field{Key: "status", Value: fmt.Sprintf("%d", diagnostic.HTTPStatus)})
	}
	if diagnostic.ProviderCode != "" {
		fields = append(fields, diagnostics.Field{Key: "backend_code", Value: diagnostic.ProviderCode})
	}
	diagnostics.Error(operation, err, fields...)
}

// DiagnosticFor returns a safe diagnostic even when a caller supplied an
// unclassified error. It never includes the error text.
func DiagnosticFor(err error, result Result) Diagnostic {
	if err == nil {
		return NormalizeResult(result).Diagnostic
	}
	return diagnosticForFailure(stageUnknown, err, result)
}

// InvalidArgumentsDiagnostic returns the fixed safe diagnostic used when the
// model-facing action envelope is rejected before an operation is selected.
func InvalidArgumentsDiagnostic() Diagnostic {
	return diagnosticTemplate(stageInput, codeInvalidArguments)
}

// CapabilityUnavailableDiagnostic returns the fixed safe diagnostic used when
// the local migration control plane is not ready.
func CapabilityUnavailableDiagnostic() Diagnostic {
	return diagnosticTemplate(stageTool, codeCapabilityUnavailable)
}

// IsMigrationFailure reports whether the migration boundary already wrapped
// an error with a bounded diagnostic. The chat boundary uses this to avoid a
// second identical ERROR record while retaining failures from injected seams.
func IsMigrationFailure(err error) bool {
	return findDiagnosticCarrier(err).Code != ""
}

// NormalizeResult strips any untrusted diagnostic fields supplied by a seam
// and fills the safe state fields required by model-facing results.
func NormalizeResult(result Result) Result {
	if result.MonitorID != "" && !safeJobID(result.MonitorID) {
		result.MonitorID = ""
	}
	result.Diagnostic = normalizeDiagnostic(result.Diagnostic, stageForResult(result), result)
	if result.MonitorID == "" && result.Diagnostic.MonitorID != "" {
		result.MonitorID = result.Diagnostic.MonitorID
	}
	if result.Diagnostic.MonitorID == "" && safeJobID(result.MonitorID) {
		result.Diagnostic.MonitorID = result.MonitorID
	}
	return result
}

// NormalizeStatusResult applies the same safe validation to a persisted status
// result before it is returned to the model.
func NormalizeStatusResult(result StatusResult) StatusResult {
	if result.MonitorID != "" && !safeJobID(result.MonitorID) {
		result.MonitorID = ""
	}
	result.Migration = NormalizeResult(result.Migration)
	result.Diagnostic = normalizeDiagnostic(result.Diagnostic, stageForResult(result.Migration), result.Migration)
	if result.MonitorID == "" && safeJobID(result.Migration.MonitorID) {
		result.MonitorID = result.Migration.MonitorID
	}
	if result.Diagnostic.MonitorID == "" && safeJobID(result.MonitorID) {
		result.Diagnostic.MonitorID = result.MonitorID
	}
	return result
}

func diagnosticForFailure(stage string, err error, result Result) Diagnostic {
	if carrier := findDiagnosticCarrier(err); carrier.Code != "" {
		diagnostic := normalizeDiagnostic(carrier, stage, result)
		if diagnostic.MonitorID == "" && safeJobID(result.MonitorID) {
			diagnostic.MonitorID = result.MonitorID
		}
		return diagnostic
	}

	stage = safeStage(stage)
	code, status, providerCode := classifyFailure(err, stage)
	var webStatus webHTTPError
	if errors.As(err, &webStatus) && webStatus.Code != "" {
		switch webStatus.Stage {
		case "provider":
			stage = stageProvider
		case "request":
			stage = stageInput
		case "storage":
			stage = stageStorage
		case "create":
			stage = stageCreate
		}
	}
	diagnostic := diagnosticTemplate(stage, code)
	diagnostic.HTTPStatus = status
	diagnostic.ProviderCode = providerCode
	diagnostic.ResultState = resultStateFor(result, diagnostic.ResultState)
	diagnostic.CreationState = creationStateFor(result, code)
	diagnostic.MonitoringState = monitoringStateFor(result, code)
	diagnostic.MonitoringStopped = diagnostic.MonitoringState == monitoringStopped
	diagnostic.MonitorID = result.MonitorID
	return normalizeDiagnostic(diagnostic, stage, result)
}

func findDiagnosticCarrier(err error) Diagnostic {
	var found Diagnostic
	var walk func(error, int)
	walk = func(current error, depth int) {
		if current == nil || depth > 32 || found.Code != "" {
			return
		}
		if carrier, ok := current.(diagnosticCarrier); ok {
			found = carrier.MigrationDiagnostic()
			return
		}
		if joined, ok := current.(interface{ Unwrap() []error }); ok {
			for _, child := range joined.Unwrap() {
				walk(child, depth+1)
			}
			return
		}
		walk(errors.Unwrap(current), depth+1)
	}
	walk(err, 0)
	return found
}

func classifyFailure(err error, stage string) (string, int, string) {
	if errors.Is(err, context.Canceled) {
		return codeCancelled, 0, ""
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return codeTimeout, 0, ""
	}
	switch {
	case errors.Is(err, ErrHetznerTokenMissing):
		return codeMissingToken, 0, ""
	case errors.Is(err, ErrCredentialStorageUnreadable):
		return codeCredentialUnreadable, 0, ""
	case errors.Is(err, ErrHetznerTokenInvalid):
		return codeInvalidToken, 0, ""
	case errors.Is(err, ErrProtectedStorage):
		return codeProtectedStorage, 0, ""
	case errors.Is(err, ErrRecipientChanged):
		return codeRecipientChanged, 0, ""
	case errors.Is(err, ErrRecipientUnavailable):
		return codeRecipientUnavailable, 0, ""
	case errors.Is(err, ErrHandoffFailure):
		return codeHandoffFailure, 0, ""
	case errors.Is(err, ErrInvalidResponse), errors.Is(err, errInvalidRequest):
		return codeInvalidResponse, 0, ""
	case errors.Is(err, ErrMonitorStopUnconfirmed):
		return codeMonitorStopUnconfirmed, 0, ""
	case errors.Is(err, ErrMonitorNotFound):
		return codeMonitorNotFound, 0, ""
	case errors.Is(err, ErrPersistedStateInvalid):
		return codePersistedStateInvalid, 0, ""
	case errors.Is(err, ErrRetryConfirmationRequired):
		return codeRetryConfirmation, 0, ""
	case errors.Is(err, context.Canceled):
		return codeCancelled, 0, ""
	}

	var webStatus webHTTPError
	if errors.As(err, &webStatus) {
		if webStatus.Code != "" {
			return webStatus.Code, webStatus.Status, webStatus.ProviderCode
		}
		if webStatus.Status == 401 || webStatus.Status == 403 {
			return codeWebAuthRejected, webStatus.Status, ""
		}
		return codeWebHTTPError, webStatus.Status, ""
	}
	var providerStatus providerHTTPError
	if errors.As(err, &providerStatus) {
		if providerStatus.Status == 401 || providerStatus.Status == 403 {
			return codeProviderAuthRejected, providerStatus.Status, safeProviderCode(providerStatus.Code)
		}
		return codeProviderHTTPError, providerStatus.Status, safeProviderCode(providerStatus.Code)
	}
	var response webResponseError
	if errors.As(err, &response) {
		return codeInvalidResponse, 0, ""
	}
	var providerResponse providerResponseError
	if errors.As(err, &providerResponse) {
		return codeInvalidResponse, 0, ""
	}

	var dnsError *net.DNSError
	if errors.As(err, &dnsError) {
		return codeDNSFailure, 0, ""
	}
	var operationError *net.OpError
	if errors.As(err, &operationError) && operationError.Timeout() {
		return codeTimeout, 0, ""
	}
	var recordError tls.RecordHeaderError
	if errors.As(err, &recordError) {
		return codeTLSFailure, 0, ""
	}
	var unknownAuthority x509.UnknownAuthorityError
	if errors.As(err, &unknownAuthority) {
		return codeTLSFailure, 0, ""
	}
	var hostnameError x509.HostnameError
	if errors.As(err, &hostnameError) {
		return codeTLSFailure, 0, ""
	}
	var certificateError x509.CertificateInvalidError
	if errors.As(err, &certificateError) {
		return codeTLSFailure, 0, ""
	}
	var webTransport webTransportError
	if errors.As(err, &webTransport) {
		if errors.Is(err, context.DeadlineExceeded) {
			return codeTimeout, 0, ""
		}
		return codeTransportFailure, 0, ""
	}
	var providerTransport providerTransportError
	if errors.As(err, &providerTransport) {
		if errors.Is(err, context.DeadlineExceeded) {
			return codeTimeout, 0, ""
		}
		return codeTransportFailure, 0, ""
	}

	if stage == stageMonitor || stage == stageStatus || stage == stageStop {
		if errors.Is(err, errManualReview) {
			return codeMonitorState, 0, ""
		}
		return codeMonitorState, 0, ""
	}
	return codeUpstreamUnknown, 0, ""
}

func diagnosticTemplate(stage, code string) Diagnostic {
	stage = safeStage(stage)
	if !knownDiagnosticCode(code) {
		code = codeUnknownFailure
	}
	return Diagnostic{
		Stage:           stage,
		Code:            code,
		Cause:           causeForCode(code),
		ResultState:     resultStateForCode(code),
		CreationState:   creationStateForCode(code),
		MonitoringState: monitoringStateForCode(code),
		NextAction:      nextActionForCode(code),
		RetryState:      retryStateNone,
	}
}

func normalizeDiagnostic(input Diagnostic, stage string, result Result) Diagnostic {
	if !knownDiagnosticCode(input.Code) {
		input = diagnosticTemplate(safeStage(stage), codeForResult(result))
	}
	if input.Stage == "" {
		input.Stage = safeStage(stage)
	}
	if !knownStage(input.Stage) {
		input.Stage = safeStage(stage)
	}
	template := diagnosticTemplate(input.Stage, input.Code)
	if input.Cause != template.Cause {
		input.Cause = template.Cause
	}
	if input.RetryState == "" && input.RetryCount == 0 {
		input.RetryState = retryStateNone
	}
	if !validRetryState(input.RetryState, input.RetryCount) {
		input.RetryCount = 0
		input.RetryState = retryStateNone
	}
	if input.RetryState == retryStatePending {
		input.NextAction = retryPendingNextAction
	} else if input.NextAction != template.NextAction {
		input.NextAction = template.NextAction
	}
	if !knownResultState(input.ResultState) {
		input.ResultState = resultStateFor(result, template.ResultState)
	}
	if !knownCreationState(input.CreationState) {
		input.CreationState = creationStateFor(result, input.Code)
	}
	if !knownMonitoringState(input.MonitoringState) {
		input.MonitoringState = monitoringStateFor(result, input.Code)
	}
	if input.MonitoringState != monitoringStopped {
		input.MonitoringStopped = false
	} else {
		input.MonitoringStopped = true
	}
	if !safeJobID(input.MonitorID) {
		input.MonitorID = ""
	}
	if input.HTTPStatus < 0 || input.HTTPStatus > 599 || (input.HTTPStatus > 0 && input.HTTPStatus < 100) {
		input.HTTPStatus = 0
	}
	input.ProviderCode = safeProviderCode(input.ProviderCode)
	if input.UpdatedAt < 0 || input.UpdatedAt > maxDiagnosticTimestamp {
		input.UpdatedAt = 0
	}
	return input
}

func validRetryState(state string, count int) bool {
	switch state {
	case retryStateNone:
		return count == 0
	case retryStatePending:
		return count > 0 && count < availabilityRetryLimit
	case retryStateExhausted:
		return count == availabilityRetryLimit
	default:
		return false
	}
}

func codeForResult(result Result) string {
	switch result.Status {
	case resultMonitoring:
		return codeMonitorStarted
	case resultCreated:
		if result.Delivered || result.Diagnostic.CreationState == creationCreated && !result.ManualReview {
			return codeCreated
		}
		return codeCreatedDeliveryFailed
	case resultStopped:
		return codeMonitorStopped
	case resultInProgress:
		return codeMonitorInProgress
	case resultStopUnconfirmed:
		return codeMonitorStopUnconfirmed
	case resultManualReview:
		return codeManualReview
	case resultRejected:
		return codeInvalidArguments
	case resultUnavailable:
		return codeCapabilityUnavailable
	default:
		return codeUnknownFailure
	}
}

func stageForResult(result Result) string {
	if result.Diagnostic.Stage != "" {
		return safeStage(result.Diagnostic.Stage)
	}
	switch result.Status {
	case resultCreated:
		return stageCreate
	case resultMonitoring, resultStopped, resultInProgress, resultStopUnconfirmed:
		return stageMonitor
	default:
		return stageUnknown
	}
}

func resultStateFor(result Result, fallback string) string {
	if knownResultState(result.Status) {
		return result.Status
	}
	if knownResultState(result.Diagnostic.ResultState) {
		return result.Diagnostic.ResultState
	}
	if knownResultState(fallback) {
		return fallback
	}
	return resultError
}

func creationStateFor(result Result, code string) string {
	if result.Diagnostic.CreationState == creationCreated {
		return creationCreated
	}
	if knownCreationState(result.Diagnostic.CreationState) {
		return result.Diagnostic.CreationState
	}
	if result.Status == resultCreated || result.Delivered || result.ServerID > 0 || code == codeCreatedDeliveryFailed {
		return creationCreated
	}
	if result.Diagnostic.Stage == stageCreate && result.Status == resultManualReview {
		return creationUnknown
	}
	if result.Diagnostic.Stage == stageAvailability && result.Status == resultManualReview {
		return creationNotCreated
	}
	if code == codeWebHTTPError || code == codeWebAuthRejected || code == codeTransportFailure || code == codeTimeout || code == codeDNSFailure || code == codeTLSFailure || code == codeInvalidResponse || code == codeUpstreamUnknown {
		creationUncertain := result.Diagnostic.Stage == stageCreate || result.Diagnostic.Stage == stageAvailability || result.Diagnostic.Stage == stageMonitor
		if result.Status == resultManualReview && (creationUncertain || result.MonitorID != "") {
			return creationUnknown
		}
	}
	if code == codeProviderAuthRejected || code == codeProviderHTTPError {
		if result.Diagnostic.Stage == stageCreate {
			return creationUnknown
		}
		return creationNotCreated
	}
	if result.Status == resultManualReview && result.MonitorID != "" && (code == codeMonitorState || code == codeMonitorStopUnconfirmed) {
		return creationUnknown
	}
	return creationNotCreated
}

func monitoringStateFor(result Result, code string) string {
	if knownMonitoringState(result.Diagnostic.MonitoringState) {
		return result.Diagnostic.MonitoringState
	}
	switch result.Status {
	case resultMonitoring:
		return monitoringRunning
	case resultStopUnconfirmed:
		return monitoringUnknown
	case resultCreated, resultManualReview, resultStopped, resultInProgress:
		if result.MonitorID != "" {
			return monitoringUnknown
		}
	}
	if code == codeMonitorStopUnconfirmed {
		return monitoringUnknown
	}
	return monitoringNotStarted
}

func safeStage(stage string) string {
	if knownStage(stage) {
		return stage
	}
	return stageUnknown
}

func knownStage(stage string) bool {
	switch stage {
	case stageInput, stageConfiguration, stageStorage, stageRecipient, stageMonitor, stageAvailability, stageCreate, stageHandoff, stageProvider, stageStatus, stageStop, stageTool, stageUnknown:
		return true
	default:
		return false
	}
}

func knownDiagnosticCode(code string) bool {
	switch code {
	case "provider_timeout", "provider_dns_failure", "provider_tls_failure", "provider_transport_failure", "invalid_request", "operation_conflict", "creation_uncertain", "storage_unavailable", "rate_limited", "unauthorized", "unavailable", codeInvalidArguments, codeCapabilityUnavailable, codeMissingToken, codeCredentialUnreadable, codeInvalidToken, codeProtectedStorage, codeRecipientUnavailable, codeRecipientChanged, codeHandoffFailure, codeCancelled, codeTimeout, codeDNSFailure, codeTLSFailure, codeTransportFailure, codeInvalidResponse, codeWebAuthRejected, codeWebHTTPError, codeProviderAuthRejected, codeProviderHTTPError, codeUpstreamUnknown, codeMonitorState, codeMonitorNotFound, codeMonitorInProgress, codeMonitorStopUnconfirmed, codePersistedStateInvalid, codeMonitorStarted, codeMonitorStopped, codeRetryConfirmation, codeCreated, codeCreatedDeliveryFailed, codeAvailabilityRetryExhausted, codeManualReview, codeUnknownFailure:
		return true
	default:
		return false
	}
}

func knownResultState(state string) bool {
	switch state {
	case resultMonitoring, resultCreated, resultManualReview, resultStopped, resultInProgress, resultStopUnconfirmed, resultRejected, resultUnavailable, resultError:
		return true
	default:
		return false
	}
}

func knownCreationState(state string) bool {
	switch state {
	case creationCreated, creationNotCreated, creationUnknown:
		return true
	default:
		return false
	}
}

func knownMonitoringState(state string) bool {
	switch state {
	case monitoringRunning, monitoringStopped, monitoringUnknown, monitoringNotStarted:
		return true
	default:
		return false
	}
}

func resultStateForCode(code string) string {
	switch code {
	case codeMonitorStarted:
		return resultMonitoring
	case codeMonitorStopped:
		return resultStopped
	case codeRetryConfirmation:
		return resultManualReview
	case codeMonitorInProgress:
		return resultInProgress
	case codeCreated, codeCreatedDeliveryFailed:
		return resultCreated
	case codeAvailabilityRetryExhausted:
		return resultManualReview
	case codeInvalidArguments:
		return resultRejected
	case codeCapabilityUnavailable:
		return resultUnavailable
	default:
		return resultManualReview
	}
}

func creationStateForCode(code string) string {
	switch code {
	case codeCreated, codeCreatedDeliveryFailed:
		return creationCreated
	case codeMissingToken, codeCredentialUnreadable, codeInvalidToken, codeRecipientUnavailable, codeRecipientChanged, codeHandoffFailure, codeProtectedStorage, codeInvalidArguments, codeCapabilityUnavailable, codeProviderAuthRejected, codeProviderHTTPError, codeAvailabilityRetryExhausted:
		return creationNotCreated
	default:
		return creationUnknown
	}
}

func monitoringStateForCode(code string) string {
	switch code {
	case codeMonitorStarted:
		return monitoringRunning
	case codeMonitorStopped:
		return monitoringStopped
	case codeMonitorInProgress:
		return monitoringRunning
	case codeMonitorStopUnconfirmed:
		return monitoringUnknown
	case codeRetryConfirmation:
		return monitoringStopped
	default:
		return monitoringUnknown
	}
}

func causeForCode(code string) string {
	switch code {
	case "provider_timeout":
		return "the provider request timed out"
	case "provider_dns_failure":
		return "the provider name could not be resolved"
	case "provider_tls_failure":
		return "the provider TLS connection failed"
	case "provider_transport_failure":
		return "the provider transport failed"
	case "invalid_request":
		return "the service rejected the request"
	case "operation_conflict":
		return "the service reported an operation conflict"
	case "creation_uncertain":
		return "the service could not confirm creation"
	case "storage_unavailable":
		return "the service storage is unavailable"
	case "rate_limited":
		return "the service reported a rate limit"
	case "unauthorized":
		return "the service rejected authorization"
	case "unavailable":
		return "the service is unavailable"
	case codeInvalidArguments:
		return "the migration arguments were rejected"
	case codeCapabilityUnavailable:
		return "the migration capability is unavailable"
	case codeMissingToken:
		return "the provider credentials are not configured"
	case codeCredentialUnreadable:
		return "the protected provider settings could not be read"
	case codeInvalidToken:
		return "the configured provider credential is invalid"
	case codeProtectedStorage:
		return "protected local migration storage failed"
	case codeRecipientUnavailable:
		return "the paired Telegram recipient is unavailable"
	case codeRecipientChanged:
		return "the paired Telegram recipient changed"
	case codeHandoffFailure:
		return "the provider result exists but protected handoff delivery failed"
	case codeCancelled:
		return "the migration request was cancelled"
	case codeTimeout:
		return "the migration request timed out"
	case codeDNSFailure:
		return "the migration service name could not be resolved"
	case codeTLSFailure:
		return "the migration service TLS connection failed"
	case codeTransportFailure:
		return "the migration service transport failed"
	case codeInvalidResponse:
		return "the migration service response was invalid"
	case codeWebAuthRejected:
		return "the migration service rejected the request"
	case codeWebHTTPError:
		return "the migration service returned an HTTP error"
	case codeProviderAuthRejected:
		return "the provider rejected the configured credential"
	case codeProviderHTTPError:
		return "the provider returned an HTTP error"
	case codeUpstreamUnknown:
		return "the upstream migration service returned an unclassified failure"
	case codeMonitorState:
		return "the migration monitor state requires manual review"
	case codeMonitorNotFound:
		return "the migration monitor was not found"
	case codeMonitorInProgress:
		return "the migration monitor is already running"
	case codeMonitorStopUnconfirmed:
		return "the migration monitor could not be confirmed stopped"
	case codePersistedStateInvalid:
		return "the persisted migration diagnostic state is invalid"
	case codeMonitorStarted:
		return "the migration monitor is active"
	case codeMonitorStopped:
		return "the migration monitor is stopped"
	case codeRetryConfirmation:
		return "the previous migration attempt may have created a server"
	case codeCreated:
		return "the migration was created and handoff completed"
	case codeCreatedDeliveryFailed:
		return "the migration was created but handoff delivery failed"
	case codeAvailabilityRetryExhausted:
		return "the bounded pre-create availability retry budget was exhausted"
	case codeManualReview:
		return "the migration requires manual review"
	default:
		return "the migration failed for an unclassified reason"
	}
}

func nextActionForCode(code string) string {
	switch code {
	case codeInvalidArguments:
		return "correct the migration action arguments and retry the read-only control call"
	case codeCapabilityUnavailable:
		return "inspect the local migration configuration and retry status later"
	case codeMissingToken:
		return "configure HCLOUD_TOKEN with /env, then inspect status; do not retry a create"
	case codeCredentialUnreadable:
		return "repair protected provider settings, then inspect status; do not retry a create"
	case codeInvalidToken:
		return "review the configured provider credential with /env, then inspect status; do not retry a create"
	case codeProtectedStorage:
		return "repair protected local migration storage and inspect status before any new attempt"
	case codeRecipientUnavailable:
		return "restore the paired private recipient and inspect status; do not resend automatically"
	case codeRecipientChanged:
		return "reconcile the paired recipient and inspect status; do not resend automatically"
	case codeHandoffFailure:
		return handoffFailureNextAction
	case codeCancelled:
		return "inspect status before deciding whether a new owner-approved attempt is safe"
	case codeTimeout, codeDNSFailure, codeTLSFailure, codeTransportFailure, codeWebHTTPError, codeWebAuthRejected, codeProviderHTTPError, codeProviderAuthRejected, codeUpstreamUnknown:
		return uncertainNextAction
	case codeInvalidResponse:
		return uncertainNextAction
	case codeAvailabilityRetryExhausted:
		return "resolve the availability service failure, then obtain fresh owner approval before a new attempt"
	case codeMonitorState, codeMonitorNotFound, codePersistedStateInvalid:
		return "reconcile the retained monitor record manually before any new attempt"
	case codeMonitorInProgress:
		return "wait for the running monitor execution, then inspect status"
	case codeMonitorStopUnconfirmed:
		return "manually verify monitor state before any new attempt; do not assume it is stopped"
	case codeMonitorStarted:
		return "wait for the next 30-minute availability check"
	case codeMonitorStopped:
		return "no automatic action will run; obtain fresh owner approval before a new attempt"
	case codeRetryConfirmation:
		return retryConfirmationNextAction
	case codeCreated:
		return "use the protected paired handoff and continue setup manually"
	case codeCreatedDeliveryFailed:
		return createdDeliveryFailedNextAction
	case codeManualReview:
		return uncertainNextAction
	default:
		return uncertainNextAction
	}
}

func safeProviderCode(code string) string {
	switch code {
	case "unauthorized", "forbidden", "rate_limit_exceeded", "resource_unavailable", "invalid_input":
		return code
	default:
		return ""
	}
}

type monitorDiagnosticRecord struct {
	Version    string     `json:"version"`
	MonitorID  string     `json:"monitor_id"`
	Sequence   int64      `json:"sequence"`
	Diagnostic Diagnostic `json:"diagnostic"`
}

type monitorDiagnosticEvidence struct {
	Diagnostic Diagnostic
	Found      bool
	HasCreated bool
	HasUnknown bool
}

const monitorDiagnosticVersion = "v1"

const maxDiagnosticSequence = int64(1 << 62)

func storeMonitorDiagnostic(options Options, diagnostic Diagnostic) error {
	diagnostic = NormalizeResult(Result{MonitorID: diagnostic.MonitorID, Status: diagnostic.ResultState, Diagnostic: diagnostic}).Diagnostic
	if !safeJobID(diagnostic.MonitorID) || !knownDiagnosticCode(diagnostic.Code) {
		return errors.Join(ErrProtectedStorage, errors.New("migration diagnostic state is invalid"))
	}
	if diagnostic.UpdatedAt == 0 {
		diagnostic.UpdatedAt = currentTime(options.Now).Unix()
	}
	if err := validatePersistedDiagnostic(diagnostic, diagnostic.MonitorID); err != nil {
		return errors.Join(ErrProtectedStorage, err)
	}
	directory, err := operationDirectory(options)
	if err != nil {
		return errors.Join(ErrProtectedStorage, err)
	}
	sequence, err := nextDiagnosticSequence(directory, diagnostic.MonitorID)
	if err != nil {
		return errors.Join(ErrProtectedStorage, err)
	}
	record := monitorDiagnosticRecord{Version: monitorDiagnosticVersion, MonitorID: diagnostic.MonitorID, Sequence: sequence, Diagnostic: diagnostic}
	data, err := json.Marshal(record)
	if err != nil {
		return errors.Join(ErrProtectedStorage, err)
	}
	if len(data) > maxDiagnosticBytes {
		return errors.Join(ErrProtectedStorage, errors.New("migration diagnostic state exceeds its bound"))
	}
	path := filepath.Join(directory, diagnostic.MonitorID+diagnosticFileSeparator+diagnostic.Code+fmt.Sprintf(".%020d", sequence)+diagnosticFileSuffix)
	if _, err := protectedfile.WriteExclusive(path, data); err == nil {
		return nil
	}
	// Diagnostic records are append-only. A repeated state is safe only when
	// the already-published record is exactly the same validated value.
	content, readErr := protectedfile.Read(path, maxDiagnosticBytes)
	if readErr != nil {
		return errors.Join(ErrProtectedStorage, readErr)
	}
	defer clear(content.Data)
	var existing monitorDiagnosticRecord
	if readErr = decodeExactJSON(content.Data, &existing); readErr != nil || existing.Version != monitorDiagnosticVersion || existing.MonitorID != diagnostic.MonitorID || existing.Sequence != sequence || existing.Diagnostic != diagnostic {
		return errors.Join(ErrProtectedStorage, errors.New("migration diagnostic record changed"), readErr)
	}
	if err := validatePersistedDiagnostic(existing.Diagnostic, existing.MonitorID); err != nil {
		return errors.Join(ErrProtectedStorage, err)
	}
	return nil
}

func loadMonitorDiagnostic(options Options, monitorID string) (Diagnostic, bool, error) {
	evidence, err := loadMonitorDiagnosticEvidence(options, monitorID)
	return evidence.Diagnostic, evidence.Found, err
}

func loadMonitorDiagnosticEvidence(options Options, monitorID string) (monitorDiagnosticEvidence, error) {
	if !safeJobID(monitorID) {
		return monitorDiagnosticEvidence{}, errors.Join(ErrProtectedStorage, errors.New("migration monitor ID is invalid"))
	}
	directory, err := operationDirectory(options)
	if err != nil {
		return monitorDiagnosticEvidence{}, errors.Join(ErrProtectedStorage, err)
	}
	entries, err := os.ReadDir(directory)
	if err != nil {
		return monitorDiagnosticEvidence{}, errors.Join(ErrProtectedStorage, err)
	}
	prefix := monitorID + diagnosticFileSeparator
	evidence := monitorDiagnosticEvidence{}
	latestName := ""
	for _, entry := range entries {
		name := entry.Name()
		if !strings.HasPrefix(name, prefix) {
			continue
		}
		if entry.IsDir() || !strings.HasSuffix(name, diagnosticFileSuffix) {
			return monitorDiagnosticEvidence{}, errors.Join(ErrPersistedStateInvalid, errors.New("migration diagnostic record path is invalid"))
		}
		stem := strings.TrimSuffix(strings.TrimPrefix(name, prefix), diagnosticFileSuffix)
		parts := strings.Split(stem, ".")
		if len(parts) != 2 {
			return monitorDiagnosticEvidence{}, errors.Join(ErrPersistedStateInvalid, errors.New("migration diagnostic record code is invalid"))
		}
		code := parts[0]
		sequence, parseErr := parseDiagnosticSequence(parts[1])
		if !knownDiagnosticCode(code) || code == "" || parseErr != nil {
			return monitorDiagnosticEvidence{}, errors.Join(ErrPersistedStateInvalid, errors.New("migration diagnostic record code is invalid"), parseErr)
		}
		content, readErr := protectedfile.Read(filepath.Join(directory, name), maxDiagnosticBytes)
		if readErr != nil {
			return monitorDiagnosticEvidence{}, errors.Join(ErrProtectedStorage, readErr)
		}
		var record monitorDiagnosticRecord
		decodeErr := decodeExactJSON(content.Data, &record)
		clear(content.Data)
		if decodeErr != nil || record.Version != monitorDiagnosticVersion || record.MonitorID != monitorID || record.Sequence != sequence || record.Diagnostic.Code != code {
			return monitorDiagnosticEvidence{}, errors.Join(ErrPersistedStateInvalid, errors.New("migration diagnostic record is invalid"), decodeErr)
		}
		if validateErr := validatePersistedDiagnostic(record.Diagnostic, monitorID); validateErr != nil {
			return monitorDiagnosticEvidence{}, errors.Join(ErrPersistedStateInvalid, validateErr)
		}
		record.Diagnostic.NextAction = nextActionForCode(record.Diagnostic.Code)
		if record.Diagnostic.RetryState == retryStatePending {
			record.Diagnostic.NextAction = retryPendingNextAction
		}
		if record.Diagnostic.CreationState == creationCreated {
			evidence.HasCreated = true
		}
		if record.Diagnostic.CreationState == creationUnknown && record.Diagnostic.Code != codeMonitorStarted {
			evidence.HasUnknown = true
		}
		if !evidence.Found || record.Sequence > sequenceForDiagnostic(latestName) {
			evidence.Diagnostic = record.Diagnostic
			latestName = name
			evidence.Found = true
		}
	}
	if evidence.HasCreated {
		evidence.Diagnostic.CreationState = creationCreated
	} else if evidence.HasUnknown {
		evidence.Diagnostic.CreationState = creationUnknown
	}
	return evidence, nil
}

func nextDiagnosticSequence(directory, monitorID string) (int64, error) {
	entries, err := os.ReadDir(directory)
	if err != nil {
		return 0, err
	}
	prefix := monitorID + diagnosticFileSeparator
	var maximum int64
	for _, entry := range entries {
		name := entry.Name()
		if !strings.HasPrefix(name, prefix) || !strings.HasSuffix(name, diagnosticFileSuffix) {
			continue
		}
		stem := strings.TrimSuffix(strings.TrimPrefix(name, prefix), diagnosticFileSuffix)
		parts := strings.Split(stem, ".")
		if len(parts) != 2 || !knownDiagnosticCode(parts[0]) {
			return 0, errors.New("migration diagnostic record path is invalid")
		}
		sequence, err := parseDiagnosticSequence(parts[1])
		if err != nil {
			return 0, err
		}
		if sequence > maximum {
			maximum = sequence
		}
	}
	if maximum >= maxDiagnosticSequence {
		return 0, errors.New("migration diagnostic record sequence exceeds its bound")
	}
	return maximum + 1, nil
}

func parseDiagnosticSequence(value string) (int64, error) {
	if len(value) != 20 {
		return 0, errors.New("migration diagnostic record sequence is invalid")
	}
	for _, character := range value {
		if character < '0' || character > '9' {
			return 0, errors.New("migration diagnostic record sequence is invalid")
		}
	}
	var sequence int64
	for _, character := range value {
		digit := int64(character - '0')
		if sequence > (maxDiagnosticSequence-digit)/10 {
			return 0, errors.New("migration diagnostic record sequence exceeds its bound")
		}
		sequence = sequence*10 + digit
	}
	if sequence <= 0 {
		return 0, errors.New("migration diagnostic record sequence is invalid")
	}
	return sequence, nil
}

func sequenceForDiagnostic(name string) int64 {
	if name == "" {
		return 0
	}
	stem := strings.TrimSuffix(name, diagnosticFileSuffix)
	parts := strings.Split(stem, ".")
	if len(parts) == 0 {
		return 0
	}
	sequence, err := parseDiagnosticSequence(parts[len(parts)-1])
	if err != nil {
		return 0
	}
	return sequence
}

func validatePersistedDiagnostic(diagnostic Diagnostic, monitorID string) error {
	retryStateValid := validRetryState(diagnostic.RetryState, diagnostic.RetryCount) || diagnostic.RetryState == "" && diagnostic.RetryCount == 0
	expectedNextAction := nextActionForCode(diagnostic.Code)
	if diagnostic.RetryState == retryStatePending {
		expectedNextAction = retryPendingNextAction
	}
	if diagnostic.MonitorID != monitorID || !safeJobID(monitorID) || !knownStage(diagnostic.Stage) || !knownDiagnosticCode(diagnostic.Code) || !knownResultState(diagnostic.ResultState) || !knownCreationState(diagnostic.CreationState) || !knownMonitoringState(diagnostic.MonitoringState) || !retryStateValid || diagnostic.Cause != causeForCode(diagnostic.Code) || !persistedNextActionMatches(diagnostic, expectedNextAction) || diagnostic.MonitoringStopped != (diagnostic.MonitoringState == monitoringStopped) || diagnostic.HTTPStatus < 0 || diagnostic.HTTPStatus > 599 || diagnostic.HTTPStatus > 0 && diagnostic.HTTPStatus < 100 || safeProviderCode(diagnostic.ProviderCode) != diagnostic.ProviderCode || diagnostic.UpdatedAt < 0 || diagnostic.UpdatedAt > maxDiagnosticTimestamp {
		return errors.New("migration diagnostic state failed validation")
	}
	return nil
}

func persistedNextActionMatches(diagnostic Diagnostic, expected string) bool {
	if diagnostic.NextAction == expected {
		return true
	}
	if diagnostic.RetryState == retryStatePending {
		return false
	}
	switch diagnostic.Code {
	case codeTimeout, codeDNSFailure, codeTLSFailure, codeTransportFailure, codeWebHTTPError, codeWebAuthRejected, codeProviderHTTPError, codeProviderAuthRejected, codeUpstreamUnknown:
		return diagnostic.NextAction == legacyServiceNextAction
	case codeInvalidResponse:
		return diagnostic.NextAction == legacyInvalidResponseNextAction
	case codeManualReview, codeUnknownFailure, "provider_timeout", "provider_dns_failure", "provider_tls_failure", "provider_transport_failure", "invalid_request", "operation_conflict", "creation_uncertain", "storage_unavailable", "rate_limited", "unauthorized", "unavailable":
		return diagnostic.NextAction == legacyManualReviewNextAction
	case codeRetryConfirmation:
		return diagnostic.NextAction == legacyRetryConfirmationNextAction
	case codeHandoffFailure:
		return diagnostic.NextAction == legacyHandoffFailureNextAction
	case codeCreatedDeliveryFailed:
		return diagnostic.NextAction == legacyCreatedDeliveryFailedAction
	default:
		return false
	}
}

func initialMonitorDiagnostic(monitorID string) Diagnostic {
	diagnostic := diagnosticTemplate(stageMonitor, codeMonitorStarted)
	diagnostic.MonitorID = monitorID
	diagnostic.CreationState = creationNotCreated
	return diagnostic
}

func stoppedMonitorDiagnostic(previous Diagnostic, monitorID string) Diagnostic {
	creation := previous.CreationState
	if !knownCreationState(creation) {
		creation = creationNotCreated
	}
	diagnostic := diagnosticTemplate(stageStop, codeMonitorStopped)
	diagnostic.CreationState = creation
	diagnostic.MonitorID = monitorID
	diagnostic.UpdatedAt = 0
	return normalizeDiagnostic(diagnostic, stageStop, Result{MonitorID: monitorID, Status: resultStopped, Diagnostic: diagnostic})
}

func unconfirmedStopDiagnostic(previous Diagnostic, monitorID string) Diagnostic {
	diagnostic := diagnosticTemplate(stageStop, codeMonitorStopUnconfirmed)
	diagnostic.MonitorID = monitorID
	diagnostic.MonitoringState = monitoringUnknown
	diagnostic.MonitoringStopped = false
	if knownCreationState(previous.CreationState) {
		diagnostic.CreationState = previous.CreationState
	}
	if knownResultState(previous.ResultState) {
		diagnostic.ResultState = previous.ResultState
	}
	return diagnostic
}

func statusDiagnosticForJob(jobID string, jobState string, enabled bool, review bool, stored Diagnostic, found bool) Diagnostic {
	if !found {
		stored = initialMonitorDiagnostic(jobID)
	}
	if !safeJobID(stored.MonitorID) {
		stored.MonitorID = jobID
	}
	if (enabled && !review) || jobState == "running" && !review {
		stored.MonitorID = jobID
		stored.MonitoringState = monitoringRunning
		stored.MonitoringStopped = false
		if stored.ResultState == "" || stored.ResultState == resultStopped {
			stored.ResultState = resultMonitoring
		}
		return normalizeDiagnostic(stored, stageMonitor, Result{MonitorID: jobID, Status: resultMonitoring, Diagnostic: stored})
	}
	if jobState != "running" && !stored.MonitoringStopped {
		stored.MonitorID = jobID
		stored.MonitoringState = monitoringStopped
		stored.MonitoringStopped = true
		if stored.ResultState == resultMonitoring {
			stored = stoppedMonitorDiagnostic(stored, jobID)
		}
	}
	if review && stored.ResultState == resultMonitoring {
		stored = diagnosticTemplate(stageMonitor, codeManualReview)
		stored.MonitorID = jobID
		stored.CreationState = creationNotCreated
		stored.MonitoringState = monitoringStopped
		stored.MonitoringStopped = true
	}
	return normalizeDiagnostic(stored, stageMonitor, Result{MonitorID: jobID, Status: stored.ResultState, Diagnostic: stored})
}

func safeDiagnosticError(stage, code string, result Result) error {
	diagnostic := diagnosticTemplate(stage, code)
	diagnostic.MonitorID = result.MonitorID
	diagnostic.CreationState = creationStateFor(result, code)
	diagnostic.MonitoringState = monitoringStateFor(result, code)
	diagnostic.MonitoringStopped = diagnostic.MonitoringState == monitoringStopped
	return migrationFailure{diagnostic: normalizeDiagnostic(diagnostic, stage, result), cause: errors.New(diagnostic.Cause)}
}

func formatDiagnosticState(diagnostic Diagnostic) string {
	data, err := json.Marshal(diagnostic)
	if err != nil {
		return "migration diagnostic state unavailable"
	}
	return string(data)
}

func safeDiagnosticTimestamp(now func() time.Time) int64 {
	value := currentTime(now).Unix()
	if value < 0 || value > maxDiagnosticTimestamp {
		return 0
	}
	return value
}

func invalidDiagnosticRecordError() error {
	return fmt.Errorf("%s", causeForCode(codePersistedStateInvalid))
}
