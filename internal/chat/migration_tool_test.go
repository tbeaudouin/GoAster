package chat

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync/atomic"
	"testing"

	"goaster/internal/migration"
)

type migrationToolTestRegistrar struct {
	tool Tool
}

func (registrar *migrationToolTestRegistrar) RegisterTool(tool Tool) error {
	registrar.tool = tool
	return nil
}

func TestMigrationToolExposesOnlySafeMonitorActions(t *testing.T) {
	registrar := &migrationToolTestRegistrar{}
	var starts atomic.Int32
	var received migration.Request
	if err := RegisterMigrationTool(registrar, MigrationToolOperations{
		Start: func(_ context.Context, request migration.Request) (migration.Result, error) {
			starts.Add(1)
			received = request
			return migration.Result{Status: "created", ServerType: request.ServerType}, nil
		},
	}); err != nil {
		t.Fatal(err)
	}
	if registrar.tool.Name != migrationToolName || registrar.tool.Handler == nil {
		t.Fatalf("registered migration tool = %#v", registrar.tool)
	}
	for _, required := range []string{
		"indicative public Hetzner pricing",
		"ordinary server-type and location discovery",
		"final authoritative availability",
		"approved start/monitor workflow",
		"any compatible location",
		"non-empty",
	} {
		if !strings.Contains(registrar.tool.Description, required) {
			t.Fatalf("tool description missing %q: %s", required, registrar.tool.Description)
		}
	}
	for _, forbidden := range []string{"estimate", "quote", "trusted paired-private", "approval code", "APPROVE GOASTER MIGRATION"} {
		if strings.Contains(registrar.tool.Description, forbidden) {
			t.Fatalf("model-facing migration description retained %q: %s", forbidden, registrar.tool.Description)
		}
	}
	parameters, err := json.Marshal(registrar.tool.Parameters)
	if err != nil {
		t.Fatal(err)
	}
	parameterText := string(parameters)
	for _, forbidden := range []string{"estimate", "quote", "availability", "hetzner_token", "private_key", "owner_url", "owner_secret", "callback_secret", "chat_id", "approved_hourly_gross", "approved_currency", "approved"} {
		if strings.Contains(parameterText, forbidden) {
			t.Fatalf("migration schema exposes forbidden parameter %q: %s", forbidden, parameterText)
		}
	}

	for _, arguments := range []string{
		`{"action":"start","server_type":"cx22","approved":true}`,
		`{"action":"start","server_type":"cx22","hetzner_token":"secret"}`,
		`{"action":"start","server_type":"cx22","job_id":"migration-forged"}`,
		`{"action":"start","server_type":"cx22","retry_attempt":"/tmp/not-a-monitor"}`,
		`{"action":"availability","server_type":"cx22"}`,
	} {
		result, err := registrar.tool.Handler(WithConversationType(context.Background(), "private"), arguments)
		if err != nil || !strings.Contains(result, `"status":"rejected"`) || starts.Load() != 0 {
			t.Fatalf("rejected model control input %q = %q, %v; starts=%d", arguments, result, err, starts.Load())
		}
	}
	scheduled := context.WithValue(WithConversationType(context.Background(), "private"), scheduledTurnKey{}, true)
	result, err := registrar.tool.Handler(scheduled, `{"action":"start","server_type":"cx22"}`)
	if err != nil || !strings.Contains(result, `"status":"rejected"`) || starts.Load() != 0 {
		t.Fatalf("scheduled start = %q, %v; starts=%d", result, err, starts.Load())
	}
	result, err = registrar.tool.Handler(WithConversationType(context.Background(), "private"), `{"action":"start","server_type":"cx22"}`)
	if err != nil || !strings.Contains(result, `"status":"created"`) || starts.Load() != 1 {
		t.Fatalf("trusted start delegation = %q, %v; starts=%d", result, err, starts.Load())
	}
	if received.ServerType != "cx22" || received.ApprovedHourlyGross != "" || received.ApprovedCurrency != "" || received.JobID != "" {
		t.Fatalf("model-controlled approval fields reached start operation: %#v", received)
	}
	result, err = registrar.tool.Handler(WithConversationType(context.Background(), "private"), `{"action":"start","server_type":"cx22","retry_attempt":"migration-prior-attempt"}`)
	if err != nil || !strings.Contains(result, `"status":"created"`) || starts.Load() != 2 || received.RetryAttempt != "migration-prior-attempt" {
		t.Fatalf("scoped retry delegation = %q, %v; starts=%d request=%#v", result, err, starts.Load(), received)
	}
	scheduledRetry := context.WithValue(WithConversationType(context.Background(), "private"), scheduledTurnKey{}, true)
	result, err = registrar.tool.Handler(scheduledRetry, `{"action":"start","server_type":"cx22","retry_attempt":"migration-prior-attempt"}`)
	if err != nil || !strings.Contains(result, `"status":"rejected"`) || starts.Load() != 2 {
		t.Fatalf("scheduled retry = %q, %v; starts=%d", result, err, starts.Load())
	}

	var actions []string
	ops := MigrationToolOperations{
		Status: func(_ context.Context, jobID string) (migration.StatusResult, error) {
			actions = append(actions, "status:"+jobID)
			return migration.StatusResult{MonitorID: jobID, MonitorOn: true, Migration: migration.Result{Status: "monitoring", MonitorID: jobID}}, nil
		},
		Stop: func(_ context.Context, jobID string) (migration.Result, error) {
			actions = append(actions, "stop:"+jobID)
			return migration.Result{Status: "stopped", MonitorID: jobID}, nil
		},
	}
	result, err = executeMigrationTool(context.Background(), `{"action":"status","job_id":"migration-safe"}`, ops)
	if err != nil || !strings.Contains(result, `"monitor_id":"migration-safe"`) || len(actions) != 1 {
		t.Fatalf("status action = %q, %v actions=%v", result, err, actions)
	}
	result, err = executeMigrationTool(context.Background(), `{"action":"status","job_id":"migration-safe","retry_attempt":"migration-prior-attempt"}`, ops)
	if err != nil || !strings.Contains(result, `"status":"rejected"`) || len(actions) != 1 {
		t.Fatalf("status retry bypass = %q, %v actions=%v", result, err, actions)
	}
	result, err = executeMigrationTool(context.Background(), `{"action":"stop","job_id":"migration-safe"}`, ops)
	if err != nil || !strings.Contains(result, `"status":"stopped"`) || len(actions) != 2 {
		t.Fatalf("stop action = %q, %v actions=%v", result, err, actions)
	}
	result, err = executeMigrationTool(context.Background(), `{"action":"stop","job_id":"migration-safe","retry_attempt":"migration-prior-attempt"}`, ops)
	if err != nil || !strings.Contains(result, `"status":"rejected"`) || len(actions) != 2 {
		t.Fatalf("stop retry bypass = %q, %v actions=%v", result, err, actions)
	}
	result, err = executeMigrationTool(context.Background(), `{"action":"status","job_id":"/tmp/private"}`, ops)
	if err != nil || !strings.Contains(result, `"status":"rejected"`) || len(actions) != 2 {
		t.Fatalf("unsafe monitor ID = %q, %v actions=%v", result, err, actions)
	}
}

