package chat

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"goaster/internal/migration"
)

const safeMigrationDiagnosticOutput = `{"creation_state":"not_created","code":"invalid_input","stage":"provider","http_status":422,"provider_code":"invalid_input","provider_message":"invalid input","provider_fields":[{"field":"server_type","messages":["is not available"]}]}`

func TestCodexProviderObservesMigrationResultBeforeContinuation(t *testing.T) {
	requests := 0
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		if requests == 1 {
			writeCodexFunctionCall(t, w, "migration-call", migrationToolName, `{"action":"create","server_type":"cx22","server_name":"goaster-migration-0123456789abcdef"}`)
			return
		}
		fmt.Fprint(w, `{"output_text":"continued"}`)
	}))
	defer backend.Close()

	provider := NewCodexProvider(CredentialStore{})
	provider.backendURL = backend.URL
	provider.httpClient = backend.Client()
	if err := provider.RegisterTool(Tool{
		Name:        migrationToolName,
		Description: "test migration tool",
		Parameters:  map[string]any{"type": "object"},
		Handler: func(context.Context, string) (string, error) {
			return safeMigrationDiagnosticOutput, nil
		},
	}); err != nil {
		t.Fatal(err)
	}
	observed := ""
	ctx := WithMigrationDiagnosticObserver(context.Background(), func(output string) error {
		observed = output
		return nil
	})
	reply, _, err := provider.requestReply(ctx, Credential{}, "test", nil, true)
	if err != nil || reply != safeMigrationDiagnosticOutput {
		t.Fatalf("model continuation = %q, error = %v", reply, err)
	}
	if requests != 1 || !strings.Contains(observed, `"provider_message":"invalid input"`) {
		t.Fatalf("migration observer output = %q, backend requests = %d", observed, requests)
	}
}

type migrationDiagnosticTestProvider struct {
	output         string
	outputs        []string
	response       string
	responseErr    error
	emit           bool
	beforeObserver func()
	calls          int
	histories      [][]ChatMessage
}

func (provider *migrationDiagnosticTestProvider) Reply(ctx context.Context, model, message string) (string, error) {
	return provider.ReplyWithHistory(ctx, model, []ChatMessage{{Role: "user", Content: message}})
}

func (provider *migrationDiagnosticTestProvider) ReplyWithHistory(ctx context.Context, _ string, history []ChatMessage) (string, error) {
	provider.calls++
	provider.histories = append(provider.histories, cloneChatMessages(history))
	if provider.emit || len(provider.outputs) > 0 {
		output := provider.output
		if len(provider.outputs) > 0 {
			output = provider.outputs[0]
			provider.outputs = provider.outputs[1:]
		} else {
			provider.emit = false
		}
		observer := migrationDiagnosticObserver(ctx)
		if observer == nil {
			return "", errors.New("migration diagnostic observer missing")
		}
		if provider.beforeObserver != nil {
			provider.beforeObserver()
		}
		if err := observer(output); err != nil {
			return "", err
		}
	}
	return provider.response, provider.responseErr
}

