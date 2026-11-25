// goaster-agent exposes GoAster chat locally over loopback or a Unix socket.
package main

import (
	"context"
	cryptorand "crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"
	"unicode"

	"goaster/internal/artefacts"
	"goaster/internal/attachment"
	"goaster/internal/chat"
	"goaster/internal/command"
	"goaster/internal/controlsecret"
	"goaster/internal/controltransport"
	"goaster/internal/diagnostics"
	"goaster/internal/localenv"
	"goaster/internal/migration"
	"goaster/internal/scheduler"
	"goaster/internal/telegram"
)

var restartWorkMu sync.RWMutex
var restartGuardMu sync.Mutex
var restartGuardID string
var restartGuardHeld bool
var restartGuardCommitted bool
var restartGuardDuration = 50 * time.Second

func expireRestartGuard(id string) {
	restartGuardMu.Lock()
	defer restartGuardMu.Unlock()
	if restartGuardHeld && restartGuardID == id && !restartGuardCommitted {
		restartGuardHeld = false
		restartWorkMu.Unlock()
	}
}

func releaseRestartGuard(id string) {
	restartGuardMu.Lock()
	defer restartGuardMu.Unlock()
	if restartGuardHeld && restartGuardID == id {
		restartGuardHeld = false
		restartWorkMu.Unlock()
	}
}

const maxChatEnvelopeBytes int64 = 1 << 20

const (
	environmentCommand       = "/env"
	environmentAcceptKeyword = "ACCEPT"
	environmentAcceptCommand = environmentCommand + " " + environmentAcceptKeyword
	environmentFilename      = "goaster.env"
	environmentProposalTTL   = 10 * time.Minute
)

const controlAuthorizationHeader = "X-GoAster-GitHub-Auth-Control"

const (
	agentRequestReferenceHeader    = "X-GoAster-Request-Reference"
	agentErrorClassificationHeader = "X-GoAster-Error-Classification"
)

type countingReader struct {
	io.Reader
	bytesRead int64
}

type environmentProposalKey struct {
	userID int64
	chatID int64
}

type environmentProposal struct {
	baseDigest string
	values     map[string]string
	expiresAt  time.Time
}

type environmentRequestKind uint8

const (
	environmentRequestNone environmentRequestKind = iota
	environmentPreviewRequest
	environmentAcceptRequest
	environmentMalformedPreviewRequest
	environmentMalformedAcceptRequest
)

func (r *countingReader) Read(p []byte) (int, error) {
	n, err := r.Reader.Read(p)
	r.bytesRead += int64(n)
	return n, err
}

func main() {
	store, err := agentCredentialStore(os.UserHomeDir)
	if err != nil {
		if len(os.Args) > 1 && os.Args[1] == "test-model" {
			err = modelTestStartupFailure(os.Stdout, err)
			logAgentError(err)
		} else {
			diagnostics.Error("agent.startup", err)
		}
		fmt.Fprintln(os.Stderr, "agent: cannot initialize credential store")
		os.Exit(1)
	}
	ctx := context.Background()
	if len(os.Args) > 1 && os.Args[1] == "test-model" {
		var stop context.CancelFunc
		ctx, stop = signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
		defer stop()
	}
	if err := run(ctx, os.Args[1:], os.Stdout, chat.NewOpenAIAuthenticator(store), serve); err != nil {
		logAgentError(err)
		fmt.Fprintln(os.Stderr, "agent: operation failed")
		os.Exit(1)
	}
}

func modelTestStartupFailure(output io.Writer, cause error) error {
	return writeModelTestResult(output, "", modelTestError{"configuration", "", cause})
}

func logAgentError(err error) {
	var testErr modelTestError
	if errors.As(err, &testErr) {
		fields := []diagnostics.Field{{Key: "model_test_stage", Value: testErr.stage}, {Key: "configured_model", Value: testErr.model}}
		fields = append(fields, chat.ProviderFailureDiagnosticFields(err)...)
		if testErr.stage == "provider" {
			// Preserve individual attempt records and add one owner summary.
			diagnostics.Error("agent.model_test", errors.Join(err, errors.New("model test provider failed")), fields...)
		} else {
			diagnostics.Error("agent.model_test", err, fields...)
		}
	} else {
		diagnostics.Error("agent.main", err)
	}
}

// The remote agent must never fall back to the terminal's auth.json, even if
// both happen to run under the same home directory.
func agentCredentialStore(homeDir func() (string, error)) (chat.CredentialStore, error) {
	home, err := homeDir()
	if err != nil {
		return chat.CredentialStore{}, err
	}
	return chat.NewCredentialStore(filepath.Join(home, ".goaster", "agent-auth.json"))
}

func run(ctx context.Context, args []string, output io.Writer, auth *chat.OpenAIAuthenticator, start func(chat.CredentialStore) error) error {
	if len(args) > 0 && args[0] == "test-model" {
		if len(args) != 2 {
			return writeModelTestResult(output, "", modelTestError{"configuration", "", errors.New("usage: goaster-agent test-model EXACT_MODEL_ID")})
		}
		if auth == nil {
			return writeModelTestResult(output, "", modelTestError{"configuration", "", errors.New("agent authentication is unavailable")})
		}
		return runModelTest(ctx, args[1], output, auth.Store)
	}
	if len(args) == 1 && args[0] == "login" {
		return auth.Login(ctx, output)
	}
	if len(args) > 0 && (args[0] == "prompt" || args[0] == "test-prompt") {
		if len(args) != 2 || strings.TrimSpace(args[1]) == "" {
			return errors.New(`usage: goaster-agent prompt|test-prompt "MESSAGE"`)
		}
		if auth == nil {
			return errors.New("agent authentication is unavailable")
		}
		if args[0] == "test-prompt" {
			return runTestPrompt(ctx, args[1], output, auth.Store)
		}
		return runPrompt(ctx, args[1], output, auth.Store)
	}
	if len(args) == 1 && (args[0] == "live" || args[0] == "health") {
		return probeAgent(ctx, args[0], output)
	}
	if len(args) != 0 {
		return fmt.Errorf("invalid invocation; use goaster-agent, login, prompt, test-prompt, test-model, live, or health")
	}
	return start(auth.Store)
}

func newAgentRequestReference() string {
	var random [12]byte
	if _, err := cryptorand.Read(random[:]); err != nil {
		return "chat-unknown"
	}
	return "chat-" + hex.EncodeToString(random[:])
}

func runTestPrompt(ctx context.Context, message string, output io.Writer, store chat.CredentialStore) error {
	reply, err := chat.ReplyWithoutTools(ctx, chat.NewCodexProvider(store), chat.DefaultModel, message)
	if err != nil {
		return err
	}
	if _, err := fmt.Fprintln(output, reply); err != nil {
		return fmt.Errorf("write prompt reply: %w", err)
	}
	return nil
}

