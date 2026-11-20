package chat

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"unicode/utf8"

	"goaster/internal/attachment"
	"goaster/internal/diagnostics"
)

// Providers wrap ErrModelUnavailable when the selected model is unavailable.
var ErrModelUnavailable = errors.New("model unavailable")

const (
	requestMigrationRetryException = "For migration, one approval covers the immediate first create and 30-minute capacity-only retries; retry only the exact top-level not_created/no_capacity result and stop for every other outcome."
	requestUncertaintyWarning      = "If an earlier attempt may have failed or been interrupted, it may have performed actions and its tool results may be unavailable. Before continuing or retrying, check whether any actions completed and verify external state. For migration, do not replay an uncertain create or handoff. " + requestMigrationRetryException
	requestHistoryWarning          = "Conversation history may omit failed or interrupted attempts, even after a restart. Do not assume that an omitted attempt performed no actions."
	requestScheduledRepeatWarning  = "Carry out explicitly scheduled normal repeats as requested; do not suppress them because previous scheduled runs occurred. A scheduled migration uses one create call per claimed turn and cannot add another job; only the exact no_capacity result quietly keeps the same job waiting."
	postToolAuthenticationWarning  = "OpenAI authentication failed after a tool started. Actions may already have completed and tool results may be unavailable. Check and reconcile external state, then resolve OpenAI authentication before retrying. For migration, stop and report rather than replaying an uncertain create or handoff."
	postToolAuthenticationLegacy   = "OpenAI authentication failed after a tool started. Actions may already have completed and tool results may be unavailable. Check and reconcile external state, then resolve OpenAI authentication before retrying."
	interruptedActionWarning       = "Scheduled run was interrupted after a tool started. Actions may already have completed and tool results may be unavailable. Check and reconcile external state before retrying."
	unknownActionWarning           = "Scheduled run was interrupted and its action status is unknown. Actions may already have completed and tool results may be unavailable. Check and reconcile external state before retrying."

	requestFailureAuthentication     = "Authentication is required to continue. Check your OpenAI sign-in and account status."
	requestFailureCredentialsMissing = "OpenAI credentials were not found. Restart GoAster to initiate sign-in."
	requestFailureTimeout            = "The request timed out."
	requestFailureModelUnavailable   = "The selected model is unavailable."
	requestFailureInterrupted        = "The request was interrupted."
	requestFailureStateSave          = "Conversation state could not be saved or confirmed. Recent changes may not be recorded."
	requestFailureLockBusy           = "Another instance may be using the conversation. Wait for it to finish, then restart GoAster to reload the conversation."
	requestFailureCredentialMode     = "The credential file permissions are too broad. Restrict access to the file (mode 0600) before retrying."
	requestFailureConversationMode   = "The conversation file permissions are too broad. Restrict access to the file (mode 0600) before retrying."
	requestFailureDirectoryMode      = "The conversation directory permissions are too broad. Restrict access to the directory (mode 0700) before retrying."
	requestFailureModelStream        = "The model stream failed."
	requestFailureUnknown            = "The request failed for an unknown reason."
)

const (
	modelStreamClassification         = "model_stream"
	modelRequestFailureClassification = "model_request_failed"
	modelStreamInterruptedRead        = "interrupted_read"
	modelStreamPrematureEOF           = "premature_eof"
	modelStreamMalformedEvent         = "malformed_event"
	modelStreamUnrecognizedEvent      = "unrecognized_event"
	modelStreamResponseIncomplete     = "response_incomplete"
	modelStreamProviderError          = "provider_error"
	modelStreamMissingFinal           = "missing_final"
)

var errModelStream = errors.New("model stream failure")

// ModelStreamError is the safe boundary for a model response that did not
// produce a completed usable result. Cause remains available to the owner for
// typed diagnostics, while Error never copies provider text, bodies, or URLs.
type ModelStreamError struct {
	Classification string
	Reference      string
	Retryable      bool
	Cause          error
}

func (err ModelStreamError) Error() string {
	message := SafeModelStreamMessage(err.Classification, err.Reference)
	if detail := modelStreamCauseDetail(err.Cause); detail != "" {
		message += ": " + detail
	}
	return message
}

func (err ModelStreamError) Unwrap() error { return err.Cause }

func (err ModelStreamError) Is(target error) bool { return target == errModelStream }

func (err ModelStreamError) DiagnosticFields() []diagnostics.Field {
	fields := []diagnostics.Field{{Key: "stream_classification", Value: err.Classification}}
	if err.Reference != "" {
		fields = append(fields, diagnostics.Field{Key: "request_reference", Value: err.Reference})
	}
	return fields
}

// IsModelStreamError reports whether err contains the safe model-stream
// classification marker.
func IsModelStreamError(err error) bool {
	for _, current := range requestFailureErrors(err) {
		if _, ok := current.(ModelStreamError); ok {
			return true
		}
	}
	return false
}

func modelStreamCauseDetail(err error) string {
	for _, current := range requestFailureErrors(err) {
		if backend, ok := current.(backendError); ok {
			return backend.Error()
		}
	}
	return ""
}

// ModelStreamFailureInfo returns only the allowlisted classification and local
// request reference carried by a model-stream failure.
func ModelStreamFailureInfo(err error) (classification, reference string, ok bool) {
	if err == nil {
		return "", "", false
	}
	for _, current := range requestFailureErrors(err) {
		streamErr, matches := current.(ModelStreamError)
		if !matches {
			continue
		}
		classification = diagnostics.StreamClassification(streamErr.Classification)
		if classification == "" {
			classification = modelStreamClassification
		}
		return classification, diagnostics.RequestReference(streamErr.Reference), true
	}
	return "", "", false
}