func TestMigrationResponseDecoderToolAndPersistedFollowupPreserveCreated(t *testing.T) {
	for _, test := range []struct {
		name      string
		status    int
		body      string
		wantState string
		wantID    int64
		wantLabel string
	}{
		{
			name:      "missing error metadata",
			status:    http.StatusInternalServerError,
			body:      `{"creation_state":"created","server_id":42,"server_label":"goaster-migration-0123456789abcdef","setup_url":"https://private.invalid/setup"}`,
			wantState: "created",
			wantID:    42,
			wantLabel: migrationToolTargetName,
		},
		{
			name:      "inconsistent error metadata",
			status:    http.StatusInternalServerError,
			body:      `{"creation_state":"created","server_id":42,"server_label":"goaster-migration-0123456789abcdef","error":{"code":"unavailable","stage":"provider","http_status":409}}`,
			wantState: "created",
			wantID:    42,
			wantLabel: migrationToolTargetName,
		},
		{
			name:      "target exists contradiction",
			status:    http.StatusConflict,
			body:      `{"creation_state":"created","server_id":42,"server_label":"goaster-migration-0123456789abcdef","error":{"code":"target_exists","stage":"provider","http_status":409}}`,
			wantState: "created",
			wantID:    42,
			wantLabel: migrationToolTargetName,
		},
		{
			name:      "provider lookup failure contradiction",
			status:    http.StatusBadGateway,
			body:      `{"creation_state":"created","server_id":42,"server_label":"goaster-migration-0123456789abcdef","error":{"code":"provider_lookup_failure","stage":"provider","http_status":502}}`,
			wantState: "created",
			wantID:    42,
			wantLabel: migrationToolTargetName,
		},
		{
			name:      "malformed error envelope",
			status:    http.StatusInternalServerError,
			body:      `{"creation_state":"created","server_id":42,"server_label":"goaster-migration-0123456789abcdef","error":"malformed"}`,
			wantState: "created",
			wantID:    42,
			wantLabel: migrationToolTargetName,
		},
		{
			name:      "malformed success response",
			status:    http.StatusCreated,
			body:      `{"creation_state":"created","server_id":42,"server_label":"goaster-migration-0123456789abcdef","error":"malformed"}`,
			wantState: "created",
			wantID:    42,
			wantLabel: migrationToolTargetName,
		},
		{
			name:      "unknown nested error field",
			status:    http.StatusInternalServerError,
			body:      `{"creation_state":"created","server_id":42,"server_label":"goaster-migration-0123456789abcdef","error":{"code":"unavailable","stage":"provider","http_status":500,"private_detail":"https://private.invalid/error"}}`,
			wantState: "created",
			wantID:    42,
			wantLabel: migrationToolTargetName,
		},
		{
			name:      "duplicate creation state",
			status:    http.StatusInternalServerError,
			body:      `{"creation_state":"created","creation_state":"not_created","server_id":42,"server_label":"goaster-migration-0123456789abcdef","error":"malformed"}`,
			wantState: "unknown",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			web := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(test.status)
				_, _ = fmt.Fprint(w, test.body)
			}))
			defer web.Close()
			options := migration.DefaultOptions()
			options.WebOrigin = web.URL
			options.AllowInsecureHTTP = true
			options.HTTPClient = web.Client()
			options.LoadHetznerToken = func() (string, error) { return "synthetic-token", nil }
			options.MigrationRecipient = func() (int64, error) { return 7, nil }
			deliveries := 0
			options.PrepareRecipientHandoffParts = func(int64) (func(context.Context, string, []byte) error, error) {
				return func(context.Context, string, []byte) error {
					deliveries++
					return nil
				}, nil
			}

			registrar := &migrationToolTestRegistrar{}
			if err := RegisterMigrationTool(registrar, MigrationToolOperations{
				Create: func(ctx context.Context, request migration.Request, owner int64) migration.OneShotResult {
					return migration.CreateOnce(ctx, request, options, owner)
				},
			}); err != nil {
				t.Fatal(err)
			}
			ctx := WithConversationID(WithConversationType(context.Background(), "private"), 7)
			ctx = context.WithValue(ctx, scheduledTurnKey{}, true)
			ctx = WithScheduledMigration(ctx, func(ctx context.Context, request migration.Request, _ string) migration.OneShotResult {
				return migration.CreateOnce(ctx, request, options, 7)
			})
			toolOutput, err := registrar.tool.Handler(ctx, `{"action":"create","server_type":"cx22","server_name":"goaster-migration-0123456789abcdef"}`)
			if err != nil {
				t.Fatal(err)
			}
			var decoded migration.OneShotResult
			if err := json.Unmarshal([]byte(toolOutput), &decoded); err != nil {
				t.Fatal(err)
			}
			if deliveries != 0 || decoded.CreationState != test.wantState || decoded.Code != "response_unusable" || decoded.ServerID != test.wantID || decoded.ServerLabel != test.wantLabel || decoded.Handoff != "" {
				t.Fatalf("tool result = %+v deliveries=%d", decoded, deliveries)
			}
			if strings.Contains(toolOutput, "private.invalid") || strings.Contains(toolOutput, "synthetic-token") || strings.Contains(toolOutput, "malformed") || strings.Contains(toolOutput, "unavailable") {
				t.Fatalf("tool output retained private data: %s", toolOutput)
			}

			path := filepath.Join(t.TempDir(), "session.json")
			provider := &migrationDiagnosticTestProvider{output: toolOutput, response: "saved", emit: true}
			session, err := NewPersistentSessionAt(Config{Model: "test"}, provider, path)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := session.Reply(context.Background(), "record result"); err != nil {
				t.Fatal(err)
			}
			loaded, err := NewSessionStoreAt(path).Load()
			if err != nil {
				t.Fatal(err)
			}
			if len(loaded.MigrationDiagnostics) != 1 || loaded.MigrationDiagnostics[0].CreationState != test.wantState || loaded.MigrationDiagnostics[0].Code != "response_unusable" {
				t.Fatalf("persisted diagnostic = %+v", loaded.MigrationDiagnostics)
			}
			saved, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(string(saved), "private.invalid") || strings.Contains(string(saved), "synthetic-token") || strings.Contains(string(saved), "server_label") {
				t.Fatalf("persisted state retained response data: %s", saved)
			}

			restartedProvider := &migrationDiagnosticTestProvider{response: "follow-up"}
			restarted, err := NewPersistentSessionAt(Config{Model: "test"}, restartedProvider, path)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := restarted.Reply(context.Background(), "what happened?"); err != nil {
				t.Fatal(err)
			}
			metadata := ""
			for _, message := range restartedProvider.histories[0] {
				if message.Role == "system" && strings.Contains(message.Content, "migration diagnostic metadata") {
					metadata = message.Content
				}
			}
			if !strings.Contains(metadata, `"creation_state":"`+test.wantState+`"`) || !strings.Contains(metadata, `"code":"response_unusable"`) || strings.Contains(metadata, "private.invalid") || strings.Contains(metadata, "malformed") || strings.Contains(metadata, "unavailable") {
				t.Fatalf("follow-up metadata = %q", metadata)
			}
		})
	}
}

