package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"

	"errors"
	"goaster/internal/diagnostics"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"goaster/internal/chat"
)

func TestModelDiagnostic(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "models.json")
	original := `{"provider":"openai","models":[{"id":"default","alias":"d"},{"id":"candidate","alias":"alias"}],"chats":{"12":"default"}}`
	if err := os.WriteFile(path, []byte(original), 0600); err != nil {
		t.Fatal(err)
	}
	registry := chat.NewModelRegistryAt(path)
	for _, result := range []string{modelTestSentinel, "wrong", "", " " + modelTestSentinel, modelTestSentinel + " ", modelTestSentinel + "\n", "x" + modelTestSentinel, modelTestSentinel + "x"} {
		var out bytes.Buffer
		calls := 0
		err := testConfiguredModel(context.Background(), "candidate", &out, registry, func(ctx context.Context, id string) (string, error) {
			calls++
			if id != "candidate" {
				t.Errorf("model %q", id)
			}
			return result, nil
		})
		if (err == nil) != (result == modelTestSentinel) || calls != 1 {
			t.Fatalf("result %q error %v calls %d", result, err, calls)
		}
		var record struct{ Model, Status, Stage string }
		if len(out.Bytes()) > 128 || strings.Contains(out.String(), "secret") || strings.Contains(out.String(), "private-") {
			t.Fatalf("unsafe output %q", out.String())
		}
		if e := json.Unmarshal(out.Bytes(), &record); e != nil || record.Model != "candidate" || (err == nil && record.Status != "ok") || (err != nil && (record.Status != "error" || record.Stage != "response_mismatch")) {
			t.Fatalf("bad record %s: %v", out.String(), e)
		}
		if result != modelTestSentinel && (strings.Contains(out.String(), result) && result != "" || strings.Contains(err.Error(), result) && result != "") {
			t.Fatalf("leaked response: %q %v", out.String(), err)
		}
	}
	for _, id := range []string{"alias", "", "missing", " candidate", "secret-token-do-not-echo"} {
		calls := 0
		var out bytes.Buffer
		if err := testConfiguredModel(context.Background(), id, &out, registry, func(context.Context, string) (string, error) { calls++; return "", nil }); err == nil || calls != 0 || strings.Contains(out.String(), id) && id != "" {
			t.Fatalf("id %q: %v calls %d", id, err, calls)
		}
	}
	cause := errors.New("secret provider body")
	err := testConfiguredModel(context.Background(), "candidate", &bytes.Buffer{}, registry, func(context.Context, string) (string, error) { return "", cause })
	if !errors.Is(err, cause) || strings.Contains(err.Error(), "secret") {
		t.Fatalf("provider error %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err = testConfiguredModel(ctx, "candidate", &bytes.Buffer{}, registry, func(ctx context.Context, _ string) (string, error) { return "", ctx.Err() })
	if !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	deadline, stop := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer stop()
	err = testConfiguredModel(deadline, "candidate", &bytes.Buffer{}, registry, func(ctx context.Context, _ string) (string, error) { return "", ctx.Err() })
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal(err)
	}
	closed, err := os.CreateTemp(dir, "output")
	if err != nil {
		t.Fatal(err)
	}
	if err := closed.Close(); err != nil {
		t.Fatal(err)
	}
	err = testConfiguredModel(context.Background(), "candidate", closed, registry, func(context.Context, string) (string, error) { return modelTestSentinel, nil })
	if err == nil {
		t.Fatal("output failure was ignored")
	}
	data, _ := os.ReadFile(path)
	info, _ := os.Stat(path)
	if string(data) != original || info.Mode().Perm() != 0600 {
		t.Fatal("configuration changed")
	}
	if err := os.WriteFile(path, []byte(`{"provider":"openai","models":[{"id":"other"}]}`), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := registry.ExactConfiguredID("candidate"); err == nil {
		t.Fatal("stale configuration")
	}
	for _, content := range []string{"invalid", strings.Repeat("x", 65537)} {
		if err := os.WriteFile(path, []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := registry.ExactConfiguredID("candidate"); err == nil {
			t.Fatal("invalid configuration accepted")
		}
		data, _ := os.ReadFile(path)
		if string(data) != content {
			t.Fatal("recovery changed configuration")
		}
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if _, err := registry.ExactConfiguredID("candidate"); err == nil {
		t.Fatal("missing configuration accepted")
	}
	if _, err := os.Lstat(path); !os.IsNotExist(err) {
		t.Fatal("missing configuration created")
	}
	if err := os.Symlink(filepath.Join(dir, "target"), path); err != nil {
		t.Fatal(err)
	}
	if _, err := registry.ExactConfiguredID("candidate"); err == nil {
		t.Fatal("symlink accepted")
	}
}

func TestModelDiagnosticArguments(t *testing.T) {
	for _, args := range [][]string{{"test-model"}, {"test-model", ""}, {"test-model", "secret-argument", "extra-secret"}} {
		started := false
		var out bytes.Buffer
		err := run(context.Background(), args, &out, nil, func(chat.CredentialStore) error { started = true; return nil })
		if err == nil || started || out.String() != "{\"status\":\"error\",\"stage\":\"configuration\"}\n" || strings.Contains(err.Error(), "secret") {
			t.Fatalf("error=%v output=%q started=%v", err, out.String(), started)
		}
	}
	cause := errors.New("startup private cause")
	var out bytes.Buffer
	err := modelTestStartupFailure(&out, cause)
	if !errors.Is(err, cause) || out.String() != "{\"status\":\"error\",\"stage\":\"configuration\"}\n" {
		t.Fatalf("startup: %v %q", err, out.String())
	}
	calls := 0
	err = modelTestStartupFailure(shortDiagnosticWriter{&calls}, cause)
	if calls != 1 || !errors.Is(err, cause) || !errors.Is(err, io.ErrShortWrite) {
		t.Fatalf("startup output: %v", err)
	}
}

func TestModelDiagnosticActualWiring(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	path := filepath.Join(home, ".goaster", "artefacts", "config", "openai-models.json")
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	original := `{"provider":"openai","models":[{"id":"candidate-exact"},{"id":"other-model"}],"chats":{"12":"other-model"}}`
	if err := os.WriteFile(path, []byte(original), 0600); err != nil {
		t.Fatal(err)
	}
	store, err := agentCredentialStore(os.UserHomeDir)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Save(chat.Credential{AccessToken: "agent-sentinel", RefreshToken: "agent-refresh-secret", ExpiresAt: time.Now().Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}
	other, err := chat.NewCredentialStore(filepath.Join(home, ".goaster", "auth.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := other.Save(chat.Credential{AccessToken: "terminal-secret", ExpiresAt: time.Now().Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}
	old := http.DefaultTransport
	defer func() { http.DefaultTransport = old }()
	for _, scenario := range []string{"success", "mismatch", "provider", "unsupported", "auth", "transient", "cancel", "output"} {
		t.Run(scenario, func(t *testing.T) {
			calls := 0
			refreshCalls := 0
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			http.DefaultTransport = parityTransport(func(r *http.Request) (*http.Response, error) {
				if r.URL.Path == "/oauth/token" {
					refreshCalls++
					var request map[string]string
					if json.NewDecoder(r.Body).Decode(&request) != nil || request["refresh_token"] != "agent-refresh-secret" {
						t.Error("wrong refresh credential")
					}
					return &http.Response{StatusCode: 401, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"error":{"code":"invalid_grant"}}`)), Request: r}, nil
				}
				calls++
				if r.Header.Get("Authorization") != "Bearer agent-sentinel" {
					t.Errorf("unexpected authorization")
				}
				var req struct {
					Model string `json:"model"`
					Input []struct {
						Role    string `json:"role"`
						Content []struct {
							Text string `json:"text"`
						} `json:"content"`
					} `json:"input"`
					Tools []json.RawMessage `json:"tools"`
				}
				if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
					t.Fatal(err)
				}
				if req.Model != "candidate-exact" || len(req.Tools) != 0 || len(req.Input) != 1 || req.Input[0].Role != "user" || len(req.Input[0].Content) != 1 || req.Input[0].Content[0].Text != modelTestPrompt {
					t.Errorf("unexpected request: %+v", req)
				}
				if scenario == "cancel" {
					cancel()
					return nil, r.Context().Err()
				}
				if scenario == "provider" {
					return nil, errors.New("private-provider-text")
				}
				if scenario == "unsupported" || scenario == "auth" || scenario == "transient" {
					status := map[string]int{"unsupported": 400, "auth": 401, "transient": 503}[scenario]
					body := `{"error":{"code":"model_not_found"}}`
					if scenario != "unsupported" {
						body = `{"error":{"code":"service_unavailable"}}`
					}
					header := make(http.Header)
					header.Set("Retry-After", "0")
					return &http.Response{StatusCode: status, Header: header, Body: io.NopCloser(strings.NewReader(body)), Request: r}, nil
				}
				body := `{"output_text":"GOASTER_MODEL_TEST_OK"}`
				if scenario == "mismatch" {
					body = `{"output_text":"private-mismatch-text"}`
				}
				return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body)), Request: r}, nil
			})
			var out, log bytes.Buffer
			restore := diagnostics.SetOutput(&log)
			defer restore()
			var writer io.Writer = &out
			if scenario == "output" {
				f, e := os.CreateTemp(t.TempDir(), "closed")
				if e != nil {
					t.Fatal(e)
				}
				f.Close()
				writer = f
			}
			started := false
			err := run(ctx, []string{"test-model", "candidate-exact"}, writer, chat.NewOpenAIAuthenticator(store), func(chat.CredentialStore) error { started = true; return nil })
			if scenario == "cancel" && !errors.Is(err, context.Canceled) {
				t.Fatalf("cancellation cause: %v", err)
			}
			if started || calls == 0 || (err == nil) != (scenario == "success") {
				t.Fatalf("err=%v calls=%d started=%v", err, calls, started)
			}
			if err != nil {
				logAgentError(err)
				if scenario != "cancel" && (strings.Count(log.String(), `operation=agent.model_test`) < 1 || strings.Contains(log.String(), `operation=chat.provider`) || !strings.Contains(log.String(), `configured_model="candidate-exact"`) || !strings.Contains(log.String(), `model_test_stage="`+map[string]string{"provider": "provider", "unsupported": "provider", "auth": "provider", "transient": "provider", "mismatch": "response_mismatch", "output": "output"}[scenario]+`"`)) {
					t.Fatalf("diagnostic %s", log.String())
				}
				if strings.Contains(log.String(), "private-") || strings.Contains(log.String(), "agent-sentinel") || strings.Contains(log.String(), "agent-refresh-secret") || strings.Contains(log.String(), "terminal-secret") {
					t.Fatalf("private content %s", log.String())
				}
			}
			if scenario == "unsupported" || scenario == "auth" || scenario == "transient" {
				status := map[string]string{"unsupported": "400", "auth": "401", "transient": "503"}[scenario]
				fields := chat.ProviderFailureDiagnosticFields(err)
				foundPhase, foundStatus := false, false
				for _, f := range fields {
					if f.Key == "phase" && f.Value == "pre-stream" {
						foundPhase = true
					}
					if f.Key == "status" && f.Value == status {
						foundStatus = true
					}
				}
				if !foundPhase || !foundStatus || errors.Unwrap(err) == nil || (scenario == "auth" && !errors.Is(err, chat.ErrLoginRequired)) || (scenario == "unsupported" && !strings.Contains(log.String(), `backend_code="model_not_found"`)) {
					t.Fatalf("missing typed cause: %v %+v", err, fields)
				}
				wantCalls := 1
				if scenario == "transient" {
					wantCalls = 3
				}
				if (scenario == "auth" && refreshCalls != 1) || (scenario != "auth" && refreshCalls != 0) {
					t.Fatalf("refresh calls %d", refreshCalls)
				}
				if calls != wantCalls {
					t.Fatalf("calls %d want %d", calls, wantCalls)
				}
				lines := strings.Split(strings.TrimSpace(log.String()), "\n")
				if len(lines) != wantCalls+1 {
					t.Fatalf("attempts and final: %s", log.String())
				}
				for i := 0; i < wantCalls; i++ {
					if !strings.Contains(lines[i], `attempt="`+string(rune('1'+i))+`"`) {
						t.Fatalf("attempt records: %s", log.String())
					}
				}
				if strings.Contains(lines[len(lines)-1], `attempt="`) || !strings.Contains(lines[len(lines)-1], "operation=agent.model_test") {
					t.Fatalf("final: %s", log.String())
				}
			}
			if scenario != "output" {
				var record struct{ Model, Status, Stage string }
				if e := json.Unmarshal(out.Bytes(), &record); e != nil || len(out.Bytes()) > 128 || strings.Contains(out.String(), "secret") || strings.Contains(out.String(), "private-") || record.Model != "candidate-exact" || (scenario == "success" && record.Status != "ok") || (scenario != "success" && (record.Status != "error" || record.Stage != map[string]string{"provider": "provider", "unsupported": "provider", "auth": "provider", "transient": "provider", "cancel": "provider", "mismatch": "response_mismatch"}[scenario])) {
					t.Fatalf("record %s %v", out.String(), e)
				}
			}
			data, e := os.ReadFile(path)
			info, e2 := os.Stat(path)
			if e != nil || e2 != nil || string(data) != original || info.Mode().Perm() != 0600 {
				t.Fatal("config changed")
			}
		})
	}
}

func TestModelDiagnosticProtectedResolver(t *testing.T) {
	dir := t.TempDir()
	targetDir := filepath.Join(dir, "real")
	if err := os.Mkdir(targetDir, 0700); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(targetDir, "models.json")
	original := `{"provider":"openai","models":[{"id":"candidate"}]}`
	if err := os.WriteFile(target, []byte(original), 0600); err != nil {
		t.Fatal(err)
	}
	fileLink := filepath.Join(dir, "file-link")
	dirLink := filepath.Join(dir, "dir-link")
	if err := os.Symlink(target, fileLink); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(targetDir, dirLink); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{fileLink, filepath.Join(dirLink, "models.json")} {
		if _, err := chat.NewModelRegistryAt(path).ExactConfiguredID("candidate"); err == nil {
			t.Fatal("symlink accepted")
		}
	}
	if err := os.Chmod(target, 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := chat.NewModelRegistryAt(target).ExactConfiguredID("candidate"); err == nil {
		t.Fatal("unprotected file accepted")
	}
	data, err := os.ReadFile(target)
	info, statErr := os.Stat(target)
	if err != nil || statErr != nil || string(data) != original || info.Mode().Perm() != 0644 {
		t.Fatal("target changed")
	}
}

type shortDiagnosticWriter struct{ calls *int }

func (w shortDiagnosticWriter) Write(p []byte) (int, error) {
	*w.calls++
	return len(p) / 2, nil
}

func TestModelDiagnosticShortWrite(t *testing.T) {
	calls := 0
	cause := errors.New("private cause")
	err := writeModelTestResult(shortDiagnosticWriter{&calls}, "candidate", modelTestError{"provider", "candidate", cause})
	var result modelTestError
	if calls != 1 || !errors.As(err, &result) || result.stage != "output" || !errors.Is(err, io.ErrShortWrite) || !errors.Is(err, cause) {
		t.Fatalf("calls=%d error=%v", calls, err)
	}
}
