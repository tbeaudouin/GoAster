package chat

import (
	"context"
	"errors"
	"strings"
	"testing"

	"goaster/internal/migration"
)

func TestMigrationDirectScheduling(t *testing.T) {
	for _, tc := range []struct {
		status    string
		fail      bool
		want      string
		schedules int
	}{
		{"success", false, `"status":"success"`, 0}, {"stop", false, `"status":"stop"`, 0},
		{"retry_later", false, `"status":"retry_later"`, 1}, {"retry_later", true, `"status":"schedule_failed"`, 1},
	} {
		t.Run(tc.status+tc.want, func(t *testing.T) {
			creates, schedules := 0, 0
			ops := MigrationToolOperations{Create: func(_ context.Context, req migration.Request, owner int64) migration.OneShotResult {
				creates++
				if owner != 7 || req.ServerName != migrationToolTargetName || req.ServerType != "cx22" || len(req.Locations) != 1 || req.Locations[0] != "fsn1" {
					t.Fatal("request or origin changed")
				}
				return migration.OneShotResult{Status: tc.status, Handoff: "delivered", Code: "no_capacity", Stage: "provider", ProviderMessage: "secret arbitrary text"}
			}, Schedule: func(req migration.Request, owner int64) error {
				schedules++
				if owner != 7 || req.ServerName != migrationToolTargetName || req.Locations[0] != "fsn1" {
					t.Fatal("schedule changed")
				}
				if tc.fail {
					return errors.New("storage failure")
				}
				return nil
			}}
			ctx := WithConversationID(WithConversationType(context.Background(), "private"), 7)
			result, err := executeMigrationTool(ctx, `{"action":"create","server_name":"goaster-migration-0123456789abcdef","server_type":"cx22","locations":["fsn1"]}`, ops)
			if tc.status == "retry_later" && (!strings.Contains(result, `"code":"no_capacity"`) || !strings.Contains(result, `"stage":"provider"`) || strings.Contains(result, "secret arbitrary text")) {
				t.Fatalf("unsafe or missing retry details: %s", result)
			}
			if err != nil || creates != 1 || schedules != tc.schedules || !strings.Contains(result, tc.want) {
				t.Fatalf("result=%s err=%v creates=%d schedules=%d", result, err, creates, schedules)
			}
		})
	}
}

func TestMigrationLocalFailureNeverSchedules(t *testing.T) {
	ctx := WithConversationID(WithConversationType(context.Background(), "private"), 7)
	calls := 0
	result, err := executeMigrationTool(ctx, `{"action":"create","server_name":"goaster-migration-0123456789abcdef","server_type":"cx22"}`, MigrationToolOperations{
		Create: func(context.Context, migration.Request, int64) migration.OneShotResult {
			return migration.OneShotResult{Status: "retry_later", LocalFailure: true}
		},
		Schedule: func(migration.Request, int64) error { calls++; return nil },
	})
	if err != nil || calls != 0 || !strings.Contains(result, `"status":"failed"`) {
		t.Fatalf("result=%s err=%v schedules=%d", result, err, calls)
	}
}