func TestMigrationDiagnosticsPersistAsMetadataAcrossSuccessAndRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "session.json")
	firstProvider := &migrationDiagnosticTestProvider{output: safeMigrationDiagnosticOutput, response: "migration answer", emit: true}
	first, err := NewPersistentSessionAt(Config{Model: "test"}, firstProvider, path)
	if err != nil {
		t.Fatal(err)
	}
	if reply, err := first.Reply(context.Background(), "create"); err != nil || reply != "migration answer" {
		t.Fatalf("successful migration turn = %q, %v", reply, err)
	}
	loaded, err := NewSessionStoreAt(path).Load()
	if err != nil {
		t.Fatal(err)
	}
	if len(loaded.MigrationDiagnostics) != 1 || loaded.MigrationDiagnostics[0].ProviderMessage != "invalid input" || len(loaded.MigrationDiagnostics[0].ProviderFields) != 1 {
		t.Fatalf("saved migration diagnostics = %+v", loaded.MigrationDiagnostics)
	}
	saved, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(saved), "generic tool result") || strings.Contains(string(saved), "setup_url") || strings.Contains(string(saved), "secret-token") {
		t.Fatalf("saved state retained forbidden tool data: %s", saved)
	}

	restartedProvider := &migrationDiagnosticTestProvider{response: "follow-up answer"}
	restarted, err := NewPersistentSessionAt(Config{Model: "test"}, restartedProvider, path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := restarted.Reply(context.Background(), "what happened?"); err != nil {
		t.Fatal(err)
	}
	if restartedProvider.calls != 1 || len(restartedProvider.histories) != 1 {
		t.Fatalf("follow-up provider calls = %d histories = %d", restartedProvider.calls, len(restartedProvider.histories))
	}
	history := restartedProvider.histories[0]
	metadata := ""
	for _, message := range history {
		if message.Role == "system" && strings.Contains(message.Content, "migration diagnostic metadata") {
			metadata = message.Content
		}
	}
	if !strings.Contains(metadata, `"provider_message":"invalid input"`) || !strings.Contains(metadata, `"field":"server_type"`) || !strings.Contains(metadata, `"messages":["is not available"]`) {
		t.Fatalf("follow-up omitted safe migration metadata: %+v", history)
	}
	for _, message := range loaded.Messages {
		if message.Role == "system" {
			t.Fatalf("migration metadata became a synthetic persisted message: %+v", loaded.Messages)
		}
	}
}

func TestMigrationDiagnosticsPersistMissingOrInvalidCreationStateAsUnknown(t *testing.T) {
	for _, test := range []struct {
		name, output string
	}{
		{
			name:   "missing",
			output: `{"code":"invalid_input","stage":"provider","http_status":422,"provider_code":"invalid_input"}`,
		},
		{
			name:   "invalid",
			output: `{"creation_state":"maybe_created","code":"invalid_input","stage":"provider","http_status":422,"provider_code":"invalid_input"}`,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "session.json")
			provider := &migrationDiagnosticTestProvider{output: test.output, response: "saved", emit: true}
			session, err := NewPersistentSessionAt(Config{Model: "test"}, provider, path)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := session.Reply(context.Background(), "migration result"); err != nil {
				t.Fatal(err)
			}

			loaded, err := NewSessionStoreAt(path).Load()
			if err != nil {
				t.Fatal(err)
			}
			if len(loaded.MigrationDiagnostics) != 1 || loaded.MigrationDiagnostics[0].CreationState != "unknown" {
				t.Fatalf("persisted migration diagnostics = %+v, want one unknown outcome", loaded.MigrationDiagnostics)
			}

			restartedProvider := &migrationDiagnosticTestProvider{response: "follow-up"}
			restarted, err := NewPersistentSessionAt(Config{Model: "test"}, restartedProvider, path)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := restarted.Reply(context.Background(), "what happened?"); err != nil {
				t.Fatal(err)
			}
			if len(restartedProvider.histories) != 1 || !strings.Contains(restartedProvider.histories[0][0].Content, `"creation_state":"unknown"`) {
				t.Fatalf("restarted migration metadata = %+v, want unknown outcome", restartedProvider.histories)
			}
		})
	}
}

