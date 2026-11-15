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

func TestMigrationToolKeepsSecretsAndModelApprovalOutOfSchema(t *testing.T) {
	registrar := &migrationToolTestRegistrar{}
	var starts atomic.Int32
	var received migration.Request
	var availabilityRequest migration.PriceRequest
	if err := RegisterMigrationTool(registrar, MigrationToolOperations{
		Availability: func(_ context.Context, request migration.PriceRequest) (migration.AvailabilityResult, error) {
			availabilityRequest = request
			return migration.AvailabilityResult{
				Version:            "v1",
				ServerType:         request.ServerType,
				LocationPolicy:     migration.LocationPolicyAllowlist,
				RequestedLocations: append([]string(nil), request.Locations...),
				Locations:          []string{"fsn1"},
			}, nil
		},
		Start: func(_ context.Context, request migration.Request) (migration.Result, error) {
			starts.Add(1)
			received = request
			return migration.Result{Status: "created"}, nil
		},
	}); err != nil {
		t.Fatal(err)
	}
	if registrar.tool.Name != migrationToolName || registrar.tool.Handler == nil {
		t.Fatalf("registered migration tool = %#v", registrar.tool)
	}
	for _, required := range []string{
		"may omit locations not currently available",
		"read-only",
		"repeats the requested location policy",
		"Pricing is informational only",
		"ordinary explicit user approval",
		"private conversation",
		"not a trusted cryptographic binding",
		"not recorded by a separate approval mechanism",
	} {
		if !strings.Contains(registrar.tool.Description, required) {
			t.Fatalf("tool description missing %q: %s", required, registrar.tool.Description)
		}
	}
	for _, forbidden := range []string{
		"trusted paired-private",
		"approval code",
		"APPROVE GOASTER MIGRATION",
	} {
		if strings.Contains(registrar.tool.Description, forbidden) {
			t.Fatalf("rejected approval prototype remained in tool description: %q", forbidden)
		}
	}
	parameters, err := json.Marshal(registrar.tool.Parameters)
	if err != nil {
		t.Fatal(err)
	}
	parameterText := string(parameters)
	for _, forbidden := range []string{"hetzner_token", "private_key", "owner_url", "owner_secret", "callback_secret", "chat_id", "approved_hourly_gross", "approved_currency", "approved"} {
		if strings.Contains(parameterText, forbidden) {
			t.Fatalf("migration schema exposes forbidden parameter %q: %s", forbidden, parameterText)
		}
	}

	result, err := registrar.tool.Handler(WithConversationType(context.Background(), "private"), `{"action":"start","server_type":"cx22","approved":true}`)
	if err != nil || !strings.Contains(result, `"rejected"`) || starts.Load() != 0 {
		t.Fatalf("model approval forgery = %q, %v; starts=%d", result, err, starts.Load())
	}
	result, err = registrar.tool.Handler(WithConversationType(context.Background(), "private"), `{"action":"start","server_type":"cx22","approved_hourly_gross":"0.10","approved_currency":"EUR","approved":true,"hetzner_token":"secret"}`)
	if err != nil || !strings.Contains(result, `"rejected"`) || starts.Load() != 0 {
		t.Fatalf("secret-bearing or model-approved start = %q, %v; starts=%d", result, err, starts.Load())
	}
	result, err = registrar.tool.Handler(WithConversationType(context.Background(), "private"), `{"action":"start","server_type":"cx22","job_id":"migration-forged"}`)
	if err != nil || !strings.Contains(result, `"rejected"`) || starts.Load() != 0 {
		t.Fatalf("model-selected monitor ID = %q, %v; starts=%d", result, err, starts.Load())
	}
	scheduled := context.WithValue(WithConversationType(context.Background(), "private"), scheduledTurnKey{}, true)
	result, err = registrar.tool.Handler(scheduled, `{"action":"start","server_type":"cx22"}`)
	if err != nil || !strings.Contains(result, `"rejected"`) || starts.Load() != 0 {
		t.Fatalf("scheduled start = %q, %v; starts=%d", result, err, starts.Load())
	}
	result, err = registrar.tool.Handler(WithConversationType(context.Background(), "private"), `{"action":"start","server_type":"cx22"}`)
	if err != nil || !strings.Contains(result, `"created"`) || starts.Load() != 1 {
		t.Fatalf("trusted start delegation = %q, %v; starts=%d", result, err, starts.Load())
	}
	if received.ServerType != "cx22" || received.ApprovedHourlyGross != "" || received.ApprovedCurrency != "" || received.JobID != "" {
		t.Fatalf("model-controlled approval fields reached start operation: %#v", received)
	}
	result, err = registrar.tool.Handler(context.Background(), `{"action":"availability","server_type":"cx22","locations":["fsn1"]}`)
	if err != nil || !strings.Contains(result, `"location_policy":"allowlist"`) || !strings.Contains(result, `"requested_locations":["fsn1"]`) {
		t.Fatalf("availability action = %q, %v", result, err)
	}
	if availabilityRequest.ServerType != "cx22" || len(availabilityRequest.Locations) != 1 || availabilityRequest.Locations[0] != "fsn1" {
		t.Fatalf("availability request = %#v", availabilityRequest)
	}
}

