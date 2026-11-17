package telegram

import (
	"context"
	"errors"
	"fmt"
	"goaster/internal/diagnostics"
	"goaster/internal/migration"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
)

func TestMigrationHandoffUsesOnlyStoredPairedUser(t *testing.T) {
	runtime := t.TempDir()
	paths := Paths{Token: filepath.Join(runtime, "token"), UserID: filepath.Join(runtime, "user-id")}
	if err := writePrivate(paths.Token, []byte(testToken)); err != nil {
		t.Fatal(err)
	}
	if err := writePrivate(paths.UserID, []byte("42")); err != nil {
		t.Fatal(err)
	}
	var requestPath string
	var requestBody []byte
	var requestPaths []string
	var requestBodies []string
	changeAfterMessage := false
	var switchErr error
	client := doerFunc(func(request *http.Request) (*http.Response, error) {
		requestPath = request.URL.Path
		var err error
		requestBody, err = io.ReadAll(request.Body)
		if err != nil {
			return nil, err
		}
		requestPaths = append(requestPaths, request.URL.Path)
		requestBodies = append(requestBodies, string(requestBody))
		if changeAfterMessage && strings.HasSuffix(request.URL.Path, "/sendMessage") {
			switchErr = os.WriteFile(paths.UserID, []byte("99"), 0600)
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     make(http.Header),
			Body:       io.NopCloser(strings.NewReader(`{"ok":true,"result":{"message_id":1}}`)),
		}, nil
	})
	parts, err := PrepareRecipientMigrationHandoffPartsWith(paths, client, "https://telegram.synthetic", 42)
	if err != nil {
		t.Fatal(err)
	}
	if err := parts(t.Context(), "https://web.example/migration#attempt=abc&owner=secret", []byte("-----BEGIN PRIVATE KEY-----\nkey\n-----END PRIVATE KEY-----\n")); err != nil {
		t.Fatal(err)
	}
	if len(requestPaths) != 2 || !strings.HasSuffix(requestPaths[0], "/sendMessage") || !strings.HasSuffix(requestPaths[1], "/sendDocument") || !strings.Contains(requestBodies[0], "https://web.example/migration#attempt=abc") || !strings.Contains(requestBodies[0], "owner=secret") || !strings.Contains(requestBodies[1], "BEGIN PRIVATE KEY") {
		t.Fatalf("split handoff requests = %q / %q", requestPaths, requestBodies)
	}
	changeAfterMessage = true
	if err := parts(t.Context(), "https://web.example/migration#attempt=partial&owner=secret", []byte("-----BEGIN PRIVATE KEY-----\npartial\n-----END PRIVATE KEY-----\n")); err == nil || switchErr != nil || len(requestPaths) != 3 || !strings.HasSuffix(requestPaths[2], "/sendMessage") {
		t.Fatalf("owner change after link was accepted: err=%v switch=%v paths=%q", err, switchErr, requestPaths)
	}
	if requestPath != "/bot"+testToken+"/sendMessage" {
		t.Fatalf("Telegram endpoint = %q", requestPath)
	}
	if !strings.Contains(string(requestBody), `"chat_id":42`) || strings.Contains(string(requestBody), `"chat_id":99`) {
		t.Fatalf("paired-user message body = %q", requestBody)
	}
}

func TestMigrationOutcomeTextForExistingTargetIsHonest(t *testing.T) {
	text := migrationOutcomeText(migration.OneShotResult{
		CreationState: "not_created",
		Code:          "target_exists",
		ServerName:    "goaster-migration-0123456789abcdef",
	})
	for _, phrase := range []string{"Existing GoAster server found", "No new server was created", "does not mean setup or SSH private-key material was recovered"} {
		if !strings.Contains(text, phrase) {
			t.Fatalf("existing-target outcome missing %q: %s", phrase, text)
		}
	}
}