func TestMigrationDiagnosticsPersistAfterModelFailureAndCancellation(t *testing.T) {
	for _, test := range []struct {
		name string
		err  error
	}{
		{name: "model failure", err: errors.New("model failed after migration")},
		{name: "canceled", err: context.Canceled},
		{name: "timeout", err: context.DeadlineExceeded},
	} {
		t.Run(test.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "session.json")
			provider := &migrationDiagnosticTestProvider{output: safeMigrationDiagnosticOutput, responseErr: test.err, emit: true}
			session, err := NewPersistentSessionAt(Config{Model: "test"}, provider, path)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := session.Reply(context.Background(), "failed migration turn"); err == nil || !errors.Is(err, test.err) {
				t.Fatalf("failed migration turn = %v", err)
			}
			loaded, err := NewSessionStoreAt(path).Load()
			if err != nil {
				t.Fatal(err)
			}
			if len(loaded.MigrationDiagnostics) != 1 || loaded.MigrationDiagnostics[0].Code != "invalid_input" {
				t.Fatalf("failed-turn diagnostics = %+v", loaded.MigrationDiagnostics)
			}
			if len(loaded.Messages) != 2 || !containsMessage(loaded.Messages, "user", "failed migration turn") || !strings.Contains(loaded.Messages[1].Content, "Turn interrupted") {
				t.Fatalf("failed migration turn created conversation claims: %+v", loaded.Messages)
			}

			restartedProvider := &migrationDiagnosticTestProvider{response: "manual follow-up"}
			restarted, err := NewPersistentSessionAt(Config{Model: "test"}, restartedProvider, path)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := restarted.Reply(context.Background(), "follow up"); err != nil {
				t.Fatal(err)
			}
			if restartedProvider.calls != 1 {
				t.Fatalf("follow-up repeated the migration tool path: %d calls", restartedProvider.calls)
			}
			if len(restartedProvider.histories) != 1 || !strings.Contains(restartedProvider.histories[0][0].Content, `"creation_state":"not_created"`) {
				t.Fatalf("follow-up missing failed-turn outcome: %+v", restartedProvider.histories)
			}
		})
	}
}

func TestMigrationDiagnosticCheckpointPreservesSaveFailureCause(t *testing.T) {
	path := filepath.Join(t.TempDir(), "session.json")
	provider := &migrationDiagnosticTestProvider{output: safeMigrationDiagnosticOutput, response: "not reached", emit: true}
	session, err := NewPersistentSessionAt(Config{Model: "test"}, provider, path)
	if err != nil {
		t.Fatal(err)
	}
	saveCause := errors.New("diagnostic checkpoint sync failed")
	saves := 0
	session.store.syncDirectory = func(string) error {
		saves++
		if saves == 2 {
			return saveCause
		}
		return nil
	}
	if _, err := session.Reply(context.Background(), "migration"); err == nil || !errors.Is(err, saveCause) {
		t.Fatalf("checkpoint failure = %v", err)
	}
	if provider.calls != 1 || provider.emit {
		t.Fatalf("checkpoint failure continued or repeated provider work: calls=%d emit=%t", provider.calls, provider.emit)
	}
	if data, readErr := os.ReadFile(path); readErr == nil && strings.Contains(string(data), "setup_url") {
		t.Fatalf("checkpoint state retained private setup material: %s", data)
	}
}