const modelTestPrompt = "Reply with exactly GOASTER_MODEL_TEST_OK and nothing else."
const modelTestSentinel = "GOASTER_MODEL_TEST_OK"

// Keep the cause available to diagnostics without exposing provider content to callers.
type modelTestError struct {
	stage string
	model string
	cause error
}

func (e modelTestError) Error() string { return "model test " + e.stage + " failed" }
func (e modelTestError) Unwrap() error { return e.cause }

func runModelTest(ctx context.Context, id string, output io.Writer, store chat.CredentialStore) error {
	registry, err := chat.NewModelRegistry("")
	if err != nil {
		return writeModelTestResult(output, "", modelTestError{"configuration", "", err})
	}
	return testConfiguredModel(ctx, id, output, registry, func(ctx context.Context, model string) (string, error) {
		return chat.ReplyWithoutTools(chat.WithProviderFailureBoundary(ctx), chat.NewCodexProvider(store), model, modelTestPrompt)
	})
}

func writeModelTestResult(output io.Writer, model string, failure error) error {
	status, stage := "ok", ""
	if failure != nil {
		status = "error"
		var testErr modelTestError
		if errors.As(failure, &testErr) {
			stage = testErr.stage
		}
	}
	record, err := json.Marshal(struct {
		Model  string `json:"model,omitempty"`
		Status string `json:"status"`
		Stage  string `json:"stage,omitempty"`
	}{model, status, stage})
	if err == nil {
		record = append(record, '\n')
		var n int
		n, err = output.Write(record)
		if err == nil && n != len(record) {
			err = io.ErrShortWrite
		}
	}
	if err != nil {
		return modelTestError{"output", model, errors.Join(failure, err)}
	}
	return failure
}

func testConfiguredModel(ctx context.Context, id string, output io.Writer, registry chat.ModelRegistry, reply func(context.Context, string) (string, error)) error {
	model, err := registry.ExactConfiguredID(id)
	if err != nil {
		return writeModelTestResult(output, "", modelTestError{"configuration", "", err})
	}
	ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	result, err := reply(ctx, model)
	if err == nil {
		err = ctx.Err()
	}
	if err != nil {
		return writeModelTestResult(output, model, modelTestError{"provider", model, err})
	}
	if result != modelTestSentinel {
		return writeModelTestResult(output, model, modelTestError{"response_mismatch", model, errors.New("response did not match sentinel")})
	}
	return writeModelTestResult(output, model, nil)
}

type agentPromptSetup struct {
	memory   *chat.MemoryStore
	registry chat.ModelRegistry
}

func newAgentPromptSetup(ctx context.Context) (agentPromptSetup, error) {
	memory, err := chat.NewMemoryStoreWithError("")
	if err != nil {
		return agentPromptSetup{}, fmt.Errorf("initialize Markdown memory store: %w", err)
	}
	if err := artefacts.InstallStarters(ctx); err != nil {
		return agentPromptSetup{}, fmt.Errorf("install starter skills: %w", err)
	}
	registry, err := chat.NewModelRegistry("")
	if err != nil {
		return agentPromptSetup{}, fmt.Errorf("initialize model registry: %w", err)
	}
	return agentPromptSetup{memory: memory, registry: registry}, nil
}

// registerAgentScheduledTools adds runtime capabilities to the same provider used by all turns.
func registerAgentScheduledTools(provider chat.ToolRegistrar, jobs cronSchedulerRuntime, create scheduledMigrationCreate, authorize ...func(int64) error) error {
	if err := chat.RegisterCronTool(provider, jobs); err != nil {
		return fmt.Errorf("register cron tool: %w", err)
	}
	if err := chat.RegisterCronSelfDisableTool(provider, jobs); err != nil {
		return fmt.Errorf("register cron self-disable tool: %w", err)
	}
	if err := chat.RegisterMigrationTool(provider, chat.MigrationToolOperations{Create: create, Schedule: func(request migration.Request, owner int64) error {
		scheduler, ok := jobs.(interface {
			CreateOnce(migration.Request, int64) (scheduler.Job, error)
		})
		if !ok {
			return errors.New("migration scheduler unavailable")
		}
		if len(authorize) > 0 {
			if err := authorize[0](owner); err != nil {
				return err
			}
		} else if err := authorizeMigrationOwner(owner); err != nil {
			return err
		}
		_, err := scheduler.CreateOnce(request, owner)
		return err
	}}); err != nil {
		return fmt.Errorf("register migration tool: %w", err)
	}
	return nil
}

func authorizeMigrationOwner(owner int64) error {
	current, err := telegram.MigrationRecipient()
	if err != nil || current != owner {
		return errors.Join(errors.New("paired recipient unavailable or changed"), err)
	}
	if _, err := telegram.PrepareRecipientMigrationHandoffParts(owner); err != nil {
		return fmt.Errorf("prepare migration handoff: %w", err)
	}
	return nil
}

func newAgentSession(provider chat.Provider, memory *chat.MemoryStore, registry chat.ModelRegistry, chatID int64) *chat.Session {
	return chat.NewSessionWithMemory(chat.Config{Registry: registry, ChatID: chatID}, provider, memory)
}

func newAgentProvider(store chat.CredentialStore) (*chat.CodexProvider, command.BackgroundCommandManager, error) {
	provider := chat.NewCodexProvider(store)
	backgroundManager, err := command.NewBackgroundCommandManager()
	if err != nil {
		return nil, command.BackgroundCommandManager{}, fmt.Errorf("initialize background command manager: %w", err)
	}
	if err := chat.RegisterBackgroundCommandTool(provider, backgroundManager); err != nil {
		closeErr := backgroundManager.Close()
		return nil, command.BackgroundCommandManager{}, errors.Join(fmt.Errorf("register background command tool: %w", err), closeErr)
	}
	if err := chat.RegisterSendFileTool(provider, telegram.SendFile); err != nil {
		closeErr := backgroundManager.Close()
		return nil, command.BackgroundCommandManager{}, errors.Join(fmt.Errorf("register send_file tool: %w", err), closeErr)
	}
	return provider, backgroundManager, nil
}

func runPrompt(ctx context.Context, message string, output io.Writer, store chat.CredentialStore) (promptErr error) {
	provider, backgroundManager, err := newAgentProvider(store)
	if err != nil {
		return err
	}
	defer func() { promptErr = errors.Join(promptErr, backgroundManager.Close()) }()
	return runAgentPrompt(ctx, message, output, provider)
}