// SafeModelStreamMessage creates the plain-text message shared by the
// terminal, local agent and Telegram adapter. Both inputs are sanitized before
// they can reach a user or a response body.
func SafeModelStreamMessage(classification, reference string) string {
	safeClassification := diagnostics.StreamClassification(classification)
	message := requestFailureModelStream
	switch safeClassification {
	case modelStreamInterruptedRead:
		message = "The model stream was interrupted before completion."
	case modelStreamPrematureEOF:
		message = "The model stream ended before completion."
	case modelStreamMalformedEvent:
		message = "The model returned a malformed stream response."
	case modelStreamUnrecognizedEvent:
		message = "The model returned an unrecognized stream event."
	case modelStreamResponseIncomplete:
		message = "The model response was incomplete."
	case modelStreamProviderError:
		message = "The model provider reported a stream failure."
	case modelRequestFailureClassification:
		message = "The model request failed."
	case modelStreamMissingFinal:
		message = "The model stream ended without a final response."
	}
	if safeReference := diagnostics.RequestReference(reference); safeReference != "" {
		message += " Reference: " + safeReference + "."
	}
	return message
}

// ModelStreamMessageForError returns a safe terminal message for err. A
// caller-supplied reference is used only when the error did not already carry
// one, which lets /chat correlate failures across the local boundary.
func ModelStreamMessageForError(err error, reference string) string {
	classification, errorReference, ok := ModelStreamFailureInfo(err)
	if !ok {
		classification = modelRequestFailureClassification
	}
	if errorReference == "" {
		errorReference = diagnostics.RequestReference(reference)
	}
	return SafeModelStreamMessage(classification, errorReference)
}

type requestReferenceKey struct{}

// WithRequestReference carries only the sanitized local /chat reference into
// the provider request. It never carries a prompt, model output, or secret.
func WithRequestReference(ctx context.Context, reference string) context.Context {
	return context.WithValue(ctx, requestReferenceKey{}, diagnostics.RequestReference(reference))
}

func requestReference(ctx context.Context) string {
	reference, _ := ctx.Value(requestReferenceKey{}).(string)
	return diagnostics.RequestReference(reference)
}

type ToolProgress struct {
	Started   int
	Completed int
}

type toolProgressError struct {
	cause    error
	progress ToolProgress
}

func (err toolProgressError) Error() string {
	if err.cause == nil {
		return "model request failed"
	}
	return err.cause.Error()
}

func (err toolProgressError) Unwrap() error { return err.cause }

func (err toolProgressError) DiagnosticFields() []diagnostics.Field {
	return []diagnostics.Field{
		{Key: "tool_started_count", Value: fmt.Sprintf("%d", err.progress.Started)},
		{Key: "tool_completed_count", Value: fmt.Sprintf("%d", err.progress.Completed)},
		{Key: "tool_started", Value: fmt.Sprintf("%t", err.progress.Started > 0)},
		{Key: "tool_completed", Value: fmt.Sprintf("%t", err.progress.Completed > 0)},
	}
}

// ToolProgressDetails returns bounded local progress metadata without exposing
// tool arguments or results.
func ToolProgressDetails(err error) (ToolProgress, bool) {
	if err == nil {
		return ToolProgress{}, false
	}
	for _, current := range requestFailureErrors(err) {
		progressErr, matches := current.(toolProgressError)
		if !matches {
			continue
		}
		if progressErr.progress.Started < 0 || progressErr.progress.Completed < 0 {
			return ToolProgress{}, false
		}
		return progressErr.progress, true
	}
	return ToolProgress{}, false
}

type postToolAuthenticationMarker struct{}

func (postToolAuthenticationMarker) Error() string { return postToolAuthenticationLegacy }

type postToolParentCanceledMarker struct{}

func (postToolParentCanceledMarker) Error() string { return "parent request canceled" }

type postToolAuthenticationError struct {
	cause     error
	parentErr error
}

func (err postToolAuthenticationError) Error() string {
	if err.parentErr != nil {
		return err.parentErr.Error()
	}
	return postToolAuthenticationWarning
}
func (err postToolAuthenticationError) Unwrap() error { return err.cause }
func (err postToolAuthenticationError) Is(target error) bool {
	return target == (postToolAuthenticationMarker{}) || (target == (postToolParentCanceledMarker{}) && err.parentErr != nil)
}

// PostToolAuthenticationReviewMessage also identifies canceled requests so a
// scheduled run can retain the uncertainty for manual review.
func PostToolAuthenticationReviewMessage(err error) string {
	if requestFailureIs(err, postToolAuthenticationMarker{}) {
		return postToolAuthenticationWarning
	}
	return ""
}

// PostToolAuthenticationMessage returns fixed guidance for a live caller,
// including when only the client's response body was canceled or timed out.
func PostToolAuthenticationMessage(err error) string {
	if requestFailureIs(err, postToolParentCanceledMarker{}) {
		return ""
	}
	return PostToolAuthenticationReviewMessage(err)
}

type interruptedActionMarker struct{}

func (interruptedActionMarker) Error() string { return interruptedActionWarning }

type interruptedActionError struct{ cause error }

func (interruptedActionError) Error() string        { return interruptedActionWarning }
func (err interruptedActionError) Unwrap() error    { return err.cause }
func (interruptedActionError) Is(target error) bool { return target == (interruptedActionMarker{}) }

type unknownActionMarker struct{}

func (unknownActionMarker) Error() string { return unknownActionWarning }

type unknownActionError struct{ cause error }

func (unknownActionError) Error() string        { return unknownActionWarning }
func (err unknownActionError) Unwrap() error    { return err.cause }
func (unknownActionError) Is(target error) bool { return target == (unknownActionMarker{}) }

// ScheduledActionReviewMessage identifies interrupted scheduled runs and
// post-tool authentication failures that must not be replayed automatically.
func ScheduledActionReviewMessage(err error) string {
	if message := PostToolAuthenticationReviewMessage(err); message != "" {
		return message
	}
	if errors.Is(err, interruptedActionMarker{}) {
		return interruptedActionWarning
	}
	if errors.Is(err, unknownActionMarker{}) {
		return unknownActionWarning
	}
	return ""
}

// IsPostToolAuthenticationMessage validates guidance from the local agent.
func IsPostToolAuthenticationMessage(message string) bool {
	return message == postToolAuthenticationWarning || message == postToolAuthenticationLegacy
}

