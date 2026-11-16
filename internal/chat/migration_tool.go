package chat

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"

	"goaster/internal/diagnostics"
	"goaster/internal/migration"
)

const migrationToolName = "goaster_migration"

const migrationRetryConfirmationWarning = "The previous attempt may already have created a server. Trying again could create a duplicate and add charges. I recommend checking your Hetzner account for duplicate servers. Are you sure you want me to try again?"

type conversationTypeKey struct{}
type conversationIDKey struct{}
type conversationThreadIDKey struct{}

// WithConversationType carries transport-provided conversation context.
func WithConversationType(ctx context.Context, kind string) context.Context {
	switch kind {
	case "private", "group", "supergroup":
	default:
		kind = "unknown"
	}
	return context.WithValue(ctx, conversationTypeKey{}, kind)
}

// WithConversationID carries the transport-validated chat identity. Model
// arguments never populate this value; the agent sets it from the inbound
// control request before invoking a session.
func WithConversationID(ctx context.Context, chatID int64) context.Context {
	return context.WithValue(ctx, conversationIDKey{}, chatID)
}

// ConversationID returns the transport-provided chat identity when present.
func ConversationID(ctx context.Context) (int64, bool) {
	chatID, ok := ctx.Value(conversationIDKey{}).(int64)
	return chatID, ok
}

// WithConversationThreadID carries an optional Telegram forum-topic identity.
func WithConversationThreadID(ctx context.Context, threadID int64) context.Context {
	return context.WithValue(ctx, conversationThreadIDKey{}, threadID)
}

// ConversationThreadID returns the transport-provided forum-topic identity.
func ConversationThreadID(ctx context.Context) (int64, bool) {
	threadID, ok := ctx.Value(conversationThreadIDKey{}).(int64)
	return threadID, ok
}

// ConversationType returns the normalized transport conversation type.
func ConversationType(ctx context.Context) string {
	kind, _ := ctx.Value(conversationTypeKey{}).(string)
	return kind
}

// MigrationToolOperations keeps the model-facing control plane separate from
// the privileged migration executor. No operation accepts a token, key,
// owner URL, chat ID, or callback secret.
type MigrationToolOperations struct {
	Create func(context.Context, migration.Request, int64) migration.OneShotResult
	Start  func(context.Context, migration.Request) (migration.Result, error)
	Status func(context.Context, string) (migration.StatusResult, error)
	Stop   func(context.Context, string) (migration.Result, error)
}

// RegisterMigrationTool exposes the source-side migration control plane. The
// handoff is delivered directly by the paired Telegram implementation after
// ordinary explicit approval in a private conversation.
func RegisterMigrationTool(registrar ToolRegistrar, operations MigrationToolOperations) error {
	if registrar == nil {
		return errors.New("migration tool registrar is nil")
	}
	return registrar.RegisterTool(Tool{
		Name:        migrationToolName,
		Description: "Create one server with the exact type and optional strict locations after owner approval in a private chat. Never schedule creation or automatically retry. A lost response may mean a server was created. The private setup handoff is sent directly to the paired owner.",
		Parameters: map[string]any{"type": "object", "properties": map[string]any{
			"action":      map[string]any{"type": "string", "enum": []string{"create"}},
			"server_type": map[string]any{"type": "string"},
			"locations":   map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
		}, "required": []string{"action", "server_type"}, "additionalProperties": false},
		Handler: func(ctx context.Context, arguments string) (string, error) {
			return executeMigrationTool(ctx, arguments, operations)
		},
	})
}

type migrationToolArguments struct {
	Action     string   `json:"action"`
	ServerType string   `json:"server_type,omitempty"`
	Locations  []string `json:"locations,omitempty"`
}