func TestMigrationToolReportsUnconfirmedStop(t *testing.T) {
	registrar := &migrationToolTestRegistrar{}
	if err := RegisterMigrationTool(registrar, MigrationToolOperations{Start: func(context.Context, migration.Request) (migration.Result, error) {
		return migration.Result{Status: "stop_unconfirmed", MonitorID: "migration-test"}, errors.New("synthetic failure")
	}}); err != nil {
		t.Fatal(err)
	}
	result, err := registrar.tool.Handler(WithConversationType(context.Background(), "private"), `{"action":"start","server_type":"cx22"}`)
	if err != nil || !strings.Contains(result, `"stop_unconfirmed"`) || !strings.Contains(result, `"migration-test"`) {
		t.Fatalf("result = %s, %v", result, err)
	}
	calls := 0
	ops := MigrationToolOperations{Start: func(context.Context, migration.Request) (migration.Result, error) {
		calls++
		return migration.Result{}, nil
	}}
	for _, kind := range []string{"", "group", "supergroup", "forged"} {
		result, err := executeMigrationTool(WithConversationType(context.Background(), kind), `{"action":"start","server_type":"cx22"}`, ops)
		if err != nil || !strings.Contains(result, `"rejected"`) || calls != 0 {
			t.Fatalf("kind %q: %s %v calls=%d", kind, result, err, calls)
		}
	}

}

func TestMigrationToolReportsMissingHetznerTokenConfiguration(t *testing.T) {
	operations := MigrationToolOperations{Start: func(context.Context, migration.Request) (migration.Result, error) {
		return migration.Result{Status: "manual_review", MonitorID: "migration-synthetic", ManualReview: true}, errors.Join(errors.New("synthetic-secret-value"), migration.ErrHetznerTokenMissing)
	}}
	result, err := executeMigrationTool(WithConversationType(context.Background(), "private"), `{"action":"start","server_type":"cx22"}`, operations)
	if err != nil {
		t.Fatal(err)
	}
	var response struct {
		Status          string `json:"status"`
		Code            string `json:"code"`
		Message         string `json:"message"`
		MigrationStatus string `json:"migration_status"`
		MonitorID       string `json:"monitor_id"`
		ManualReview    bool   `json:"manual_review"`
	}
	if err := json.Unmarshal([]byte(result), &response); err != nil {
		t.Fatalf("missing-token result is not JSON: %q: %v", result, err)
	}
	if response.Status != "configuration_required" || response.Code != "missing_hcloud_token" ||
		!strings.Contains(response.Message, "Add HCLOUD_TOKEN using /env first") ||
		!strings.Contains(response.Message, "No provider create request was sent") ||
		response.MigrationStatus != "manual_review" || response.MonitorID != "migration-synthetic" || !response.ManualReview {
		t.Fatalf("missing-token result = %#v (%s)", response, result)
	}
	if strings.Contains(result, "synthetic-secret-value") || strings.Contains(result, "manual review; no action was repeated") {
		t.Fatalf("missing-token result leaked a cause or used the generic response: %s", result)
	}

	estimateOperations := MigrationToolOperations{Estimate: func(context.Context, migration.PriceRequest) (migration.PriceEstimate, error) {
		return migration.PriceEstimate{}, migration.ErrHetznerTokenMissing
	}}
	estimateResult, err := executeMigrationTool(context.Background(), `{"action":"estimate","server_type":"cx22"}`, estimateOperations)
	if err != nil || !strings.Contains(estimateResult, `"code":"missing_hcloud_token"`) || !strings.Contains(estimateResult, "Add HCLOUD_TOKEN using /env first") {
		t.Fatalf("missing-token estimate result = %q, %v", estimateResult, err)
	}
}

func TestMigrationToolKeepsMalformedCredentialFailureUnavailable(t *testing.T) {
	operations := MigrationToolOperations{Start: func(context.Context, migration.Request) (migration.Result, error) {
		return migration.Result{Status: "manual_review", ManualReview: true}, errors.New("malformed settings contain synthetic-secret-value")
	}}
	result, err := executeMigrationTool(WithConversationType(context.Background(), "private"), `{"action":"start","server_type":"cx22"}`, operations)
	if err != nil || !strings.Contains(result, `"status":"unavailable"`) ||
		strings.Contains(result, "missing_hcloud_token") || strings.Contains(result, "synthetic-secret-value") ||
		strings.Contains(result, "Add HCLOUD_TOKEN using /env first") {
		t.Fatalf("malformed-credential result = %q, %v", result, err)
	}
}