func runAgentPrompt(ctx context.Context, message string, output io.Writer, provider chat.Provider) error {
	setup, err := newAgentPromptSetup(ctx)
	if err != nil {
		return err
	}
	session := newAgentSession(provider, setup.memory, setup.registry, 0)
	reply, err := session.Reply(ctx, message)
	if err != nil {
		return err
	}
	if _, err := fmt.Fprintln(output, reply); err != nil {
		return fmt.Errorf("write prompt reply: %w", err)
	}
	return nil
}

func probeAgent(ctx context.Context, probe string, output io.Writer) error {
	transport, err := controltransport.ClientTransport()
	if err != nil {
		return err
	}
	endpoint := "/livez"
	if probe == "health" {
		endpoint = "/healthz"
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://"+controltransport.Address+endpoint, nil)
	if err != nil {
		return fmt.Errorf("build agent probe request: %w", err)
	}
	client := http.Client{Transport: transport, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	response, err := client.Do(request)
	if err != nil {
		return fmt.Errorf("agent probe transport: %w", err)
	}
	if response == nil || response.Body == nil {
		return fmt.Errorf("agent probe returned no response body")
	}
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, 33))
	if err != nil {
		return fmt.Errorf("read agent probe response: %w", err)
	}
	if response.StatusCode != http.StatusOK || string(body) != "ok\n" {
		return fmt.Errorf("agent probe returned status %s or an invalid response", response.Status)
	}
	_, err = io.WriteString(output, "ok\n")
	return err
}

func pairedMigrationOptions() migration.Options {
	options := migration.DefaultOptions()
	options.MigrationRecipient = telegram.MigrationRecipient
	options.PrepareRecipientHandoffParts = telegram.PrepareRecipientMigrationHandoffParts
	options.ReportOutcome = telegram.ReportMigrationOutcome
	return options
}