// IsSafeModelErrorMessage accepts only the local agent's bounded selected
// model marker. It prevents an arbitrary streamed "model ..." error from
// becoming Telegram user-visible text.
func IsSafeModelErrorMessage(message string) bool {
	message = strings.TrimSpace(message)
	if len(message) > 512 || !strings.HasPrefix(message, "model ") || !strings.HasSuffix(message, ": model unavailable") {
		return false
	}
	model := strings.TrimSuffix(strings.TrimPrefix(message, "model "), ": model unavailable")
	if len(model) == 0 || len(model) > 96 {
		return false
	}
	for _, character := range model {
		if character >= 'a' && character <= 'z' || character >= 'A' && character <= 'Z' || character >= '0' && character <= '9' || strings.ContainsRune("._-", character) {
			continue
		}
		return false
	}
	return true
}

type insecurePermissionsError uint8

const (
	errCredentialPermissions insecurePermissionsError = iota + 1
	errConversationFilePermissions
	errConversationDirectoryPermissions
)

func (err insecurePermissionsError) Error() string {
	switch err {
	case errCredentialPermissions:
		return "credential file permissions are too broad"
	case errConversationFilePermissions:
		return "conversation file permissions are too broad"
	case errConversationDirectoryPermissions:
		return "conversation directory permissions are too broad"
	default:
		return "permissions are too broad"
	}
}

// ErrSessionBusy means a scheduled turn must be deferred before any action
// starts, such as during lock contention, recovery, or cancellation. Retrying
// cannot repeat a tool action.
var ErrSessionBusy = errors.New("conversation is busy")

// ErrConversationRecovery requires an explicit acknowledgement before a
// missing or quarantined conversation can be replaced by an interactive turn.
var ErrConversationRecovery = errors.New("The saved conversation is missing or invalid and may have been moved aside. Check the saved or quarantined file and whether any actions already completed. Use /clear to start a new conversation.")

var errMigrationDiagnosticsPending = errors.New("pending migration diagnostic evidence requires manual reconciliation before continuing")

// ModelRequestError reports the exact model that returned a provider error.
// The caller must not retry the request with another model.
type ModelRequestError struct {
	Model string
	Err   error
}

func (err ModelRequestError) Error() string {
	if message := PostToolAuthenticationMessage(err.Err); message != "" {
		return message
	}
	return fmt.Sprintf("model %s: %v", err.Model, err.Err)
}

func (err ModelRequestError) Unwrap() error { return err.Err }

type safeRequestError struct {
	message string
	cause   error
}

func (err safeRequestError) Error() string {
	return err.message
}

func (err safeRequestError) Unwrap() error {
	return err.cause
}

const maxRequestErrorDepth = 1024

func requestErrorComparable(err error) bool {
	return err != nil && reflect.ValueOf(err).Comparable()
}

func requestFailureErrors(err error) []error {
	if err == nil {
		return nil
	}
	nodes := make([]error, 0, 4)
	visited := make(map[error]struct{})
	var collect func(error, int)
	collect = func(current error, depth int) {
		if current == nil || depth > maxRequestErrorDepth || len(nodes) >= maxRequestErrorDepth {
			return
		}
		if requestErrorComparable(current) {
			if _, seen := visited[current]; seen {
				return
			}
			visited[current] = struct{}{}
		}
		nodes = append(nodes, current)
		if joined, ok := current.(interface{ Unwrap() []error }); ok {
			for _, cause := range joined.Unwrap() {
				collect(cause, depth+1)
			}
			return
		}
		collect(errors.Unwrap(current), depth+1)
	}
	collect(err, 0)
	return nodes
}

func requestFailureIs(err, target error) bool {
	if err == nil {
		return target == nil
	}
	if target == nil {
		return false
	}
	targetComparable := requestErrorComparable(target)
	for _, current := range requestFailureErrors(err) {
		if targetComparable && requestErrorComparable(current) && current == target {
			return true
		}
		if matcher, ok := current.(interface{ Is(error) bool }); ok && matcher.Is(target) {
			return true
		}
	}
	return false
}

func requestFailureDiagnostic(err error) string {
	message := requestFailureUnknown
	if authenticationMessage := PostToolAuthenticationMessage(err); authenticationMessage != "" {
		message = authenticationMessage
	} else {
		switch {
		case requestFailureIs(err, errConversationStateChanged):
			message = errConversationStateChanged.Error()
		case requestFailureIs(err, errConversationStateTooLarge):
			message = errConversationStateTooLarge.Error()
		case requestFailureIs(err, errConversationLockBusy):
			message = requestFailureLockBusy
		case requestFailureIs(err, errCredentialPermissions):
			message = requestFailureCredentialMode
		case requestFailureIs(err, errConversationFilePermissions):
			message = requestFailureConversationMode
		case requestFailureIs(err, errConversationDirectoryPermissions):
			message = requestFailureDirectoryMode
		case requestFailureIs(err, errConversationSaveFailed):
			message = requestFailureStateSave
		case requestFailureIs(err, context.DeadlineExceeded):
			message = requestFailureTimeout
		case requestFailureIs(err, context.Canceled):
			message = requestFailureInterrupted
		case requestFailureIs(err, errCredentialsMissing):
			message = requestFailureCredentialsMissing
		case requestFailureIs(err, ErrLoginRequired) || requestFailureIs(err, errRequestAuthentication):
			message = requestFailureAuthentication
		case requestFailureIs(err, ErrModelUnavailable):
			message = requestFailureModelUnavailable
		case IsModelStreamError(err):
			message = ModelStreamMessageForError(err, "")
		}
	}
	if requestFailureIs(err, errConversationSaveFailed) && message != requestFailureStateSave {
		message = requestFailureStateSave + "\n" + message
	}
	return requestFailureMessage(message, err) + "\n" + requestUncertaintyWarning
}

