package chat

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"

	"goaster/internal/migration"
)

const migrationToolName = "goaster_migration"

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

type scheduledMigrationKey struct{}
type scheduledMigrationStartedKey struct{}

// WithScheduledMigrationStarted guards cron mutations after a scheduled
// create attempt begins.
func WithScheduledMigrationStarted(ctx context.Context, started func() bool) context.Context {
	return context.WithValue(ctx, scheduledMigrationStartedKey{}, started)
}

// WithScheduledMigration is set by the claimed generic cron executor, never by
// tool arguments.
func WithScheduledMigration(ctx context.Context, create func(context.Context, migration.Request, string) migration.OneShotResult) context.Context {
	return context.WithValue(ctx, scheduledMigrationKey{}, create)
}

// MigrationToolOperations contains the sole model-facing create capability.
// The owner identity and private handoff are supplied by the transport/runtime
// rather than by model arguments.
type MigrationToolOperations struct {
	Create func(context.Context, migration.Request, int64) migration.OneShotResult
}

// RegisterMigrationTool exposes one-shot server creation after ordinary
// explicit approval in a paired private conversation.
func RegisterMigrationTool(registrar ToolRegistrar, operations MigrationToolOperations) error {
	if registrar == nil {
		return errors.New("migration tool registrar is nil")
	}
	return registrar.RegisterTool(Tool{
		Name:        migrationToolName,
		Description: "Create one server after owner approval in a private chat. Every create must use the approved exact server_name and server_type; locations are optional strict constraints. Private URL and key handoff is sent directly to the paired owner.",
		Parameters: map[string]any{"type": "object", "properties": map[string]any{
			"action":      map[string]any{"type": "string", "enum": []string{"create"}},
			"server_type": map[string]any{"type": "string"},
			"server_name": map[string]any{"type": "string"},
			"locations":   map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
		}, "required": []string{"action", "server_type", "server_name"}, "additionalProperties": false},
		Handler: func(ctx context.Context, arguments string) (string, error) {
			return executeMigrationTool(ctx, arguments, operations)
		},
	})
}

type migrationToolArguments struct {
	Action     string   `json:"action"`
	ServerType string   `json:"server_type"`
	ServerName string   `json:"server_name"`
	Locations  []string `json:"locations,omitempty"`
}

func executeMigrationTool(ctx context.Context, arguments string, operations MigrationToolOperations) (string, error) {
	reject := func(code string) (string, error) {
		if create, ok := ctx.Value(scheduledMigrationKey{}).(func(context.Context, migration.Request, string) migration.OneShotResult); ok {
			create(ctx, migration.Request{}, code)
		}
		return `{"creation_state":"not_created","code":"` + code + `"}`, nil
	}
	parsed, err := parseMigrationToolArguments(arguments)
	if err != nil {
		return reject("invalid_arguments")
	}
	if err := ctx.Err(); err != nil {
		return reject("request_cancelled")
	}
	if parsed.Action != "create" || parsed.ServerType == "" || strings.TrimSpace(parsed.ServerType) != parsed.ServerType || !migration.ValidMigrationServerName(parsed.ServerName) {
		return reject("invalid_arguments")
	}
	if ConversationType(ctx) != "private" {
		return reject("private_conversation_required")
	}
	owner, ok := ConversationID(ctx)
	if !ok || owner <= 0 {
		return reject("owner_unavailable")
	}
	request := migration.Request{ServerType: parsed.ServerType, ServerName: parsed.ServerName, Locations: parsed.Locations}
	if scheduled, _ := ctx.Value(scheduledTurnKey{}).(bool); scheduled {
		create, ok := ctx.Value(scheduledMigrationKey{}).(func(context.Context, migration.Request, string) migration.OneShotResult)
		if !ok {
			return reject("capability_unavailable")
		}
		return encodeMigrationToolResult(create(ctx, request, ""))
	}
	if operations.Create == nil {
		return reject("capability_unavailable")
	}
	return encodeMigrationToolResult(operations.Create(ctx, request, owner))
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
