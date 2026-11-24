package chat

import (
	"context"
	"goaster/internal/migration"
	"strings"
	"testing"
)

type channelToolRegistrar chan Tool

func (r channelToolRegistrar) RegisterTool(tool Tool) error { r <- tool; return nil }

type migrationToolTestRegistrar struct{ tool Tool }

func (r *migrationToolTestRegistrar) RegisterTool(tool Tool) error { r.tool = tool; return nil }

const migrationToolTargetName = "goaster-migration-0123456789abcdef"

func TestMigrationToolOneShotPrivate(t *testing.T) {
	r := &migrationToolTestRegistrar{}
	calls := 0
	err := RegisterMigrationTool(r, MigrationToolOperations{Create: func(_ context.Context, req migration.Request, owner int64) migration.OneShotResult {
		calls++
		if owner != 7 || req.ServerType != "cx22" {
			t.Error("wrong request")
		}
		return migration.OneShotResult{Status: "success", CreationState: "created", Handoff: "delivered"}
	}})
	if err != nil {
		t.Fatal(err)
	}
	required, ok := r.tool.Parameters["required"].([]string)
	if !ok || len(required) != 3 || required[0] != "action" || required[1] != "server_type" || required[2] != "server_name" {
		t.Fatalf("migration tool required fields = %#v", r.tool.Parameters["required"])
	}
	ctx := WithConversationID(WithConversationType(context.Background(), "private"), 7)
	for _, bad := range []string{`{"action":"unsupported","server_type":"cx22"}`, `{"action":"create","server_type":"cx22","unexpected":"x"}`, `{"action":"create","server_type":"cx22","server_name":"` + migrationToolTargetName + `","unexpected":"x"}`} {
		r.tool.Handler(ctx, bad)
	}
	r.tool.Handler(WithConversationType(context.Background(), "group"), `{"action":"create","server_type":"cx22","server_name":"`+migrationToolTargetName+`"}`)
	if calls != 0 {
		t.Fatal("unexpected create")
	}
	result, err := r.tool.Handler(ctx, `{"action":"create","server_type":"cx22","server_name":"`+migrationToolTargetName+`","locations":[]}`)
	if err != nil || calls != 1 || !strings.Contains(result, `"creation_state":"created"`) {
		t.Fatal(result, err, calls)
	}
	result, err = r.tool.Handler(ctx, `{"action":"create","server_type":"cx22","server_name":"`+migrationToolTargetName+`"}`)
	if err != nil || calls != 2 || !strings.Contains(result, `"creation_state":"created"`) {
		t.Fatal("fresh attempt unexpectedly gated", result, err, calls)
	}
	scheduled := context.WithValue(ctx, scheduledTurnKey{}, true)
	r.tool.Handler(scheduled, `{"action":"create","server_type":"cx22"}`)
	if calls != 2 {
		t.Fatal("scheduled creation was allowed")
	}
}