func TestOfflineMigrationRecipientChangePreventsDelivery(t *testing.T) {
	runtime := t.TempDir()
	paths := Paths{Token: filepath.Join(runtime, "token"), UserID: filepath.Join(runtime, "user-id")}
	if err := writePrivate(paths.Token, []byte(testToken)); err != nil {
		t.Fatal(err)
	}
	if err := writePrivate(paths.UserID, []byte("42")); err != nil {
		t.Fatal(err)
	}
	calls := 0
	client := doerFunc(func(*http.Request) (*http.Response, error) { calls++; return nil, nil })
	deliver, err := PrepareRecipientMigrationHandoffPartsWith(paths, client, "https://telegram.synthetic", 42)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(paths.UserID, []byte("99"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := deliver(t.Context(), "https://web.example/migration#attempt=changed&owner=secret", []byte("synthetic")); err == nil || calls != 0 {
		t.Fatalf("changed pairing accepted: err=%v calls=%d", err, calls)
	}
	if _, err := PrepareRecipientMigrationHandoffPartsWith(paths, client, "https://telegram.synthetic", 42); err == nil {
		t.Fatal("changed pairing accepted for preparation")
	}
}

type migrationTestLog chan string

func (log migrationTestLog) Write(data []byte) (int, error) {
	log <- string(data)
	return len(data), nil
}

func TestMigrationTransportEOFThroughCreateOnce(t *testing.T) {
	const targetName = "goaster-migration-0123456789abcdef"
	paths := Paths{Token: filepath.Join(t.TempDir(), "token"), UserID: filepath.Join(t.TempDir(), "user-id")}
	if err := writePrivate(paths.Token, []byte(testToken)); err != nil {
		t.Fatal(err)
	}
	if err := writePrivate(paths.UserID, []byte("42")); err != nil {
		t.Fatal(err)
	}
	var uploads, messages, creates atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/migration/create" {
			creates.Add(1)
			secret := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
			w.WriteHeader(http.StatusCreated)
			fmt.Fprintf(w, `{"creation_state":"created","attempt":"attempt1","server_label":"%s","server_id":42,"ip":"192.0.2.1","location":"fsn1","expires":2000000000,"setup_url":"http://%s/setup#owner=%s"}`, targetName, r.Host, secret)
			return
		}
		if strings.HasSuffix(r.URL.Path, "/sendMessage") {
			messages.Add(1)
			io.Copy(io.Discard, r.Body)
			fmt.Fprint(w, `{"ok":true,"result":{"message_id":1}}`)
			return
		}
		uploads.Add(1)
		io.Copy(io.Discard, r.Body)
		conn, _, err := w.(http.Hijacker).Hijack()
		if err != nil {
			t.Error(err)
			return
		}
		conn.Close()
	}))
	defer server.Close()
	opts := migration.DefaultOptions()
	opts.WebOrigin = server.URL
	opts.AllowInsecureHTTP = true
	opts.HTTPClient = server.Client()
	opts.LoadHetznerToken = func() (string, error) { return "private-provider-token", nil }
	opts.MigrationRecipient = func() (int64, error) { return migrationRecipient(paths) }
	var deliveryCause error
	opts.PrepareRecipientHandoffParts = func(id int64) (func(context.Context, string, []byte) error, error) {
		deliver, err := PrepareRecipientMigrationHandoffPartsWith(paths, server.Client(), server.URL, id)
		if err != nil {
			return nil, err
		}
		return func(ctx context.Context, setupURL string, privateKey []byte) error {
			deliveryCause = deliver(ctx, setupURL, privateKey)
			return deliveryCause
		}, nil
	}
	var logs strings.Builder
	restore := diagnostics.SetOutput(&logs)
	result := migration.CreateOnce(t.Context(), migration.Request{ServerType: "cx22", ServerName: targetName}, opts, 42)
	restore()
	if result.CreationState != "created" || result.Handoff != "failed" || result.Code != "handoff_failed" || creates.Load() != 1 || uploads.Load() != 1 || messages.Load() != 1 {
		t.Fatalf("result=%+v creates=%d messages=%d uploads=%d", result, creates.Load(), messages.Load(), uploads.Load())
	}
	if !errors.Is(deliveryCause, io.EOF) || !errors.Is(deliveryCause, io.ErrUnexpectedEOF) {
		t.Fatalf("missing EOF causes: %v", deliveryCause)
	}
	log := logs.String()
	for _, expected := range []string{"ERROR", "telegram.api", `method="sendDocument"`, `stage="handoff"`, `creation_state="created"`, `code="handoff_failure"`, "unexpected EOF"} {
		if !strings.Contains(log, expected) {
			t.Errorf("missing %q in %s", expected, log)
		}
	}
	if strings.Count(log, "ERROR") != 1 || len(log) > 4096 {
		t.Fatalf("unexpected diagnostics: %s", log)
	}
	for _, secret := range []string{testToken, "private-provider-token", "PRIVATE KEY", server.URL} {
		if strings.Contains(log, secret) {
			t.Errorf("diagnostic leaked %q", secret)
		}
	}
}

func TestMultipartCancellationWithEOFRemainsExpected(t *testing.T) {
	client := doerFunc(func(*http.Request) (*http.Response, error) { return nil, errors.Join(context.Canceled, io.EOF) })
	var logs strings.Builder
	restore := diagnostics.SetOutput(&logs)
	err := sendDocument(t.Context(), client, "https://telegram.synthetic", testToken, 42, "file.txt", []byte("private"))
	restore()
	if !errors.Is(err, context.Canceled) || !errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) || logs.Len() != 0 {
		t.Fatalf("cancellation: %v logs=%s", err, logs.String())
	}
}

func TestNoCapacityOutcomeMakesNoRetryPromise(t *testing.T) {
	message := migrationOutcomeText(migration.OneShotResult{CreationState: "not_created", Code: "no_capacity", ServerName: "example"})
	if strings.Contains(strings.ToLower(message), "retr") || !strings.Contains(message, "No server was created") {
		t.Fatalf("no-capacity notice: %q", message)
	}
}