func TestMigrationDiagnosticCheckpointFailureRetainsLiveCreatedOutcome(t *testing.T) {
	path := filepath.Join(t.TempDir(), "session.json")
	created := `{"creation_state":"created","code":"response_unusable","server_id":42,"server_label":"goaster-migration-0123456789abcdef"}`
	provider := &migrationDiagnosticTestProvider{
		outputs:  []string{safeMigrationDiagnosticOutput, created},
		response: "saved",
	}
	session, err := NewPersistentSessionAt(Config{Model: "test"}, provider, path)
	if err != nil {
		t.Fatal(err)
	}
	blocked := filepath.Join(t.TempDir(), "not-a-directory")
	if err := os.WriteFile(blocked, []byte("blocker"), 0600); err != nil {
		t.Fatal(err)
	}
	originalPath := session.store.Path
	provider.beforeObserver = func() {
		if provider.calls == 2 {
			session.store.Path = filepath.Join(blocked, "session.json")
		}
	}
	if _, err := session.Reply(context.Background(), "initial migration"); err != nil {
		t.Fatal(err)
	}
	if _, err := session.Reply(context.Background(), "created migration"); err == nil || !errors.Is(err, errConversationSaveFailed) {
		t.Fatalf("pre-replacement checkpoint failure = %v", err)
	}
	session.store.Path = originalPath
	if len(session.migrationDiagnostics) != 2 || session.migrationDiagnostics[1].CreationState != "created" || !session.migrationDiagnosticsPending {
		t.Fatalf("live pending diagnostics = %+v pending=%t", session.migrationDiagnostics, session.migrationDiagnosticsPending)
	}
	if _, err := session.store.Load(); err == nil || !errors.Is(err, errMigrationDiagnosticsPending) {
		t.Fatalf("same-store load while pending = %v", err)
	}
	if _, err := NewSessionWithStore(Config{Model: "test"}, &migrationDiagnosticTestProvider{}, session.store); err == nil || !errors.Is(err, errMigrationDiagnosticsPending) {
		t.Fatalf("second session construction while pending = %v", err)
	}
	old, err := NewSessionStoreAt(path).Load()
	if err != nil {
		t.Fatal(err)
	}
	if len(old.MigrationDiagnostics) != 1 || old.MigrationDiagnostics[0].CreationState != "not_created" {
		t.Fatalf("pre-replacement disk state = %+v", old.MigrationDiagnostics)
	}

	provider.response = "manual follow-up"
	if _, err := session.Reply(context.Background(), "manual follow-up"); err != nil {
		t.Fatal(err)
	}
	if len(provider.histories) != 3 {
		t.Fatalf("live follow-up calls = %d", len(provider.histories))
	}
	metadata := ""
	for _, message := range provider.histories[2] {
		if message.Role == "system" && strings.Contains(message.Content, "migration diagnostic metadata") {
			metadata = message.Content
		}
	}
	if !strings.Contains(metadata, `"creation_state":"created"`) || !strings.Contains(metadata, `"code":"response_unusable"`) {
		t.Fatalf("live follow-up omitted created outcome: %q", metadata)
	}
	loaded, err := NewSessionStoreAt(path).Load()
	if err != nil {
		t.Fatal(err)
	}
	if len(loaded.MigrationDiagnostics) != 2 || loaded.MigrationDiagnostics[1].CreationState != "created" || session.migrationDiagnosticsPending {
		t.Fatalf("recovered diagnostics = %+v pending=%t", loaded.MigrationDiagnostics, session.migrationDiagnosticsPending)
	}
	saved, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(saved), "server_label") || strings.Contains(string(saved), "setup_url") {
		t.Fatalf("recovered state retained response material: %s", saved)
	}
}

func pendingCreatedMigrationSession(t *testing.T) (*Session, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "session.json")
	created := `{"creation_state":"created","code":"response_unusable"}`
	provider := &migrationDiagnosticTestProvider{outputs: []string{safeMigrationDiagnosticOutput, created}, response: "saved"}
	session, err := NewPersistentSessionAt(Config{Model: "test"}, provider, path)
	if err != nil {
		t.Fatal(err)
	}
	blocked := filepath.Join(t.TempDir(), "not-a-directory")
	if err := os.WriteFile(blocked, []byte("blocker"), 0600); err != nil {
		t.Fatal(err)
	}
	originalPath := session.store.Path
	provider.beforeObserver = func() {
		if provider.calls == 2 {
			session.store.Path = filepath.Join(blocked, "session.json")
		}
	}
	if _, err := session.Reply(context.Background(), "initial migration"); err != nil {
		t.Fatal(err)
	}
	if _, err := session.Reply(context.Background(), "created migration"); err == nil || !errors.Is(err, errConversationSaveFailed) {
		t.Fatalf("pre-replacement checkpoint failure = %v", err)
	}
	session.store.Path = originalPath
	return session, path
}

func TestMigrationDiagnosticPendingStoreSavePreservesLatestEvidence(t *testing.T) {
	session, path := pendingCreatedMigrationSession(t)
	store := session.store
	if _, err := store.Load(); err == nil || !errors.Is(err, errMigrationDiagnosticsPending) {
		t.Fatalf("same-store load while pending = %v", err)
	}
	if _, err := store.saveStateWithVersionAndDiagnostics("", nil, session.storeVersion, true, nil, true); err == nil || !errors.Is(err, errMigrationDiagnosticsPending) {
		t.Fatalf("explicit diagnostic replacement while pending = %v", err)
	}
	before, err := NewSessionStoreAt(path).Load()
	if err != nil {
		t.Fatal(err)
	}
	if len(before.MigrationDiagnostics) != 1 || before.MigrationDiagnostics[0].CreationState != "not_created" {
		t.Fatalf("explicit replacement changed disk state = %+v", before.MigrationDiagnostics)
	}
	if err := store.Save([]ChatMessage{{Role: "user", Content: "manual evidence-preserving save"}}); err != nil {
		t.Fatalf("ordinary save with pending evidence = %v", err)
	}
	loaded, err := NewSessionStoreAt(path).Load()
	if err != nil {
		t.Fatal(err)
	}
	if len(loaded.MigrationDiagnostics) != 2 || loaded.MigrationDiagnostics[1].CreationState != "created" || len(loaded.Messages) != 1 || loaded.Messages[0].Content != "manual evidence-preserving save" {
		t.Fatalf("ordinary save lost pending evidence = %+v", loaded)
	}
	if store.migrationDiagnosticsPending {
		t.Fatal("successful ordinary save left pending evidence armed")
	}
}

