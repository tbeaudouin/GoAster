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
func TestMigrationToolOneShotPrivate(t *testing.T) {
	r := &migrationToolTestRegistrar{}
	calls := 0
	err := RegisterMigrationTool(r, MigrationToolOperations{Create: func(_ context.Context, req migration.Request, owner int64) migration.OneShotResult {
		calls++
		if owner != 7 || req.ServerType != "cx22" {
			t.Error("wrong request")
		}
		return migration.OneShotResult{CreationState: "created", Handoff: "delivered"}
	}})
	if err != nil {
		t.Fatal(err)
	}
	ctx := WithConversationID(WithConversationType(context.Background(), "private"), 7)
	for _, bad := range []string{`{"action":"start","server_type":"cx22"}`, `{"action":"status"}`, `{"action":"create","server_type":"cx22","retry_attempt":"x"}`} {
		r.tool.Handler(ctx, bad)
	}
	r.tool.Handler(WithConversationType(context.Background(), "group"), `{"action":"create","server_type":"cx22"}`)
	if calls != 0 {
		t.Fatal("unexpected create")
	}
	result, err := r.tool.Handler(ctx, `{"action":"create","server_type":"cx22","locations":[]}`)
	if err != nil || calls != 1 || !strings.Contains(result, `"creation_state":"created"`) {
		t.Fatal(result, err, calls)
	}
	result, err = r.tool.Handler(ctx, `{"action":"create","server_type":"cx22"}`)
	if err != nil || calls != 2 || !strings.Contains(result, `"creation_state":"created"`) {
		t.Fatal("fresh attempt unexpectedly gated", result, err, calls)
	}
	scheduled := context.WithValue(ctx, scheduledTurnKey{}, true)
	r.tool.Handler(scheduled, `{"action":"create","server_type":"cx22"}`)
	if calls != 2 {
		t.Fatal("scheduled creation was allowed")
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