func requestFailureMessage(message string, err error) string {
	if details := requestFailureDetails(err); details != "" {
		if strings.HasPrefix(details, message+"\n") {
			details = strings.TrimPrefix(details, message+"\n")
		}
		if details != "" && details != message {
			message += "\nUnderlying error: " + details
		}
	}
	return message
}

func requestRecoveryDiagnostic(err error) string {
	if requestFailureIs(err, ErrConversationRecovery) {
		return requestFailureMessage(ErrConversationRecovery.Error(), err)
	}
	if message := SkillRecoveryMessage(err); message != "" {
		return requestFailureMessage(message, err)
	}
	return ""
}

// requestFailureDetails keeps the full error text, including causes whose
// outer error intentionally uses a user-facing marker message.
func requestFailureDetails(err error) string {
	if err == nil {
		return ""
	}
	details := strings.Builder{}
	var collect func(error, int)
	count := 0
	collect = func(current error, depth int) {
		if current == nil || depth > maxRequestErrorDepth || count >= maxRequestErrorDepth {
			return
		}
		count++
		message := current.Error()
		if message != "" && !strings.Contains(details.String(), message) {
			if details.Len() > 0 {
				details.WriteByte('\n')
			}
			details.WriteString(message)
		}
		// ModelStreamError is the safe user-facing boundary. Its typed cause is
		// still retained for errors.Is/diagnostics, but arbitrary wire/read text
		// must not be copied into a terminal or agent-facing detail string.
		if _, boundary := current.(ModelStreamError); boundary {
			return
		}
		if joined, ok := current.(interface{ Unwrap() []error }); ok {
			for _, cause := range joined.Unwrap() {
				collect(cause, depth+1)
			}
			return
		}
		collect(errors.Unwrap(current), depth+1)
	}
	collect(err, 0)
	return details.String()
}

func safeRequestDiagnosticError(err error) error {
	if err == nil {
		return nil
	}
	message := requestRecoveryDiagnostic(err)
	if message == "" {
		message = requestFailureDiagnostic(err)
	}
	return safeRequestError{message: message, cause: err}
}

func requestOutputError(requestErr, outputErr error) error {
	message := requestRecoveryDiagnostic(requestErr)
	if message == "" {
		message = requestFailureDiagnostic(requestErr)
	}
	if details := requestFailureDetails(outputErr); details != "" {
		message += "\nOutput error: " + details
	}
	return safeRequestError{
		message: "Could not write request output.\n" + message,
		cause:   errors.Join(requestErr, outputErr),
	}
}

type requestErrorClass uint8

const (
	requestErrorFatal requestErrorClass = iota
	requestErrorRecoverable
)

var (
	errFatalRequest       = errors.New("fatal request")
	errRecoverableRequest = errors.New("recoverable request")
)

// classifiedRequestError gives the terminal loop a typed boundary without
// changing the underlying provider or persistence error returned to callers.
type classifiedRequestError struct {
	err   error
	class requestErrorClass
}

func (err classifiedRequestError) Error() string {
	return err.err.Error()
}

func (err classifiedRequestError) Unwrap() error {
	return err.err
}

func (err classifiedRequestError) Is(target error) bool {
	switch {
	case err.class == requestErrorFatal && target == errFatalRequest:
		return true
	case err.class == requestErrorRecoverable && target == errRecoverableRequest:
		return true
	default:
		return false
	}
}

func fatalRequestError(err error) error {
	if err == nil || errors.Is(err, errFatalRequest) {
		return err
	}
	return classifiedRequestError{err: err, class: requestErrorFatal}
}

func recoverableRequestError(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, errFatalRequest) || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return fatalRequestError(err)
	}
	if errors.Is(err, errRecoverableRequest) {
		return err
	}
	return classifiedRequestError{err: err, class: requestErrorRecoverable}
}

func classifyRequestError(err error) requestErrorClass {
	if err == nil {
		return requestErrorRecoverable
	}
	if errors.Is(err, errRecoverableRequest) && !errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded) {
		return requestErrorRecoverable
	}
	return requestErrorFatal
}

type Provider interface {
	Reply(ctx context.Context, model, message string) (string, error)
}

// ChatMessage is a conversation message that can be sent to a history-aware
// provider. Session persistence stores user and assistant messages separately
// from the summary supplied as context.
type ChatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// IsClearCommand reports whether a message is a /clear command or an attempted
// /clear command with extra arguments. The latter still needs command routing
// so it can be rejected without reaching the provider.
func IsClearCommand(message string) bool {
	fields := strings.Fields(message)
	return len(fields) > 0 && fields[0] == "/clear"
}

// HistoryProvider is optional so existing providers remain compatible with
// the one-message Provider interface.
type HistoryProvider interface {
	ReplyWithHistory(ctx context.Context, model string, history []ChatMessage) (string, error)
}

type conversationSummarizer interface {
	SummarizeConversation(ctx context.Context, model, previousSummary string, messages []ChatMessage) (string, error)
}

type Session struct {
	mu                          sync.Mutex
	provider                    Provider
	registry                    ModelRegistry
	chatID                      int64
	active                      string
	history                     []ChatMessage
	summary                     string
	migrationDiagnostics        []MigrationDiagnostic
	migrationDiagnosticsPending bool
	selectedSkills              []selectedSkill
	skillCheckpointFailed       bool
	recoveryPending             bool
	store                       *SessionStore
	storeVersion                [sha256.Size]byte // Expected persisted version for this session.
	memory                      *MemoryStore
	startupNotice               string
	contextNotice               string
}

type ToolCallObserver func(name, arguments string)

type toolCallObserverKey struct{}
type toolActionStartedKey struct{}
type codexToolProgressKey struct{}
type runtimeFailureOperationKey struct{}
type providerFailureBoundaryKey struct{}

func WithToolCallObserver(ctx context.Context, observer ToolCallObserver) context.Context {
	return context.WithValue(ctx, toolCallObserverKey{}, observer)
}

// WithRuntimeFailureOperation identifies the outer runtime boundary that owns
// one diagnostic record for a request. It carries no user or provider content.
func WithRuntimeFailureOperation(ctx context.Context, operation string) context.Context {
	return context.WithValue(ctx, runtimeFailureOperationKey{}, operation)
}