func executeMigrationTool(ctx context.Context, arguments string, operations MigrationToolOperations) (string, error) {
	parsed, err := parseMigrationToolArguments(arguments)
	if err != nil {
		return migrationToolRejected("invalid_arguments"), nil
	}
	if err := ctx.Err(); err != nil {
		return `{"creation_state":"not_created","code":"request_cancelled"}`, nil
	}
	if parsed.Action != "create" {
		return migrationToolRejected("invalid_arguments"), nil
	}
	if scheduled, _ := ctx.Value(scheduledTurnKey{}).(bool); scheduled || ConversationType(ctx) != "private" {
		return `{"creation_state":"not_created","code":"private_conversation_required"}`, nil
	}
	owner, ok := ConversationID(ctx)
	if !ok || owner <= 0 {
		return `{"creation_state":"not_created","code":"owner_unavailable"}`, nil
	}
	if operations.Create == nil {
		return `{"creation_state":"not_created","code":"capability_unavailable"}`, nil
	}
	return encodeMigrationToolResult(operations.Create(ctx, migration.Request{ServerType: parsed.ServerType, Locations: parsed.Locations}, owner))
}

func parseMigrationToolArguments(arguments string) (migrationToolArguments, error) {
	decoder := json.NewDecoder(strings.NewReader(arguments))
	decoder.DisallowUnknownFields()
	var parsed migrationToolArguments
	if err := decoder.Decode(&parsed); err != nil {
		return migrationToolArguments{}, errors.New("migration arguments are malformed")
	}
	var extra json.RawMessage
	if err := decoder.Decode(&extra); err != io.EOF {
		return migrationToolArguments{}, errors.New("migration arguments contain trailing data")
	}
	return parsed, nil
}

func encodeMigrationToolResult(value any) (string, error) {
	data, err := json.Marshal(value)
	if err != nil {
		return "", errors.Join(errors.New("encode migration tool result"), err)
	}
	return string(data), nil
}

type migrationToolErrorResponse struct {
	Status            string `json:"status"`
	Stage             string `json:"stage"`
	Code              string `json:"code"`
	Cause             string `json:"cause"`
	HTTPStatus        int    `json:"http_status,omitempty"`
	ProviderCode      string `json:"provider_code,omitempty"`
	MonitorID         string `json:"monitor_id,omitempty"`
	ResultState       string `json:"result_state"`
	CreationState     string `json:"creation_state"`
	MonitoringState   string `json:"monitoring_state"`
	MonitoringStopped bool   `json:"monitoring_stopped"`
	NextAction        string `json:"next_action"`
}

func migrationToolRejected(code string) string {
	return `{"creation_state":"not_created","code":"invalid_arguments"}`
}

func migrationToolOperationFailure(err error, result migration.Result) string {
	diagnostic := migration.DiagnosticFor(err, result)
	if !migration.IsMigrationFailure(err) {
		fields := migration.DiagnosticLogFields(diagnostic)
		diagnostics.Error("migration.tool", err, fields...)
	}
	return encodeMigrationToolError("error", diagnostic)
}

func migrationToolCapabilityFailure(err error) string {
	diagnostics.Error("migration.tool", err)
	return encodeMigrationToolError("error", migration.CapabilityUnavailableDiagnostic())
}

func safeMigrationMonitorID(value string) bool {
	if len(value) < len("migration-")+1 || len(value) > 96 || !strings.HasPrefix(value, "migration-") {
		return false
	}
	for _, character := range value {
		if !(character >= 'a' && character <= 'z' || character >= '0' && character <= '9' || character == '-') {
			return false
		}
	}
	return true
}

func encodeMigrationToolError(status string, diagnostic migration.Diagnostic) string {
	response := migrationToolErrorResponse{
		Status:            status,
		Stage:             diagnostic.Stage,
		Code:              diagnostic.Code,
		Cause:             diagnostic.Cause,
		HTTPStatus:        diagnostic.HTTPStatus,
		ProviderCode:      diagnostic.ProviderCode,
		MonitorID:         diagnostic.MonitorID,
		ResultState:       diagnostic.ResultState,
		CreationState:     diagnostic.CreationState,
		MonitoringState:   diagnostic.MonitoringState,
		MonitoringStopped: diagnostic.MonitoringStopped,
		NextAction:        diagnostic.NextAction,
	}
	data, err := json.Marshal(response)
	if err != nil {
		return `{"status":"error","stage":"tool","code":"unknown_failure","cause":"the migration failed for an unclassified reason","result_state":"error","creation_state":"unknown","monitoring_state":"unknown","monitoring_stopped":false,"next_action":"inspect status and reconcile external state before any new attempt"}`
	}
	return string(data)
}