func TestMigrationToolRejectsInvalidServerNamesBeforeCreate(t *testing.T) {
	tools := make(chan Tool, 1)
	if err := RegisterMigrationTool(channelToolRegistrar(tools), MigrationToolOperations{Create: func(context.Context, migration.Request, int64) migration.OneShotResult {
		t.Fatal("invalid server name reached immediate create")
		return migration.OneShotResult{}
	}}); err != nil {
		t.Fatal(err)
	}
	tool := <-tools
	ctx := WithConversationID(WithConversationType(context.Background(), "private"), 7)
	for _, tc := range []struct {
		name string
		args string
	}{
		{name: "missing server type", args: `{"action":"create","server_name":"` + migrationToolTargetName + `"}`},
		{name: "empty server type", args: `{"action":"create","server_type":"","server_name":"` + migrationToolTargetName + `"}`},
		{name: "whitespace server type", args: `{"action":"create","server_type":" cx22 ","server_name":"` + migrationToolTargetName + `"}`},
		{name: "omitted", args: `{"action":"create","server_type":"cx22"}`},
		{name: "empty", args: `{"action":"create","server_type":"cx22","server_name":""}`},
		{name: "whitespace-only", args: `{"action":"create","server_type":"cx22","server_name":"   "}`},
		{name: "null", args: `{"action":"create","server_type":"cx22","server_name":null}`},
		{name: "malformed", args: `{"action":"create","server_type":"cx22","server_name":"goaster-migration-0123456789ABCDEf"}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			result, err := tool.Handler(ctx, tc.args)
			if err != nil || !strings.Contains(result, `"code":"invalid_arguments"`) {
				t.Fatalf("result=%q err=%v", result, err)
			}
		})
	}
}

func TestScheduledMigrationToolRejectsInvalidServerNamesBeforeCreate(t *testing.T) {
	tools := make(chan Tool, 1)
	if err := RegisterMigrationTool(channelToolRegistrar(tools), MigrationToolOperations{}); err != nil {
		t.Fatal(err)
	}
	tool := <-tools
	ctx := WithConversationID(WithConversationType(context.Background(), "private"), 7)
	ctx = context.WithValue(ctx, scheduledTurnKey{}, true)
	callbackCalls := 0
	ctx = WithScheduledMigration(ctx, func(context.Context, migration.Request, string) migration.OneShotResult {
		callbackCalls++
		return migration.OneShotResult{CreationState: "not_created", Code: "invalid_arguments"}
	})
	for _, args := range []string{
		`{"action":"create","server_type":"cx22"}`,
		`{"action":"create","server_type":"cx22","server_name":""}`,
		`{"action":"create","server_type":"cx22","server_name":"   "}`,
		`{"action":"create","server_type":"cx22","server_name":null}`,
		`{"action":"create","server_type":"cx22","server_name":"goaster-migration-0123456789ABCDEf"}`,
	} {
		result, err := tool.Handler(ctx, args)
		if err != nil || !strings.Contains(result, `"code":"invalid_arguments"`) {
			t.Fatalf("result=%q err=%v", result, err)
		}
	}
	if callbackCalls != 5 {
		t.Fatalf("scheduled rejection callback calls = %d, want 5", callbackCalls)
	}
}

func TestScheduledMigrationRequiresTransportCallback(t *testing.T) {
	tools := make(chan Tool, 1)
	if err := RegisterMigrationTool(channelToolRegistrar(tools), MigrationToolOperations{}); err != nil {
		t.Fatal(err)
	}
	tool := <-tools
	ctx := WithConversationID(WithConversationType(context.Background(), "private"), 7)
	ctx = context.WithValue(ctx, scheduledTurnKey{}, true)
	args := `{"action":"create","server_type":"cx22","server_name":"goaster-migration-0123456789abcdef","locations":["fsn1"]}`
	result, err := tool.Handler(ctx, args)
	if err != nil || !strings.Contains(result, "capability_unavailable") {
		t.Fatal(result, err)
	}
	calls := 0
	ctx = WithScheduledMigration(ctx, func(_ context.Context, req migration.Request, _ string) migration.OneShotResult {
		calls++
		if req.ServerName != "goaster-migration-0123456789abcdef" || req.ServerType != "cx22" || len(req.Locations) != 1 || req.Locations[0] != "fsn1" {
			t.Errorf("wrong request: %+v", req)
		}
		return migration.OneShotResult{CreationState: "not_created", Code: "no_capacity"}
	})
	result, err = tool.Handler(ctx, args)
	if err != nil || calls != 1 || !strings.Contains(result, "no_capacity") {
		t.Fatal(result, err, calls)
	}
}

func TestMigrationToolExposesOnlySafeProviderDiagnostics(t *testing.T) {
	tools := make(chan Tool, 1)
	if err := RegisterMigrationTool(channelToolRegistrar(tools), MigrationToolOperations{Create: func(context.Context, migration.Request, int64) migration.OneShotResult {
		return migration.OneShotResult{
			Status:          "stop",
			CreationState:   "unknown",
			Code:            "invalid_input",
			Stage:           "provider",
			HTTPStatus:      422,
			ProviderCode:    "invalid_input",
			ProviderMessage: "invalid input",
			ProviderFields: []migration.ProviderField{
				{Field: "server_type", Messages: []string{"is not available"}},
				{Field: "private", Messages: []string{"private provider body"}},
			},
		}
	}}); err != nil {
		t.Fatal(err)
	}
	tool := <-tools
	ctx := WithConversationID(WithConversationType(context.Background(), "private"), 7)
	result, err := tool.Handler(ctx, `{"action":"create","server_type":"cx22","server_name":"`+migrationToolTargetName+`"}`)
	if err != nil || !strings.Contains(result, `"creation_state":"unknown"`) || !strings.Contains(result, `"provider_message":"invalid input"`) || !strings.Contains(result, `"field":"server_type"`) || strings.Contains(result, "private provider body") || strings.Contains(result, "private") {
		t.Fatalf("migration tool diagnostics = %q, error = %v", result, err)
	}
}

func TestMigrationToolNormalizesMissingOrInvalidCreationState(t *testing.T) {
	for _, test := range []struct {
		name, state string
	}{
		{name: "missing", state: ""},
		{name: "invalid", state: "maybe_created"},
	} {
		t.Run(test.name, func(t *testing.T) {
			tools := make(chan Tool, 1)
			if err := RegisterMigrationTool(channelToolRegistrar(tools), MigrationToolOperations{Create: func(context.Context, migration.Request, int64) migration.OneShotResult {
				return migration.OneShotResult{
					Status:        "stop",
					CreationState: test.state,
					Code:          "invalid_input",
					Stage:         "provider",
					HTTPStatus:    422,
					ProviderCode:  "invalid_input",
				}
			}}); err != nil {
				t.Fatal(err)
			}
			tool := <-tools
			ctx := WithConversationID(WithConversationType(context.Background(), "private"), 7)
			result, err := tool.Handler(ctx, `{"action":"create","server_type":"cx22","server_name":"`+migrationToolTargetName+`"}`)
			if err != nil || !strings.Contains(result, `"creation_state":"unknown"`) {
				t.Fatalf("migration tool result = %q, error = %v", result, err)
			}
		})
	}
}