func serve(store chat.CredentialStore) (serveErr error) {
	controlSecret, err := controlsecret.Load()
	if err != nil {
		return err
	}
	root, rootErr := attachment.DefaultRetainedRoot()
	if rootErr == nil {
		rootErr = attachment.SweepRetainedExpired(root, attachment.RetainedScope{})
	}
	if rootErr != nil {
		diagnostics.Error("agent.startup.retained_sweep", rootErr)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	setup, err := newAgentPromptSetup(ctx)
	if err != nil {
		return err
	}
	provider, backgroundManager, err := newAgentProvider(store)
	if err != nil {
		return err
	}
	defer func() { serveErr = errors.Join(serveErr, backgroundManager.Close()) }()
	sessionFor := newAgentSessionFactory(func(chatID int64) agentSession {
		return newAgentSession(provider, setup.memory, setup.registry, chatID)
	})
	migrationOptions := pairedMigrationOptions()
	var deliveryQueue cronDeliveryQueue
	cronJobs, err := newCronSchedulerWithMigration(sessionFor, func(job scheduler.Job, reply string) error {
		if deliveryQueue.Enqueue == nil {
			return errors.New("generic cron delivery queue is unavailable")
		}
		return deliveryQueue.Enqueue(job, reply)
	}, func(ctx context.Context, request migration.Request, owner int64) migration.OneShotResult {
		return migration.CreateOnce(ctx, request, migrationOptions, owner)
	})
	if err != nil {
		return err
	}
	deliveryQueue, err = newCronDeliveryQueue(filepath.Join(filepath.Dir(cronJobs.Path()), "deliveries.json"))
	if err != nil {
		return fmt.Errorf("initialize generic cron delivery queue: %w", err)
	}
	if err := registerAgentScheduledTools(provider, cronJobs, func(ctx context.Context, request migration.Request, owner int64) migration.OneShotResult {
		return migration.CreateOnce(ctx, request, migrationOptions, owner)
	}); err != nil {
		return err
	}
	if err := cronJobs.Start(ctx); err != nil {
		return fmt.Errorf("start generic cron scheduler: %w", err)
	}
	defer cronJobs.Stop()

	listener, err := controltransport.Listen()
	if err != nil {
		return fmt.Errorf("could not start local agent listener: %w", err)
	}
	defer func() {
		if err := listener.Close(); err != nil && !errors.Is(err, net.ErrClosed) {
			serveErr = errors.Join(serveErr, err)
		}
	}()
	server := http.Server{Handler: handlerWithMemoryAndRegistryWithSecretAndRuntimeAndLogin(provider.CheckAuth, setup.registry, controlSecret, provider.LoginInstructions, sessionFor, deliveryQueue, backgroundManager.List), ReadHeaderTimeout: 10 * time.Second}
	stopped := make(chan struct{})
	go func() {
		defer close(stopped)
		<-ctx.Done()
		if err := server.Close(); err != nil && !normalServerStop(err) {
			diagnostics.Error("agent.server.close", err)
		}
	}()
	err = server.Serve(listener)
	stop()
	<-stopped // Wait for Close to finish unlinking the Unix socket.
	if err != nil && !(ctx.Err() != nil && normalServerStop(err)) {
		return fmt.Errorf("server stopped: %w", err)
	}
	return nil
}

func normalServerStop(err error) bool {
	if err == nil {
		return true
	}
	if joined, ok := err.(interface{ Unwrap() []error }); ok {
		causes := joined.Unwrap()
		if len(causes) == 0 {
			return false
		}
		for _, cause := range causes {
			if !normalServerStop(cause) {
				return false
			}
		}
		return true
	}
	if cause := errors.Unwrap(err); cause != nil {
		return normalServerStop(cause)
	}
	return errors.Is(err, http.ErrServerClosed) || errors.Is(err, net.ErrClosed) || errors.Is(err, io.EOF) || errors.Is(err, context.Canceled)
}

func handlerWithMemoryAndRegistryWithSecret(provider chat.Provider, memory *chat.MemoryStore, checkAuth func(context.Context) error, registry chat.ModelRegistry, controlSecret string) http.Handler {
	return handlerWithMemoryAndRegistryWithSecretAndRuntime(checkAuth, registry, controlSecret, newAgentSessionFactory(func(chatID int64) agentSession {
		return newAgentSession(provider, memory, registry, chatID)
	}), cronDeliveryQueue{})
}

func handlerWithMemoryAndRegistryWithSecretAndRuntime(checkAuth func(context.Context) error, registry chat.ModelRegistry, controlSecret string, sessionFor func(int64, int64) agentSession, deliveryQueue cronDeliveryQueue, listBackground ...func(context.Context) ([]command.BackgroundSession, error)) http.Handler {
	return handlerWithMemoryAndRegistryWithSecretAndRuntimeAndLogin(checkAuth, registry, controlSecret, nil, sessionFor, deliveryQueue, listBackground...)
}

func handlerWithMemoryAndRegistryWithSecretAndRuntimeAndLogin(checkAuth func(context.Context) error, registry chat.ModelRegistry, controlSecret string, login func(context.Context, func(string, string) error) error, sessionFor func(int64, int64) agentSession, deliveryQueue cronDeliveryQueue, listBackground ...func(context.Context) ([]command.BackgroundSession, error)) http.Handler {
	if sessionFor == nil {
		sessionFor = func(int64, int64) agentSession { return nil }
	}
	var environmentSerialMu sync.Mutex
	environmentProposals := make(map[environmentProposalKey]environmentProposal)
	mux := http.NewServeMux()
	mux.HandleFunc("/livez", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if _, err := w.Write([]byte("ok\n")); err != nil {
			diagnostics.Error("agent.livez.write", err)
		}
	})
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		var authErr error
		if checkAuth == nil {
			authErr = errors.New("authentication checker is unavailable")
		} else {
			authErr = checkAuth(r.Context())
		}
		if authErr != nil {
			diagnostics.Error("agent.healthz.authentication", authErr)
			http.Error(w, "remote agent authentication unavailable", http.StatusServiceUnavailable)
			return
		}
		if _, err := w.Write([]byte("ok\n")); err != nil {
			diagnostics.Error("agent.healthz.write", err)
		}
	})
	mux.HandleFunc(controltransport.ScheduledDeliveriesPath, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if !validControlRequest(r, controlSecret) {
			http.Error(w, "authentication required", http.StatusUnauthorized)
			return
		}
		if deliveryQueue.Pending == nil {
			http.Error(w, "scheduled delivery unavailable", http.StatusServiceUnavailable)
			return
		}
		deliveries, err := deliveryQueue.Pending()
		if err != nil {
			diagnostics.Error("agent.cron.outbox.load", err)
			http.Error(w, "scheduled delivery unavailable", http.StatusServiceUnavailable)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(struct {
			Deliveries []chat.ScheduledDelivery `json:"deliveries"`
		}{Deliveries: deliveries}); err != nil {
			diagnostics.Error("agent.cron.outbox.encode", err)
		}
	})
	mux.HandleFunc(controltransport.ScheduledDeliveriesAckPath, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if !validControlRequest(r, controlSecret) {
			http.Error(w, "authentication required", http.StatusUnauthorized)
			return
		}
		if r.Header.Get("Content-Type") != "application/json" || deliveryQueue.Ack == nil {
			http.Error(w, "scheduled delivery unavailable", http.StatusUnsupportedMediaType)
			return
		}
		decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16*1024))
		decoder.DisallowUnknownFields()
		var request struct {
			IDs []string `json:"ids"`
		}
		if err := decoder.Decode(&request); err != nil {
			diagnostics.Error("agent.cron.outbox.ack_decode", err)
			http.Error(w, "invalid request", http.StatusBadRequest)
			return
		}
		var extra json.RawMessage
		if err := decoder.Decode(&extra); err != io.EOF {
			if err == nil {
				err = errors.New("request contains multiple JSON values")
			}
			diagnostics.Error("agent.cron.outbox.ack_trailing_data", err)
			http.Error(w, "invalid request", http.StatusBadRequest)
			return
		}
		if err := deliveryQueue.Ack(request.IDs); err != nil {
			diagnostics.Error("agent.cron.outbox.ack", err)
			http.Error(w, "scheduled delivery acknowledgement failed", http.StatusServiceUnavailable)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(struct {
			Acknowledged bool `json:"acknowledged"`
		}{Acknowledged: true}); err != nil {
			diagnostics.Error("agent.cron.outbox.ack_encode", err)
		}
	})
	mux.HandleFunc(controltransport.LoginPath, func(w http.ResponseWriter, r *http.Request) {
		handleAgentLogin(w, r, controlSecret, login)
	})
	mux.HandleFunc("/internal/restart-guard", func(w http.ResponseWriter, r *http.Request) {
		if !validControlRequest(r, controlSecret) {
			http.Error(w, "authentication required", http.StatusUnauthorized)
			return
		}
		if r.Method == http.MethodPut {
			id := r.Header.Get("X-Restart-Guard")
			restartGuardMu.Lock()
			valid := restartGuardHeld && restartGuardID == id && len(id) == 32
			if valid {
				restartGuardCommitted = true
			}
			restartGuardMu.Unlock()
			if !valid {
				http.Error(w, "invalid guard", http.StatusConflict)
				return
			}
			w.WriteHeader(http.StatusNoContent)
			return
		}
		if r.Method == http.MethodDelete {
			id := r.Header.Get("X-Restart-Guard")
			if len(id) != 32 {
				http.Error(w, "invalid guard", http.StatusBadRequest)
				return
			}
			releaseRestartGuard(id)
			w.WriteHeader(http.StatusNoContent)
			return
		}
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if !restartWorkMu.TryLock() {
			http.Error(w, "busy", http.StatusConflict)
			return
		}
		if len(listBackground) > 0 && listBackground[0] != nil {
			sessions, err := listBackground[0](r.Context())
			if err != nil {
				restartWorkMu.Unlock()
				diagnostics.Error("agent.restart.background", err)
				http.Error(w, "unavailable", http.StatusServiceUnavailable)
				return
			}
			for _, session := range sessions {
				if session.State != "exited" && session.State != "failed" && session.State != "stopped" && session.State != "timed_out" && session.State != "output_limited" {
					restartWorkMu.Unlock()
					http.Error(w, "busy", http.StatusConflict)
					return
				}
			}
		}
		var token [16]byte
		if _, err := cryptorand.Read(token[:]); err != nil {
			restartWorkMu.Unlock()
			diagnostics.Error("agent.restart.token", err)
			http.Error(w, "unavailable", http.StatusServiceUnavailable)
			return
		}
		id := hex.EncodeToString(token[:])
		restartGuardMu.Lock()
		restartGuardID = id
		restartGuardHeld = true
		restartGuardCommitted = false
		restartGuardMu.Unlock()
		time.AfterFunc(restartGuardDuration, func() { expireRestartGuard(id) })
		w.Header().Set("X-Restart-Guard", id)
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("/chat", func(w http.ResponseWriter, r *http.Request) {
		requestReference := newAgentRequestReference()
		w.Header().Set(agentRequestReferenceHeader, requestReference)
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if !validControlRequest(r, controlSecret) {
			http.Error(w, "authentication required", http.StatusUnauthorized)
			return
		}
		if !restartWorkMu.TryRLock() {
			http.Error(w, "busy", http.StatusConflict)
			return
		}
		defer restartWorkMu.RUnlock()
		if r.Header.Get("Content-Type") != "application/json" {
			http.Error(w, "expected application/json", http.StatusUnsupportedMediaType)
			return
		}
		var request struct {
			Message     string                `json:"message"`
			ChatID      int64                 `json:"chat_id"`
			ChatType    string                `json:"chat_type,omitempty"`
			ThreadID    int64                 `json:"thread_id,omitempty"`
			UserID      int64                 `json:"user_id,omitempty"`
			Attachments []attachment.Incoming `json:"attachments,omitempty"`
		}
		maxBodyBytes := int64(base64.StdEncoding.EncodedLen(int(attachment.MaxBytes))) + maxChatEnvelopeBytes
		body := &countingReader{Reader: http.MaxBytesReader(w, r.Body, maxBodyBytes)}
		dec := json.NewDecoder(body)
		dec.DisallowUnknownFields()
		if err := dec.Decode(&request); err != nil {
			diagnostics.Error("agent.chat.decode", err, diagnostics.Field{Key: "request_reference", Value: requestReference})
			http.Error(w, "invalid request", http.StatusBadRequest)
			return
		}
		if request.ChatID == 0 || request.ThreadID < 0 || (strings.TrimSpace(request.Message) == "" && len(request.Attachments) == 0) {
			http.Error(w, "invalid request", http.StatusBadRequest)
			return
		}
		environmentKind := classifyEnvironmentRequest(request.Message)
		isEnvironment := environmentKind != environmentRequestNone
		if isEnvironment && request.UserID <= 0 {
			http.Error(w, "invalid environment request", http.StatusBadRequest)
			return
		}
		if !isEnvironment && hasReservedEnvironmentAttachment(request.Attachments) {
			http.Error(w, "goaster.env uploads require /env", http.StatusBadRequest)
			return
		}
		if isEnvironment {
			environmentSerialMu.Lock()
			defer environmentSerialMu.Unlock()
		}
		// Any /env command-shaped request is a replacement attempt, even when
		// its attachment or command spelling is malformed and will be rejected
		// below. This prevents an older proposal from surviving a replacement
		// attempt.
		if environmentKind == environmentPreviewRequest || environmentKind == environmentMalformedPreviewRequest {
			key := environmentProposalKey{userID: request.UserID, chatID: request.ChatID}
			delete(environmentProposals, key)
		}
		if len(request.Attachments) > attachment.MaxAttachments {
			http.Error(w, "invalid request", http.StatusBadRequest)
			return
		}
		if err := dec.Decode(&struct{}{}); err != io.EOF {
			if err != nil {
				diagnostics.Error("agent.chat.trailing_data", err, diagnostics.Field{Key: "request_reference", Value: requestReference})
			} else {
				diagnostics.Error("agent.chat.trailing_data", errors.New("request contains multiple JSON values"), diagnostics.Field{Key: "request_reference", Value: requestReference})
			}
			http.Error(w, "invalid request", http.StatusBadRequest)
			return
		}
		// Preserve the original request envelope limit for text and metadata;
		// only the base64 representation of attachment bytes adds to it.
		envelopeBytes := body.bytesRead
		for _, item := range request.Attachments {
			envelopeBytes -= int64(base64.StdEncoding.EncodedLen(len(item.Data)))
		}
		if envelopeBytes > maxChatEnvelopeBytes {
			http.Error(w, "invalid request", http.StatusBadRequest)
			return
		}
		metadata := make([]attachment.Metadata, 0, len(request.Attachments))
		for _, item := range request.Attachments {
			validated, err := item.Validate()
			if errors.Is(err, attachment.ErrUnsupportedImage) {
				http.Error(w, "unsupported image format", http.StatusUnsupportedMediaType)
				return
			}
			if err != nil {
				http.Error(w, "invalid request", http.StatusBadRequest)
				return
			}
			metadata = append(metadata, validated)
		}
		if isEnvironment && environmentKind == environmentPreviewRequest &&
			len(request.Attachments) > 0 &&
			(len(request.Attachments) != 1 || len(metadata) != 1 ||
				request.Attachments[0].Filename != environmentFilename ||
				metadata[0].Filename != environmentFilename || metadata[0].Type != "document" ||
				metadata[0].Size > int64(localenv.MaxFileBytes)) {
			http.Error(w, "invalid environment request", http.StatusBadRequest)
			return
		}
		if isEnvironment && request.Message == environmentAcceptCommand && len(request.Attachments) != 0 {
			http.Error(w, "invalid environment request", http.StatusBadRequest)
			return
		}
		if !chat.IsClearCommand(request.Message) && !chat.IsModelCommand(request.Message) && !isEnvironment {
			var authErr error
			if checkAuth == nil {
				authErr = errors.New("authentication checker is unavailable")
			} else {
				authErr = checkAuth(r.Context())
			}
			if authErr != nil {
				diagnostics.Error("agent.chat.authentication", authErr, diagnostics.Field{Key: "request_reference", Value: requestReference})
				http.Error(w, "Agent request could not be started. Request reference: "+requestReference+".", http.StatusServiceUnavailable)
				return
			}
		}
		if len(metadata) > 0 {
			root, err := filepath.Abs(os.TempDir())
			if err != nil {
				diagnostics.Error("agent.chat.attachment_setup", err, diagnostics.Field{Key: "request_reference", Value: requestReference})
				http.Error(w, "agent request failed", http.StatusServiceUnavailable)
				return
			}
			directory, err := os.MkdirTemp(root, attachment.AgentTemporaryDirectoryPrefix)
			if err != nil {
				diagnostics.Error("agent.chat.attachment_setup", err, diagnostics.Field{Key: "request_reference", Value: requestReference})
				http.Error(w, "agent request failed", http.StatusServiceUnavailable)
				return
			}
			defer func() {
				if err := os.RemoveAll(directory); err != nil {
					diagnostics.Error("agent.chat.attachment_cleanup", err, diagnostics.Field{Key: "request_reference", Value: requestReference})
				}
			}()
			if err := os.Chmod(directory, 0700); err != nil {
				diagnostics.Error("agent.chat.attachment_setup", err, diagnostics.Field{Key: "request_reference", Value: requestReference})
				http.Error(w, "agent request failed", http.StatusServiceUnavailable)
				return
			}
			for index := range metadata {
				metadata[index].Path = filepath.Join(directory, metadata[index].Filename)
				file, err := os.OpenFile(metadata[index].Path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
				if err != nil {
					diagnostics.Error("agent.chat.attachment_write", err, diagnostics.Field{Key: "request_reference", Value: requestReference})
					http.Error(w, "agent request failed", http.StatusServiceUnavailable)
					return
				}
				if err = file.Chmod(0600); err == nil {
					_, err = file.Write(request.Attachments[index].Data)
				}
				closeErr := file.Close()
				if err != nil || closeErr != nil {
					diagnostics.Error("agent.chat.attachment_write", errors.Join(err, closeErr))
					http.Error(w, "agent request failed", http.StatusServiceUnavailable)
					return
				}
			}
		}
		environmentReply := func(userID, chatID int64, _ string, incoming []attachment.Incoming, metadata []attachment.Metadata) string {
			key := environmentProposalKey{userID: userID, chatID: chatID}
			now := time.Now()
			for proposalKey, proposal := range environmentProposals {
				if now.After(proposal.expiresAt) {
					delete(environmentProposals, proposalKey)
				}
			}
			if environmentKind == environmentMalformedPreviewRequest {
				delete(environmentProposals, key)
				return "Send /env with a document named goaster.env attached in the same message."
			}
			if environmentKind == environmentMalformedAcceptRequest {
				return "Reply exactly ACCEPT as a text message after reviewing the environment preview."
			}
			if environmentKind == environmentPreviewRequest {
				delete(environmentProposals, key)
				if len(metadata) != 1 || len(incoming) != 1 || metadata[0].Path == "" {
					return "Send /env with a document named goaster.env attached in the same message."
				}
				store, err := localenv.NewStore()
				if err != nil {
					diagnostics.Error("agent.environment.store", err, diagnostics.Field{Key: "request_reference", Value: requestReference})
					return "Environment settings are unavailable; no changes were saved."
				}
				if err := store.Load(); err != nil {
					diagnostics.Error("agent.environment.load", err, diagnostics.Field{Key: "request_reference", Value: requestReference})
					return "Environment settings are unavailable; no changes were saved."
				}
				values, err := localenv.LoadFile(metadata[0].Path)
				if err != nil {
					diagnostics.Error("agent.environment.input", err, diagnostics.Field{Key: "request_reference", Value: requestReference})
					return "The goaster.env file was rejected: use unique KEY=VALUE names, one setting per line, with no blank lines. No changes were saved."
				}
				changes := localenv.Changes(store.Values(), values)
				if len(changes.Added) == 0 && len(changes.Modified) == 0 {
					delete(environmentProposals, key)
					return "No environment changes detected; nothing was saved."
				}
				environmentProposals[key] = environmentProposal{
					baseDigest: localenv.Digest(store.Values()),
					values:     localenv.Merge(nil, values),
					expiresAt:  time.Now().Add(environmentProposalTTL),
				}
				return formatEnvironmentProposal(changes)
			}

			proposal, ok := environmentProposals[key]
			if !ok || now.After(proposal.expiresAt) {
				delete(environmentProposals, key)
				return "No environment proposal is pending. Send /env with goaster.env attached in the same message."
			}
			delete(environmentProposals, key)

			store, err := localenv.NewStore()
			if err != nil {
				diagnostics.Error("agent.environment.store", err, diagnostics.Field{Key: "request_reference", Value: requestReference})
				return "Environment settings are unavailable; no changes were saved."
			}
			if err := store.Load(); err != nil {
				diagnostics.Error("agent.environment.load", err, diagnostics.Field{Key: "request_reference", Value: requestReference})
				return "Environment settings are unavailable; no changes were saved."
			}
			changes, err := store.UpsertIfUnchanged(proposal.baseDigest, proposal.values)
			if errors.Is(err, localenv.ErrChanged) {
				diagnostics.Error("agent.environment.save", err, diagnostics.Field{Key: "request_reference", Value: requestReference})
				return "Environment settings changed since this preview; send /env with goaster.env attached in the same message to review a fresh proposal."
			}
			if err != nil {
				diagnostics.Error("agent.environment.save", err, diagnostics.Field{Key: "request_reference", Value: requestReference})
				return "Environment settings could not be saved; no changes were applied."
			}
			if len(changes.Added) == 0 && len(changes.Modified) == 0 {
				return "No environment changes detected; nothing was saved."
			}
			return formatEnvironmentApplied(changes)
		}
		if isEnvironment {
			reply := environmentReply(request.UserID, request.ChatID, request.Message, request.Attachments, metadata)
			w.Header().Set("Content-Type", "application/x-ndjson")
			w.Header().Set("Cache-Control", "no-cache")
			if err := json.NewEncoder(w).Encode(struct {
				Type  string `json:"type"`
				Reply string `json:"reply"`
			}{"final", reply}); err != nil {
				diagnostics.Error("agent.environment.stream_encode", err, diagnostics.Field{Key: "request_reference", Value: requestReference})
			}
			return
		}
		var retainedLease attachment.RetainedLease
		var retainedFiles []attachment.RetainedFile
		clearCommand := chat.IsClearCommand(request.Message)
		modelCommand := chat.IsModelCommand(request.Message)
		useRetained := !modelCommand && (!clearCommand || (strings.TrimSpace(request.Message) == "/clear" && len(metadata) == 0))
		if useRetained {
			root, err := attachment.DefaultRetainedRoot()
			if err != nil {
				diagnostics.Error("agent.chat.retained_root", err, diagnostics.Field{Key: "request_reference", Value: requestReference})
				http.Error(w, "Retained uploads are unavailable.", http.StatusServiceUnavailable)
				return
			}
			var release func()
			if !clearCommand {
				if sweepErr := attachment.SweepRetainedExpired(root, attachment.RetainedScope{ChatID: request.ChatID, ThreadID: request.ThreadID}); sweepErr != nil {
					diagnostics.Error("agent.chat.retained_sweep", sweepErr, diagnostics.Field{Key: "request_reference", Value: requestReference})
				}
			}
			retainedLease, release, err = attachment.AcquireRetainedScope(root, attachment.RetainedScope{ChatID: request.ChatID, ThreadID: request.ThreadID})
			if err != nil {
				diagnostics.Error("agent.chat.retained_scope", err, diagnostics.Field{Key: "request_reference", Value: requestReference})
				http.Error(w, "Retained uploads are unavailable.", http.StatusServiceUnavailable)
				return
			}
			defer release()
			if !clearCommand {
				for _, item := range request.Attachments {
					if _, err = retainedLease.Retain(item); err != nil {
						diagnostics.Error("agent.chat.retained_write", err, diagnostics.Field{Key: "request_reference", Value: requestReference})
						if errors.Is(err, attachment.ErrRetainedQuota) {
							http.Error(w, "Retained upload limit reached (128 files or 256 MiB). Use /clear or wait for expiry.", http.StatusRequestEntityTooLarge)
						} else {
							http.Error(w, "Retained uploads are unavailable. Use /clear to delete this conversation’s uploads and history, then upload files again.", http.StatusServiceUnavailable)
						}
						return
					}
				}
				retainedFiles, err = retainedLease.List()
				if err != nil {
					diagnostics.Error("agent.chat.retained_list", err, diagnostics.Field{Key: "request_reference", Value: requestReference})
					http.Error(w, "Retained uploads are unavailable. Use /clear to delete this conversation’s uploads and history, then upload files again.", http.StatusServiceUnavailable)
					return
				}
			}
		}
		w.Header().Set("Content-Type", "application/x-ndjson")
		w.Header().Set("Cache-Control", "no-cache")
		encoder := json.NewEncoder(w)
		streamContext, cancelStream := context.WithCancel(r.Context())
		defer cancelStream()
		_, canFlush := w.(http.Flusher)
		streamEmitted := false
		var streamErr error
		var streamOperation, streamEvent, streamPhase string
		defer func() {
			if streamErr != nil {
				diagnostics.Error(streamOperation, streamErr,
					diagnostics.Field{Key: "event", Value: streamEvent},
					diagnostics.Field{Key: "phase", Value: streamPhase},
					diagnostics.Field{Key: "request_reference", Value: requestReference},
				)
			}
		}()
		emit := func(eventName, phase string, event any) {
			if streamErr != nil {
				return
			}
			streamEmitted = true
			if err := encoder.Encode(event); err != nil {
				streamErr = fmt.Errorf("encode NDJSON event: %w", err)
				cancelStream()
				streamOperation, streamEvent, streamPhase = "agent.chat.stream_encode", eventName, phase
				return
			}
			if canFlush {
				if err := http.NewResponseController(w).Flush(); err != nil {
					streamErr = fmt.Errorf("flush NDJSON event: %w", err)
					cancelStream()
					streamOperation, streamEvent, streamPhase = "agent.chat.stream_flush", eventName, phase
				}
			}
		}
		if chat.IsModelCommand(request.Message) {
			if len(metadata) != 0 {
				http.Error(w, "model command does not accept attachments", http.StatusBadRequest)
				return
			}
			emit("final", "model_command", struct {
				Type  string `json:"type"`
				Reply string `json:"reply"`
			}{"final", modelCommandReply(registry, request.ChatID, request.Message)})
			return
		}
		observer := func(name, arguments string) {
			if name == "send_file" {
				arguments = ""
			}
			emit("tool_call", "tool_observer", struct {
				Type      string `json:"type"`
				Name      string `json:"name"`
				Arguments string `json:"arguments"`
			}{"tool_call", name, arguments})
		}
		session := sessionFor(request.ChatID, request.ThreadID)
		if session == nil {
			err := errors.New("agent conversation session is unavailable")
			diagnostics.Error("agent.chat.session", err, diagnostics.Field{Key: "reason", Value: "session factory returned nil"}, diagnostics.Field{Key: "request_reference", Value: requestReference})
			http.Error(w, "conversation session unavailable", http.StatusServiceUnavailable)
			return
		}
		if !chat.IsClearCommand(request.Message) {
			if notice := session.PendingModelRecoveryNotice(); notice != "" {
				emit("model_recovery", "initial_notice", struct {
					Type    string `json:"type"`
					Message string `json:"message"`
				}{"model_recovery", notice})
			}
		}
		if streamErr != nil {
			return
		}
		requestContext := chat.WithConversationType(streamContext, request.ChatType)
		if useRetained && !clearCommand {
			requestContext = chat.WithRetainedAttachments(requestContext, retainedFiles)
		}
		requestContext = chat.WithConversationID(requestContext, request.ChatID)
		requestContext = chat.WithConversationThreadID(requestContext, request.ThreadID)
		requestContext = chat.WithToolCallObserver(requestContext, observer)
		requestContext = chat.WithRuntimeFailureOperation(requestContext, "agent.chat")
		requestContext = chat.WithRequestReference(requestContext, requestReference)
		values, environmentErr := runtimeEnvironment()
		if environmentErr != nil {
			diagnostics.Error("agent.chat.runtime_environment", environmentErr, diagnostics.Field{Key: "request_reference", Value: requestReference})
			requestContext = chat.WithShellEnvironmentUnavailable(requestContext)
		} else if len(values) > 0 {
			requestContext = chat.WithShellEnvironment(requestContext, values)
		}
		reply, err := session.ReplyWithAttachments(requestContext, request.Message, metadata)
		if streamErr != nil {
			if errors.Is(err, context.Canceled) {
				streamErr = errors.Join(streamErr, err)
			}
			return
		}
		if err != nil {
			if errors.Is(err, attachment.ErrUnsupportedImage) {
				http.Error(w, "unsupported image format", http.StatusUnsupportedMediaType)
				return
			}
			if message := chat.PostToolAuthenticationMessage(err); message != "" {
				if streamEmitted {
					emit("post_tool_authentication", "session_error", struct {
						Type  string `json:"type"`
						Error string `json:"error"`
					}{"post_tool_authentication", message})
					return
				}
				w.Header().Set("X-GoAster-Error", "post-tool-authentication")
				http.Error(w, message, http.StatusServiceUnavailable)
				return
			}
			if message := chat.SkillRecoveryMessage(err); message != "" {
				if streamEmitted {
					emit("skill_recovery", "session_error", struct {
						Type  string `json:"type"`
						Error string `json:"error"`
					}{"skill_recovery", message})
					return
				}
				w.Header().Set("X-GoAster-Error", "skill-recovery")
				http.Error(w, message, http.StatusServiceUnavailable)
				return
			}
			if errors.Is(err, chat.ErrModelUnavailable) {
				if streamEmitted {
					emit("error", "session_error", struct {
						Type  string `json:"type"`
						Error string `json:"error"`
					}{"error", boundedAgentModelError(err)})
					return
				}
				http.Error(w, boundedAgentModelError(err), http.StatusServiceUnavailable)
				return
			}
			if streamEmitted {
				classification, reference, ok := chat.ModelStreamFailureInfo(err)
				if !ok {
					classification = "model_request_failed"
				}
				if reference == "" {
					reference = requestReference
				}
				progress, hasProgress := chat.ToolProgressDetails(err)
				errorEvent := struct {
					Type               string `json:"type"`
					Error              string `json:"error"`
					Classification     string `json:"classification"`
					Reference          string `json:"reference"`
					ToolStarted        bool   `json:"tool_started"`
					ToolCompleted      bool   `json:"tool_completed"`
					ToolStartedCount   int    `json:"tool_started_count"`
					ToolCompletedCount int    `json:"tool_completed_count"`
				}{
					Type:               "error",
					Error:              chat.ModelStreamMessageForError(err, reference),
					Classification:     classification,
					Reference:          reference,
					ToolStarted:        hasProgress && progress.Started > 0,
					ToolCompleted:      hasProgress && progress.Completed > 0,
					ToolStartedCount:   progress.Started,
					ToolCompletedCount: progress.Completed,
				}
				emit("error", "session_error", errorEvent)
				return
			}
			if chat.IsModelStreamError(err) {
				classification, _, ok := chat.ModelStreamFailureInfo(err)
				if !ok {
					classification = "model_stream"
				}
				w.Header().Set("X-GoAster-Error", "model-stream")
				w.Header().Set(agentErrorClassificationHeader, classification)
				http.Error(w, chat.ModelStreamMessageForError(err, requestReference), http.StatusServiceUnavailable)
				return
			}
			http.Error(w, "agent request failed. Request reference: "+requestReference+".", http.StatusServiceUnavailable)
			return
		}
		if clearCommand && err == nil && len(metadata) == 0 {
			if strings.TrimSpace(request.Message) == "/clear" && reply == "Conversation history cleared." {
				if clearErr := retainedLease.Clear(); clearErr != nil {
					diagnostics.Error("agent.chat.retained_clear", clearErr, diagnostics.Field{Key: "request_reference", Value: requestReference})
					reply += " Retained uploads could not be cleared; please retry /clear."
				} else {
					reply += " Retained uploads cleared."
				}
			} else if strings.HasPrefix(reply, "Removed ") {
				reply += " Retained files remain until expiry or a full /clear."
			}
		}

		emit("final", "final_reply", struct {
			Type  string `json:"type"`
			Reply string `json:"reply"`
		}{"final", reply})
	})
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Host != "127.0.0.1:8765" && r.Host != "127.0.0.1" {
			http.Error(w, "invalid host", http.StatusForbidden)
			return
		}
		mux.ServeHTTP(w, r)
	})
}