func toolCallObserver(ctx context.Context) ToolCallObserver {
	observer, _ := ctx.Value(toolCallObserverKey{}).(ToolCallObserver)
	return observer
}

func withCodexToolProgress(ctx context.Context, progress ToolProgress) context.Context {
	return context.WithValue(ctx, codexToolProgressKey{}, progress)
}

func codexToolProgressFromContext(ctx context.Context) ToolProgress {
	progress, _ := ctx.Value(codexToolProgressKey{}).(ToolProgress)
	if progress.Started < 0 || progress.Completed < 0 {
		return ToolProgress{}
	}
	return progress
}

type currentTurnAttachmentsKey struct{}

func NewSession(config Config, provider Provider) *Session {
	return NewSessionWithMemory(config, provider, nil)
}

// NewSessionWithMemory adds Markdown memory to normal provider requests while
// keeping it out of conversation history and persisted summaries.
func NewSessionWithMemory(config Config, provider Provider, memory *MemoryStore) *Session {
	active := strings.TrimSpace(config.Model)
	if active == "" {
		active = DefaultModel
	}
	session := &Session{
		provider: provider,
		registry: config.Registry,
		chatID:   config.ChatID,
		active:   active,
		memory:   memory,
	}
	session.refreshModel()
	return session
}

func withCurrentTurnAttachments(ctx context.Context, attachments []attachment.Metadata) context.Context {
	return context.WithValue(ctx, currentTurnAttachmentsKey{}, append([]attachment.Metadata(nil), attachments...))
}

func currentTurnAttachments(ctx context.Context) []attachment.Metadata {
	attachments, _ := ctx.Value(currentTurnAttachmentsKey{}).([]attachment.Metadata)
	return attachments
}

func (s *Session) Reply(ctx context.Context, message string) (string, error) {
	return s.replyWithAttachments(ctx, message, nil, false)
}

type scheduledTurnKey struct{}

// ReplyScheduled refreshes a stale on-disk conversation before running a job.
func (s *Session) ReplyScheduled(ctx context.Context, message string) (string, error) {
	return s.replyWithAttachments(context.WithValue(ctx, scheduledTurnKey{}, true), message, nil, true)
}

// ReplyWithAttachments sends one current-turn message with temporary local
// attachment references. The references are added only to the provider input;
// the persisted conversation keeps the original user text and never retains
// temporary paths.
func (s *Session) ReplyWithAttachments(ctx context.Context, message string, attachments []attachment.Metadata) (string, error) {
	if err := ctx.Err(); err != nil {
		failure := fatalRequestError(err)
		s.logSessionFailure(ctx, failure)
		return "", failure
	}
	if len(attachments) > attachment.MaxAttachments {
		err := recoverableRequestError(fmt.Errorf("too many attachments"))
		s.logSessionFailure(ctx, err)
		return "", err
	}
	for _, item := range attachments {
		if err := item.ValidateMetadata(); err != nil {
			failure := recoverableRequestError(fmt.Errorf("invalid attachment metadata: %w", err))
			s.logSessionFailure(ctx, failure)
			return "", failure
		}
	}
	return s.replyWithAttachments(ctx, message, attachments, false)
}