func TestMigrationDiagnosticPendingStoreRejectsDiskVersionConflict(t *testing.T) {
	session, path := pendingCreatedMigrationSession(t)
	other := NewSessionStoreAt(path)
	if _, err := other.Load(); err != nil {
		t.Fatal(err)
	}
	if err := other.Save([]ChatMessage{{Role: "user", Content: "different disk version"}}); err != nil {
		t.Fatal(err)
	}
	if err := session.store.Save([]ChatMessage{{Role: "user", Content: "must not overwrite"}}); err == nil || !errors.Is(err, errConversationStateChanged) {
		t.Fatalf("pending store save after disk conflict = %v", err)
	}
	if !session.store.migrationDiagnosticsPending {
		t.Fatal("disk conflict cleared pending evidence")
	}
	if _, err := session.store.Load(); err == nil || !errors.Is(err, errMigrationDiagnosticsPending) {
		t.Fatalf("pending store reload after disk conflict = %v", err)
	}
	loaded, err := NewSessionStoreAt(path).Load()
	if err != nil {
		t.Fatal(err)
	}
	if len(loaded.MigrationDiagnostics) != 1 || loaded.MigrationDiagnostics[0].CreationState != "not_created" || len(loaded.Messages) != 1 || loaded.Messages[0].Content != "different disk version" {
		t.Fatalf("disk conflict was overwritten = %+v", loaded)
	}
}

func TestMigrationDiagnosticPendingEvidenceRefusesConcurrentReload(t *testing.T) {
	path := filepath.Join(t.TempDir(), "session.json")
	created := `{"creation_state":"created","code":"response_unusable","server_id":42,"server_label":"goaster-migration-0123456789abcdef"}`
	provider := &migrationDiagnosticTestProvider{outputs: []string{safeMigrationDiagnosticOutput, created}, response: "saved"}
	session, err := NewPersistentSessionAt(Config{Model: "test"}, provider, path)
	if err != nil {
		t.Fatal(err)
	}
	blocked := filepath.Join(t.TempDir(), "not-a-directory")
	if err := os.WriteFile(blocked, []byte("blocker"), 0600); err != nil {
		t.Fatal(err)
	}
	originalPath := session.store.Path
	provider.beforeObserver = func() {
		if provider.calls == 2 {
			session.store.Path = filepath.Join(blocked, "session.json")
		}
	}
	if _, err := session.Reply(context.Background(), "initial migration"); err != nil {
		t.Fatal(err)
	}
	if _, err := session.Reply(context.Background(), "created migration"); err == nil {
		t.Fatal("checkpoint unexpectedly succeeded")
	}
	session.store.Path = originalPath

	concurrent := NewSessionStoreAt(path)
	if _, err := concurrent.Load(); err != nil {
		t.Fatal(err)
	}
	if err := concurrent.Save([]ChatMessage{{Role: "user", Content: "concurrent state"}}); err != nil {
		t.Fatal(err)
	}
	provider.response = "must not run"
	calls := provider.calls
	if _, err := session.Reply(context.Background(), "manual follow-up"); err == nil || !errors.Is(err, errConversationStateChanged) {
		t.Fatalf("concurrent pending reload = %v", err)
	}
	if provider.calls != calls || len(session.migrationDiagnostics) != 2 || session.migrationDiagnostics[1].CreationState != "created" {
		t.Fatalf("concurrent reload erased live evidence: calls=%d diagnostics=%+v", provider.calls, session.migrationDiagnostics)
	}
}

func TestMigrationDiagnosticParsingRejectsHostileAndUnknownResults(t *testing.T) {
	for _, output := range []string{
		`{"creation_state":"not_created","code":"invalid_input","provider_message":"private body"}`,
		`{"creation_state":"not_created","code":"invalid_input","provider_fields":[{"field":"server_type","messages":["private detail"]}]}`,
		`{"creation_state":"not_created","code":"invalid_input","setup_url":"https://private.invalid/setup"}`,
		`{"creation_state":"not_created","code":"invalid_input"} trailing`,
	} {
		diagnostic, ok := migrationDiagnosticFromToolOutput(output)
		if !ok {
			continue
		}
		if diagnostic.ProviderMessage != "" || len(diagnostic.ProviderFields) != 0 {
			t.Fatalf("hostile migration result retained details: %+v", diagnostic)
		}
	}
	if _, ok := migrationDiagnosticFromToolOutput(`{"creation_state":"unknown","code":"provider_timeout"}`); !ok {
		t.Fatal("safe uncertainty result was rejected")
	}
}