func validControlRequest(r *http.Request, controlSecret string) bool {
	if r == nil || controlSecret == "" || strings.ContainsAny(controlSecret, "\r\n") {
		return false
	}
	provided := r.Header.Get(controlAuthorizationHeader)
	return subtle.ConstantTimeCompare([]byte(provided), []byte(controlSecret)) == 1
}

func isEnvironmentRequest(message string) bool {
	return classifyEnvironmentRequest(message) != environmentRequestNone
}

func hasReservedEnvironmentAttachment(attachments []attachment.Incoming) bool {
	for _, item := range attachments {
		if attachment.SanitizeFilename(item.Filename) == environmentFilename {
			return true
		}
	}
	return false
}

func classifyEnvironmentRequest(message string) environmentRequestKind {
	switch message {
	case environmentCommand:
		return environmentPreviewRequest
	case environmentAcceptCommand:
		return environmentAcceptRequest
	case environmentAcceptKeyword:
		return environmentMalformedAcceptRequest
	}
	if isEnvironmentCommandMention(message) {
		return environmentPreviewRequest
	}
	if strings.HasPrefix(message, environmentCommand+"@") {
		return environmentMalformedPreviewRequest
	}
	if hasEnvironmentWhitespacePrefix(message, environmentCommand) {
		return environmentMalformedPreviewRequest
	}
	if hasEnvironmentWhitespacePrefix(message, environmentAcceptKeyword) {
		return environmentMalformedAcceptRequest
	}
	return environmentRequestNone
}