func (s *Session) replyWithAttachments(ctx context.Context, message string, attachments []attachment.Metadata, scheduled bool) (reply string, replyErr error) {
	defer func() {
		if replyErr != nil {
			s.logSessionFailure(ctx, replyErr)
		}
	}()
	// A session is a single ordered conversation. Serializing its turns also
	// ensures that only one caller summarizes a given history.
	s.mu.Lock()
	defer s.mu.Unlock()
	s.contextNotice = ""
	if err := ctx.Err(); err != nil {
		if scheduled {
			// No work has started, including when cancellation occurred while
			// this turn waited for the session mutex.
			return "", errors.Join(ErrSessionBusy, err)
		}
		return "", fatalRequestError(err)
	}
	if s.store != nil {
		// Hold a separate turn lock while model calls and tools are running.
		// The ordinary state lock is still available for our checkpoints.
		unlockTurn, err := s.store.lock(true)
		if err != nil {
			if scheduled {
				return "", errors.Join(ErrSessionBusy, err)
			}
			return "", fatalRequestError(fmt.Errorf("lock conversation turn: %w", err))
		}
		defer unlockTurn()
		unlockState, err := s.store.lock(false)
		if err != nil {
			if scheduled {
				return "", errors.Join(ErrSessionBusy, err)
			}
			return "", fatalRequestError(err)
		}
		current, err := sessionVersion(s.store.Path)
		unlockState()
		if err != nil {
			if scheduled {
				return "", errors.Join(ErrSessionBusy, err)
			}
			return "", fatalRequestError(err)
		}
		changed := current != s.storeVersion
		if s.store.hasPendingMigrationDiagnostics() && !s.migrationDiagnosticsPending {
			if scheduled {
				return "", errors.Join(ErrSessionBusy, errMigrationDiagnosticsPending)
			}
			return "", fatalRequestError(errMigrationDiagnosticsPending)
		}
		if changed || (scheduled && current == [sha256.Size]byte{}) {
			if s.migrationDiagnosticsPending {
				conflict := errors.Join(errConversationStateChanged, errMigrationDiagnosticsPending)
				if scheduled {
					return "", errors.Join(ErrSessionBusy, conflict)
				}
				return "", fatalRequestError(conflict)
			}
			if !scheduled {
				return "", fatalRequestError(errConversationStateChanged)
			}
			loaded, err := s.store.loadStateUnderTurnLock(&s.storeVersion)
			if err != nil {
				return "", errors.Join(ErrSessionBusy, fmt.Errorf("reload conversation for scheduled job: %w", err))
			}
			if loaded.Recovered {
				// Do not replace the in-memory history while recovery is awaiting
				// the user's acknowledgement, even if the scheduler found it.
				s.recoveryPending = true
			} else if changed && current == [sha256.Size]byte{} {
				s.recoveryPending = true
				if err := s.store.markMissing(); err != nil {
					return "", errors.Join(ErrSessionBusy, fmt.Errorf("record missing conversation: %w", err))
				}
			} else if changed {
				s.history = cloneChatMessages(loaded.Messages)
				s.summary = loaded.Summary
				s.migrationDiagnostics = cloneMigrationDiagnostics(loaded.MigrationDiagnostics)
				s.selectedSkills = append([]selectedSkill(nil), loaded.selectedSkills...)
				s.skillCheckpointFailed = false
				s.recoveryPending = false
			}
		}
	}
	if scheduled && s.recoveryPending {
		return "", errors.Join(ErrSessionBusy, errors.New("conversation recovery needs an interactive turn"))
	}
	if IsClearCommand(message) {
		if strings.TrimSpace(message) != "/clear" || len(attachments) != 0 {
			return "Usage: /clear", nil
		}
		if s.migrationDiagnosticsPending {
			return "", fatalRequestError(errors.Join(errConversationStateChanged, errMigrationDiagnosticsPending))
		}
		if s.store != nil {
			version, err := s.store.saveStateWithVersionAndDiagnostics("", nil, nil, s.storeVersion, true, nil, true)
			if err != nil {
				return "", fatalRequestError(conversationSaveError{operation: "save cleared conversation state", cause: err})
			}
			s.storeVersion = version
		}
		s.history = nil
		s.summary = ""
		s.migrationDiagnostics = nil
		s.selectedSkills = nil
		s.skillCheckpointFailed = false
		s.recoveryPending = false
		s.contextNotice = ""
		return "Conversation history cleared.", nil
	}
	if s.recoveryPending {
		return "", recoverableRequestError(ErrConversationRecovery)
	}
	if s.skillCheckpointFailed {
		err := fatalRequestError(skillRecovery(errors.New("skill selection could not be saved; restart to reload the conversation"), skillSaveMessage))
		if scheduled {
			return "", errors.Join(ErrSessionBusy, err)
		}
		return "", err
	}
	if s.memory != nil {
		if s.memory.initErr != nil {
			if scheduled {
				return "", errors.Join(ErrSessionBusy, s.memory.initErr)
			}
			return "", recoverableRequestError(s.memory.initErr)
		}
	}
	s.refreshModel()
	// Build the same memory context used by the provider before calculating
	// the budget, so instructions and restored skills have room alongside
	// both recent history and any compaction summary.
	replyContext := withCurrentTurnAttachments(ctx, attachments)
	if s.memory != nil {
		memoryContext := s.memory.GetMemoryContext()
		if memoryContext != (MemoryContext{}) {
			replyContext = WithMemoryContext(replyContext, memoryContext)
		}
	}
	overhead := 0
	tracksTools := false
	if provider, ok := s.provider.(interface {
		skillContextOverhead(context.Context, []selectedSkill) (int, error)
	}); ok {
		tracksTools = true
		var err error
		overhead, err = provider.skillContextOverhead(replyContext, s.selectedSkills)
		if err != nil {
			if scheduled {
				return "", errors.Join(ErrSessionBusy, err)
			}
			return "", recoverableRequestError(err)
		}
	}

	historyMessage := attachment.HistoryPlaceholder(message, attachments)
	candidate := append(cloneChatMessages(s.history), ChatMessage{Role: "user", Content: historyMessage})
	normalized, trimmed, err := normalizeChatMessages(candidate, false)
	if err != nil {
		normalized, trimmed = boundChatHistory(candidate)
	}
	candidate = normalized
	summary := s.summary
	if shouldSummarize(summary, candidate, overhead) {
		keepFrom := len(candidate) - recentMessagesToKeep
		provider, canSummarize := s.provider.(conversationSummarizer)
		_, canUseHistory := s.provider.(HistoryProvider)
		var compact string
		if canSummarize && canUseHistory {
			compact, err = provider.SummarizeConversation(context.WithValue(ctx, providerFailureBoundaryKey{}, true), s.active, summary, cloneChatMessages(candidate[:keepFrom]))
		} else {
			err = errors.New("provider cannot summarize conversation history")
		}
		if scheduled && ScheduledActionReviewMessage(err) != "" {
			if ctxErr := ctx.Err(); ctxErr != nil {
				if PostToolAuthenticationReviewMessage(err) != "" {
					return "", fatalRequestError(postToolAuthenticationError{cause: errors.Join(err, ctxErr), parentErr: ctxErr})
				}
				return "", fatalRequestError(errors.Join(err, ctxErr))
			}
			return "", fatalRequestError(err)
		}
		if ctxErr := ctx.Err(); ctxErr != nil {
			if scheduled {
				return "", errors.Join(ErrSessionBusy, err, ctxErr)
			}
			return "", fatalRequestError(errors.Join(err, ctxErr))
		}
		if scheduled && (errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)) {
			// Summarization does not execute tools; a child interruption must
			// leave the scheduled turn available to retry before any action.
			return "", errors.Join(ErrSessionBusy, err)
		}
		if err != nil {
			summaryContext := WithRuntimeFailureOperation(ctx, "chat.session.summarize")
			s.logSessionFailure(summaryContext, ModelRequestError{Model: s.active, Err: err})
		}
		compact = strings.TrimSpace(compact)
		if err == nil && compact != "" && utf8.ValidString(compact) && len(compact) <= maxSummaryBytes {
			summary = compact
			candidate = cloneChatMessages(candidate[keepFrom:])
			// Checkpoint the compacted old conversation before the next model
			// request, without recording a user turn that might fail.
			previousTurns := cloneChatMessages(candidate[:len(candidate)-1])
			if s.store != nil {
				version, saveErr := s.store.saveStateWithVersion(summary, previousTurns, s.selectedSkills, s.storeVersion, true)
				if saveErr != nil {
					return "", fatalRequestError(conversationSaveError{operation: "save conversation state", cause: saveErr})
				}
				s.storeVersion = version
				if s.migrationDiagnosticsPending {
					s.migrationDiagnosticsPending = false
				}
			}
			s.summary, s.history = summary, previousTurns
		} else {
			// Leave room for the next turns if compaction is unavailable. The
			// prior summary remains useful even when newer history is trimmed.
			var fallbackTrimmed bool
			fallbackBudget := (maxContextTokens - overhead) / 2
			candidate, fallbackTrimmed = boundSessionMessages(summary, candidate, true, fallbackBudget, maxSessionMessages/2)
			trimmed = trimmed || fallbackTrimmed
		}
	}

	recoveryNotice := s.registry.takeRecoveryNotice()
	modelNotice := ChatMessage{Role: "system", Content: "Pending GoAster model configuration recovery notice: " + recoveryNotice}
	budget, maxMessages := maxContextTokens-overhead, maxSessionMessages
	if recoveryNotice != "" {
		budget -= estimateMessageTokens(modelNotice)
		maxMessages--
	}
	diagnosticNotice := migrationDiagnosticsMessage(s.migrationDiagnostics)
	modelDiagnosticNotice := ChatMessage{Role: "system", Content: diagnosticNotice}
	if diagnosticNotice != "" {
		budget -= estimateMessageTokens(modelDiagnosticNotice)
		maxMessages--
	}
	bounded, budgetTrimmed := boundSessionMessages(summary, candidate, trimmed, budget, maxMessages)
	trimmed = trimmed || budgetTrimmed
	if trimmed {
		bounded, _ = boundSessionMessages(summary, candidate, true, budget, maxMessages)
		s.contextNotice = contextTrimNotice
	}
	requestHistory := make([]ChatMessage, 0, len(bounded)+2)
	if summary != "" {
		requestHistory = append(requestHistory, summaryMessage(summary))
	}
	if diagnosticNotice != "" {
		requestHistory = append(requestHistory, modelDiagnosticNotice)
	}
	if trimmed {
		requestHistory = append(requestHistory, ChatMessage{Role: "system", Content: contextTrimNotice})
	}
	requestHistory = append(requestHistory, bounded...)
	if recoveryNotice != "" {
		requestHistory = append([]ChatMessage{modelNotice}, requestHistory...)
	}
	requestMessage := message
	if len(bounded) > 0 {
		requestMessage = bounded[len(bounded)-1].Content
	}
	if len(attachments) > 0 {
		requestMessage = attachment.AddReferences(requestMessage, attachments)
		if len(requestHistory) > 0 && requestHistory[len(requestHistory)-1].Role == "user" {
			requestHistory[len(requestHistory)-1].Content = attachment.AddReferences(requestHistory[len(requestHistory)-1].Content, attachments)
		}
	}

	// The session owns selections, even when several sessions share a provider.
	// Checkpoint before a selected skill can lead to another tool call.
	replyContext = context.WithValue(replyContext, skillSessionKey{}, skillSessionContext{
		selected: func() []selectedSkill {
			return append([]selectedSkill(nil), s.selectedSkills...)
		},
		remember: func(next selectedSkill) error {
			selected := append([]selectedSkill(nil), s.selectedSkills...)
			found := false
			for i, current := range selected {
				if current.Name == next.Name {
					if current.Digest != "" && current.Digest != next.Digest {
						return errors.New("selected skill changed while loading; use /clear to start a new conversation")
					}
					selected[i], found = next, true
					break
				}
			}
			if !found {
				selected = append(selected, next)
			}
			if !validSelectedSkills(selected) {
				return errors.New("invalid skill selection")
			}
			if s.store != nil {
				version, err := s.store.saveStateWithVersion(s.summary, s.history, selected, s.storeVersion, true)
				if err != nil {
					s.skillCheckpointFailed = true
					return fatalRequestError(skillRecovery(fmt.Errorf("save selected skill: %w", err), skillSaveMessage))
				}
				s.storeVersion = version
				if s.migrationDiagnosticsPending {
					s.migrationDiagnosticsPending = false
				}
			}
			s.selectedSkills = selected
			if len(selected) > maxSessionSkills {
				return errors.New("too many selected skills; use /clear to start a new conversation")
			}
			return nil
		},
	})
	actionStarted := false
	if scheduled && tracksTools {
		replyContext = context.WithValue(replyContext, toolActionStartedKey{}, func() { actionStarted = true })
	}
	turnMigrationDiagnostics := cloneMigrationDiagnostics(s.migrationDiagnostics)
	checkpointMigrationDiagnostics := func(values []MigrationDiagnostic) error {
		safe, _ := normalizeMigrationDiagnostics(values)
		s.migrationDiagnostics = cloneMigrationDiagnostics(safe)
		if s.store != nil {
			s.migrationDiagnosticsPending = true
			s.store.setPendingMigrationDiagnostics(safe)
			version, err := s.store.saveStateWithVersionAndDiagnostics(s.summary, s.history, s.selectedSkills, s.storeVersion, true, safe, true)
			if err != nil {
				return conversationSaveError{operation: "save migration diagnostic checkpoint", cause: err}
			}
			s.storeVersion = version
			s.migrationDiagnosticsPending = false
		} else {
			s.migrationDiagnosticsPending = false
		}
		return nil
	}
	replyContext = WithMigrationDiagnosticObserver(replyContext, func(output string) error {
		diagnostic, ok := migrationDiagnosticFromToolOutput(output)
		if !ok {
			return nil
		}
		next := appendMigrationDiagnostic(turnMigrationDiagnostics, diagnostic)
		if migrationDiagnosticsEqual(turnMigrationDiagnostics, next) {
			return nil
		}
		turnMigrationDiagnostics = cloneMigrationDiagnostics(next)
		if err := checkpointMigrationDiagnostics(next); err != nil {
			return err
		}
		return nil
	})
	reply, err = s.replyWithHistory(replyContext, requestMessage, requestHistory)
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			if PostToolAuthenticationReviewMessage(err) != "" {
				return "", fatalRequestError(postToolAuthenticationError{cause: errors.Join(err, ctxErr), parentErr: ctxErr})
			}
			if scheduled {
				if ScheduledActionReviewMessage(err) != "" {
					return "", fatalRequestError(errors.Join(err, ctxErr))
				}
				if !tracksTools {
					return "", fatalRequestError(unknownActionError{cause: errors.Join(err, ctxErr)})
				}
				if actionStarted {
					return "", fatalRequestError(interruptedActionError{cause: errors.Join(err, ctxErr)})
				}
				return "", errors.Join(ErrSessionBusy, err, ctxErr)
			}
			return "", fatalRequestError(errors.Join(err, ctxErr))
		}
		if scheduled && ScheduledActionReviewMessage(err) == "" && (errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)) {
			// A child request or tool can be interrupted while ctx is still live.
			// Preserve any existing review marker before deciding whether retry
			// is safe, especially a 401 after the setup-only load_skill tool.
			if !tracksTools {
				return "", fatalRequestError(unknownActionError{cause: err})
			}
			if actionStarted {
				return "", fatalRequestError(interruptedActionError{cause: err})
			}
			return "", errors.Join(ErrSessionBusy, err)
		}
		if scheduled && tracksTools && !actionStarted && ScheduledActionReviewMessage(err) == "" && SkillRecoveryMessage(err) != "" {
			return "", errors.Join(ErrSessionBusy, err)
		}
		return "", recoverableRequestError(err)
	}

	// A provider can quote the current-turn reference or image data in its
	// answer. Do not return or retain either after this turn.
	historyReply := reply
	for _, item := range attachments {
		historyReply = strings.ReplaceAll(historyReply, item.Path, "[temporary attachment]")
		directory := filepath.Dir(item.Path)
		if strings.HasPrefix(filepath.Base(directory), attachment.AgentTemporaryDirectoryPrefix) {
			historyReply = strings.ReplaceAll(historyReply, directory, "[temporary attachment directory]")
		}
		if item.Type == "image" && len(historyReply) >= base64.StdEncoding.EncodedLen(int(item.Size)) {
			if dataURL, err := attachment.ReadImageDataURL(item); err == nil {
				historyReply = strings.ReplaceAll(historyReply, dataURL, "[image data]")
				_, encoded, _ := strings.Cut(dataURL, ",")
				historyReply = strings.ReplaceAll(historyReply, encoded, "[image data]")
			}
		}
	}
	completedCandidate := append(cloneChatMessages(bounded), ChatMessage{Role: "assistant", Content: historyReply})
	completed, completedTrimmed, err := normalizeChatMessages(completedCandidate, false)
	if err != nil {
		completed, completedTrimmed = boundChatHistory(completedCandidate)
	}
	completed, completedBudgetTrimmed := boundSessionMessages(summary, completed, false, maxContextTokens-overhead, maxSessionMessages)
	if completedTrimmed || completedBudgetTrimmed {
		s.contextNotice = contextTrimNotice
	}
	if s.store != nil {
		version, saveErr := s.store.saveStateWithVersionAndDiagnostics(summary, completed, s.selectedSkills, s.storeVersion, true, turnMigrationDiagnostics, true)
		if saveErr != nil {
			return "", fatalRequestError(conversationSaveError{operation: "save conversation state", cause: saveErr})
		}
		s.storeVersion = version
	}
	s.history = completed
	s.summary = summary
	s.migrationDiagnostics = cloneMigrationDiagnostics(turnMigrationDiagnostics)
	s.migrationDiagnosticsPending = false
	s.recoveryPending = false
	return historyReply, nil
}