func maximumMigrationDiagnostic(state, code string) MigrationDiagnostic {
	messages := []string{"required", "is required", "invalid", "is invalid", "unavailable", "is unavailable", "not available", "not found"}
	fields := make([]migration.ProviderField, 0, 8)
	for _, field := range []string{"server_type", "server_name", "name", "location", "locations", "image", "ssh_keys", "login_public_key"} {
		fields = append(fields, migration.ProviderField{Field: field, Messages: append([]string(nil), messages...)})
	}
	return MigrationDiagnostic{
		CreationState:   state,
		Code:            code,
		Stage:           "provider",
		HTTPStatus:      422,
		ProviderCode:    "invalid_input",
		ProviderMessage: "the requested server type is not available in the requested locations",
		ProviderFields:  fields,
		Handoff:         "failed",
	}
}

func migrationDiagnosticOutput(t *testing.T, value MigrationDiagnostic) string {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func TestMigrationDiagnosticsNoticeRetainsNewestOutcomeWithinBudget(t *testing.T) {
	values := make([]MigrationDiagnostic, 0, maxMigrationDiagnostics)
	for index := 0; index < maxMigrationDiagnostics-1; index++ {
		values = append(values, maximumMigrationDiagnostic("not_created", "invalid_input"))
	}
	values = append(values, maximumMigrationDiagnostic("unknown", "response_unavailable"))
	notice := migrationDiagnosticsMessage(values)
	if notice == "" || len(notice) > maxMigrationDiagnosticBytes {
		t.Fatalf("bounded migration notice length = %d, notice empty=%t", len(notice), notice == "")
	}
	var retained []MigrationDiagnostic
	if err := json.Unmarshal([]byte(strings.TrimPrefix(notice, migrationDiagnosticNoticePrefix)), &retained); err != nil {
		t.Fatalf("decode bounded migration notice: %v", err)
	}
	if len(retained) == 0 || retained[len(retained)-1].CreationState != "unknown" || retained[len(retained)-1].Code != "response_unavailable" {
		t.Fatalf("bounded migration notice lost newest outcome: %+v", retained)
	}
}

func TestMigrationDiagnosticsWorstCaseDetailsSurviveRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "session.json")
	outputs := make([]string, 0, maxMigrationDiagnostics)
	for index := 0; index < maxMigrationDiagnostics; index++ {
		state, code := "not_created", "invalid_input"
		if index == maxMigrationDiagnostics-1 {
			state, code = "unknown", "response_unavailable"
		}
		outputs = append(outputs, migrationDiagnosticOutput(t, maximumMigrationDiagnostic(state, code)))
	}
	firstProvider := &migrationDiagnosticTestProvider{outputs: outputs, response: "saved"}
	first, err := NewPersistentSessionAt(Config{Model: "test"}, firstProvider, path)
	if err != nil {
		t.Fatal(err)
	}
	for index := range outputs {
		if _, err := first.Reply(context.Background(), fmt.Sprintf("migration attempt %d", index)); err != nil {
			t.Fatalf("migration attempt %d: %v", index, err)
		}
	}

	restartedProvider := &migrationDiagnosticTestProvider{response: "follow-up"}
	restarted, err := NewPersistentSessionAt(Config{Model: "test"}, restartedProvider, path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := restarted.Reply(context.Background(), "what is the latest migration result?"); err != nil {
		t.Fatal(err)
	}
	metadata := ""
	for _, message := range restartedProvider.histories[0] {
		if message.Role == "system" && strings.Contains(message.Content, "migration diagnostic metadata") {
			metadata = message.Content
		}
	}
	if metadata == "" || len(metadata) > maxMigrationDiagnosticBytes {
		t.Fatalf("restart migration metadata length = %d, metadata empty=%t", len(metadata), metadata == "")
	}
	var retained []MigrationDiagnostic
	if err := json.Unmarshal([]byte(strings.TrimPrefix(metadata, migrationDiagnosticNoticePrefix)), &retained); err != nil {
		t.Fatalf("decode restart migration metadata: %v", err)
	}
	if len(retained) == 0 || retained[len(retained)-1].CreationState != "unknown" || retained[len(retained)-1].Code != "response_unavailable" {
		t.Fatalf("restart migration metadata lost newest outcome: %+v", retained)
	}
}