func hasEnvironmentWhitespacePrefix(message, prefix string) bool {
	if !strings.HasPrefix(message, prefix) {
		return false
	}
	rest := message[len(prefix):]
	for _, character := range rest {
		return unicode.IsSpace(character)
	}
	return false
}

func isEnvironmentCommandMention(message string) bool {
	prefix := environmentCommand + "@"
	if !strings.HasPrefix(message, prefix) {
		return false
	}
	username := message[len(prefix):]
	if username == "" {
		return false
	}
	for _, character := range username {
		if character >= 'a' && character <= 'z' || character >= 'A' && character <= 'Z' ||
			character >= '0' && character <= '9' || character == '_' {
			continue
		}
		return false
	}
	return true
}

func formatEnvironmentNames(label string, names []string) string {
	if len(names) == 0 {
		return label + ": none"
	}
	return label + ": " + strings.Join(names, ", ")
}

func formatEnvironmentProposal(changes localenv.ChangeSet) string {
	return "Environment proposal (values hidden).\n" +
		formatEnvironmentNames("Added", changes.Added) + "\n" +
		formatEnvironmentNames("Modified", changes.Modified) + "\n" +
		"Reply exactly ACCEPT to apply this proposal."
}

func formatEnvironmentApplied(changes localenv.ChangeSet) string {
	return "Environment settings saved locally (values hidden).\n" +
		formatEnvironmentNames("Added", changes.Added) + "\n" +
		formatEnvironmentNames("Modified", changes.Modified) + "\n" +
		"The new settings are available to subsequent GoAster shell commands. Settings read by long-lived processes at startup may require a restart; no services were restarted."
}