func shouldSummarize(summary string, messages []ChatMessage, overhead int) bool {
	if len(messages) <= recentMessagesToKeep {
		return false
	}
	count := len(messages)
	tokens := overhead
	if summary != "" {
		count++
		tokens += estimateMessageTokens(summaryMessage(summary))
	}
	for _, message := range messages {
		tokens += estimateMessageTokens(message)
	}
	return count >= maxSessionMessages || tokens >= summarizeTokens
}

func (s *Session) reply(ctx context.Context, message string) (string, error) {
	return s.replyWithHistory(ctx, message, []ChatMessage{{Role: "user", Content: message}})
}

func (s *Session) replyWithHistory(ctx context.Context, message string, history []ChatMessage) (string, error) {
	providerContext := context.WithValue(ctx, providerFailureBoundaryKey{}, true)
	reply, err := s.providerReply(providerContext, s.active, message, history)
	if err == nil {
		return reply, nil
	}
	return "", ModelRequestError{Model: s.active, Err: err}
}

func (s *Session) logSessionFailure(ctx context.Context, err error) {
	if err == nil {
		return
	}
	operation := "chat.session.reply"
	if value, ok := ctx.Value(runtimeFailureOperationKey{}).(string); ok && strings.TrimSpace(value) != "" {
		operation = value
	}
	fields := make([]diagnostics.Field, 0, 3)
	if reference := requestReference(ctx); reference != "" {
		fields = append(fields, diagnostics.Field{Key: "request_reference", Value: reference})
	}
	var modelErr ModelRequestError
	if errors.As(err, &modelErr) {
		fields = append(fields, configuredModelDiagnostic(s.registry, modelErr.Model)...)
	}
	if phase, status, ok := providerFailureInfo(err); ok {
		fields = append(fields, diagnostics.Field{Key: "phase", Value: phase})
		if status != 0 {
			fields = append(fields, diagnostics.Field{Key: "status", Value: fmt.Sprintf("%d", status)})
		}
	}
	diagnostics.Error(operation, err, fields...)
}

func (s *Session) providerReply(ctx context.Context, model, message string, history []ChatMessage) (string, error) {
	if provider, ok := s.provider.(HistoryProvider); ok {
		return provider.ReplyWithHistory(ctx, model, cloneChatMessages(history))
	}
	return s.provider.Reply(ctx, model, message)
}

func cloneChatMessages(messages []ChatMessage) []ChatMessage {
	return append([]ChatMessage(nil), messages...)
}