func TestMigrationDiagnosticsPreserveRepeatedOutcomesAcrossFailedRestarts(t *testing.T) {
	path := filepath.Join(t.TempDir(), "session.json")
	unknown := `{"creation_state":"unknown","code":"response_unavailable"}`
	for index, test := range []struct {
		name       string
		output     string
		failure    error
		wantStates []string
	}{
		{name: "unknown", output: unknown, failure: errors.New("first model turn failed"), wantStates: []string{"unknown"}},
		{name: "not created", output: safeMigrationDiagnosticOutput, failure: errors.New("second model turn failed"), wantStates: []string{"unknown", "not_created"}},
		{name: "unknown again", output: unknown, failure: errors.New("third model turn failed"), wantStates: []string{"unknown", "not_created", "unknown"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			provider := &migrationDiagnosticTestProvider{output: test.output, responseErr: test.failure, emit: true}
			session, err := NewPersistentSessionAt(Config{Model: "test"}, provider, path)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := session.Reply(context.Background(), test.name); err == nil || !errors.Is(err, test.failure) {
				t.Fatalf("failed migration turn = %v", err)
			}
			loaded, err := NewSessionStoreAt(path).Load()
			if err != nil {
				t.Fatal(err)
			}
			if len(loaded.MigrationDiagnostics) != len(test.wantStates) {
				t.Fatalf("saved diagnostic count = %d, want %d: %+v", len(loaded.MigrationDiagnostics), len(test.wantStates), loaded.MigrationDiagnostics)
			}
			for diagnosticIndex, wantState := range test.wantStates {
				if loaded.MigrationDiagnostics[diagnosticIndex].CreationState != wantState {
					t.Fatalf("diagnostic states = %+v, want %v", loaded.MigrationDiagnostics, test.wantStates)
				}
			}
			if index == 2 {
				followUpProvider := &migrationDiagnosticTestProvider{response: "manual review"}
				followUp, err := NewPersistentSessionAt(Config{Model: "test"}, followUpProvider, path)
				if err != nil {
					t.Fatal(err)
				}
				if _, err := followUp.Reply(context.Background(), "follow up after restart"); err != nil {
					t.Fatal(err)
				}
				metadata := ""
				for _, message := range followUpProvider.histories[0] {
					if message.Role == "system" && strings.Contains(message.Content, "migration diagnostic metadata") {
						metadata = message.Content
					}
				}
				if metadata == "" || len(metadata) > maxMigrationDiagnosticBytes {
					t.Fatalf("follow-up metadata length = %d, metadata empty=%t", len(metadata), metadata == "")
				}
				var retained []MigrationDiagnostic
				if err := json.Unmarshal([]byte(strings.TrimPrefix(metadata, migrationDiagnosticNoticePrefix)), &retained); err != nil {
					t.Fatalf("decode follow-up migration metadata: %v", err)
				}
				if len(retained) != len(test.wantStates) {
					t.Fatalf("follow-up diagnostic count = %d, want %d: %+v", len(retained), len(test.wantStates), retained)
				}
				for diagnosticIndex, wantState := range test.wantStates {
					if retained[diagnosticIndex].CreationState != wantState {
						t.Fatalf("follow-up diagnostic states = %+v, want %v", retained, test.wantStates)
					}
				}
			}
		})
	}
}

func TestCodexScheduledMigrationDirectCompletionSaveFailures(t *testing.T) {
	for _, tc := range []struct {
		name          string
		failSave      int
		wantCompleted int
	}{
		{"diagnostic_checkpoint", 1, 0},
		{"final_session_storage", 2, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			requests, creates, completed := 0, 0, 0
			backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests++
				writeCodexFunctionCall(t, w, "migration-call", migrationToolName, `{"action":"create","server_type":"cx22","server_name":"goaster-migration-0123456789abcdef"}`)
			}))
			defer backend.Close()
			provider := runtimeTestProvider(t, backend.Client(), backend.URL)
			if err := provider.RegisterTool(Tool{Name: migrationToolName, Description: "test", Parameters: map[string]any{"type": "object"}, Handler: func(context.Context, string) (string, error) {
				creates++
				return `{"creation_state":"created","handoff":"delivered"}`, nil
			}}); err != nil {
				t.Fatal(err)
			}
			session, err := NewPersistentSessionAt(Config{Model: "test"}, provider, filepath.Join(t.TempDir(), "session.json"))
			if err != nil {
				t.Fatal(err)
			}
			cause := errors.New("injected session directory sync failure")
			saves := 0
			session.store.syncDirectory = func(string) error {
				saves++
				if saves == tc.failSave {
					return cause
				}
				return nil
			}
			ctx := WithScheduledMigrationCompleted(context.Background(), func() bool { completed++; return true })
			_, err = session.ReplyScheduled(ctx, "migration")
			if !errors.Is(err, cause) || requests != 1 || creates != 1 || completed != tc.wantCompleted {
				t.Fatalf("error=%v requests=%d creates=%d completed=%d saves=%d", err, requests, creates, completed, saves)
			}
		})
	}
}