func runtimeEnvironment() (map[string]string, error) {
	store, err := localenv.NewStore()
	if err != nil {
		return nil, err
	}
	if err := store.Load(); err != nil {
		return nil, err
	}
	return store.Values(), nil
}

func modelCommandReply(registry chat.ModelRegistry, chatID int64, message string) string {
	reference, ok := chat.ModelCommandArgument(message)
	if !ok {
		return "Usage: /m [MODEL_OR_ALIAS]"
	}
	if strings.TrimSpace(reference) == "" {
		state, err := registry.Current(chatID)
		if err != nil {
			diagnostics.Error("agent.model.current", err)
			return "Model selection could not be read."
		}
		return formatModelSelection("Current model", state.Selection)
	}
	if strings.ContainsAny(reference, " \t\r\n") {
		return "Usage: /m [MODEL_OR_ALIAS]"
	}
	selection, err := registry.Select(chatID, reference)
	if err != nil {
		diagnostics.Error("agent.model.select", err, diagnostics.Field{Key: "model", Value: reference})
		if errors.Is(err, chat.ErrModelConfigBroken) {
			return "Model configuration is recovering; using emergency model " + chat.EmergencyModel + "."
		}
		if errors.Is(err, chat.ErrModelNotConfigured) {
			return "Model is not configured: " + boundedAgentText(reference, 128)
		}
		return "Model selection could not be saved."
	}
	return formatModelSelection("Selected model", selection)
}

func formatModelSelection(prefix string, selection chat.ModelSelection) string {
	if selection.Alias == "" {
		return prefix + ": " + selection.ID
	}
	return prefix + ": " + selection.ID + " (" + selection.Alias + ")"
}

func boundedAgentModelError(err error) string {
	return boundedAgentText(err.Error(), 512)
}

func boundedAgentText(value string, max int) string {
	value = strings.Join(strings.Fields(value), " ")
	runes := []rune(value)
	if len(runes) <= max {
		return value
	}
	return string(runes[:max-1]) + "…"
}