func TestMigrationToolReturnsBoundedStructuredFailures(t *testing.T) {
	operations := MigrationToolOperations{Start: func(context.Context, migration.Request) (migration.Result, error) {
		return migration.Result{Status: "manual_review", MonitorID: "migration-synthetic", ManualReview: true}, errors.Join(errors.New("synthetic-secret-value /private/token"), migration.ErrHetznerTokenMissing)
	}}
	result, err := executeMigrationTool(WithConversationType(context.Background(), "private"), `{"action":"start","server_type":"cx22"}`, operations)
	if err != nil {
		t.Fatal(err)
	}
	var response migrationToolErrorResponse
	if err := json.Unmarshal([]byte(result), &response); err != nil {
		t.Fatalf("structured result is not JSON: %q: %v", result, err)
	}
	if response.Code != "missing_hcloud_token" || response.Stage != "unknown" || response.MonitorID != "migration-synthetic" || response.ResultState != "manual_review" || response.CreationState != "not_created" || response.MonitoringStopped {
		t.Fatalf("missing-token diagnostic = %#v", response)
	}
	if strings.Contains(result, "synthetic-secret-value") || strings.Contains(result, "/private/token") {
		t.Fatalf("structured result leaked arbitrary cause: %s", result)
	}

	result, err = executeMigrationTool(WithConversationType(context.Background(), "private"), `{"action":"start","server_type":"cx22"}`, MigrationToolOperations{Start: func(context.Context, migration.Request) (migration.Result, error) {
		return migration.Result{Status: "created", MonitorID: "migration-created", ServerID: 7, ManualReview: true}, errors.Join(migration.ErrHandoffFailure, errors.New("private-link"))
	}})
	if err != nil || !strings.Contains(result, `"code":"handoff_failure"`) || !strings.Contains(result, `"result_state":"created"`) || !strings.Contains(result, `"creation_state":"created"`) || strings.Contains(result, "private-link") {
		t.Fatalf("created delivery failure = %q, %v", result, err)
	}

	result, err = executeMigrationTool(WithConversationType(context.Background(), "private"), `{"action":"start","server_type":"cx22"}`, MigrationToolOperations{Start: func(context.Context, migration.Request) (migration.Result, error) {
		return migration.Result{Status: "stop_unconfirmed", MonitorID: "migration-uncertain"}, errors.New("unclassified upstream body")
	}})
	if err != nil || !strings.Contains(result, `"result_state":"stop_unconfirmed"`) || !strings.Contains(result, `"creation_state":"not_created"`) || !strings.Contains(result, `"monitoring_stopped":false`) || strings.Contains(result, "unclassified upstream body") {
		t.Fatalf("uncertain failure = %q, %v", result, err)
	}
}

func TestMigrationToolCancellationIsStructuredAndSafe(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	result, err := executeMigrationTool(ctx, `{"action":"start","server_type":"cx22"}`, MigrationToolOperations{})
	if err != nil || !strings.Contains(result, `"code":"cancelled"`) || !strings.Contains(result, `"next_action"`) || strings.Contains(result, "context canceled") {
		t.Fatalf("cancelled tool call = %q, %v", result, err)
	}
}
