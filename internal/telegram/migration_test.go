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
	client := doerFunc(func(request *http.Request) (*http.Response, error) {
		requestPath = request.URL.Path
		var err error
		requestBody, err = io.ReadAll(request.Body)
		if err != nil {
			return nil, err
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     make(http.Header),
			Body:       io.NopCloser(strings.NewReader(`{"ok":true}`)),
		}, nil
	})
	data := []byte("GoAster migration setup: https://web.example/migration#attempt=abc&owner=secret\nprivate key material")
	deliver, err := prepareMigrationHandoffWith(paths, client, "https://telegram.synthetic")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(paths.UserID, []byte("99"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := deliver(t.Context(), data); err != nil {
		t.Fatal(err)
	}
	if requestPath != "/bot"+testToken+"/sendDocument" {
		t.Fatalf("Telegram endpoint = %q", requestPath)
	}
	bodyText := string(requestBody)
	if !strings.Contains(bodyText, "name=\"chat_id\"") || !strings.Contains(bodyText, "42") || !strings.Contains(bodyText, "private key material") {
		t.Fatalf("paired-user document body = %q", bodyText)
	}
	if strings.Contains(bodyText, "\r\n\r\n99\r\n") || strings.Contains(bodyText, "-100") || strings.Contains(bodyText, "model-supplied") {
		t.Fatalf("document delivery leaked a group or model destination: %q", bodyText)
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
	deliver, err := prepareRecipientMigrationHandoffWith(paths, client, "https://telegram.synthetic", 42)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(paths.UserID, []byte("99"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := deliver(t.Context(), []byte("synthetic")); err == nil || calls != 0 {
		t.Fatalf("changed pairing accepted: err=%v calls=%d", err, calls)
	}
	if _, err := prepareRecipientMigrationHandoffWith(paths, client, "https://telegram.synthetic", 42); err == nil {
		t.Fatal("changed pairing accepted for preparation")
	}
}

func TestMigrationDeliveryPreservesLoggedCause(t *testing.T) {
	paths := Paths{Token: filepath.Join(t.TempDir(), "token")}
	if err := writePrivate(paths.Token, []byte(testToken)); err != nil {
		t.Fatal(err)
	}
	cause := errors.New("private-transport-detail")
	client := doerFunc(func(*http.Request) (*http.Response, error) { return nil, cause })
	logs := make(migrationTestLog, 8)
	restore := diagnostics.SetOutput(logs)
	err := sendMigrationHandoffTo(t.Context(), paths, client, "https://telegram.synthetic", 42, []byte("private-handoff"))
	restore()
	close(logs)
	logText := ""
	for line := range logs {
		logText += line
	}
	if !errors.Is(err, cause) {
		t.Fatal("transport cause lost")
	}
	if _, ok := err.(interface{ DiagnosticLogged() }); !ok {
		t.Fatal("missing diagnostic ownership")
	}
	if strings.Count(logText, "ERROR") != 1 || strings.Contains(logText, "private-transport-detail") || strings.Contains(logText, "private-handoff") || strings.Contains(err.Error(), "private-transport-detail") {
		t.Fatalf("unsafe diagnostic: %s", logText)
	}
}

type migrationTestLog chan string

func (log migrationTestLog) Write(data []byte) (int, error) {
	log <- string(data)
	return len(data), nil
}

func TestMigrationTransportEOFThroughCreateOnce(t *testing.T) {
	paths := Paths{Token: filepath.Join(t.TempDir(), "token"), UserID: filepath.Join(t.TempDir(), "user-id")}
	if err := writePrivate(paths.Token, []byte(testToken)); err != nil {
		t.Fatal(err)
	}
	if err := writePrivate(paths.UserID, []byte("42")); err != nil {
		t.Fatal(err)
	}
	var uploads, creates atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/migration/create" {
			creates.Add(1)
			secret := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
			w.WriteHeader(http.StatusCreated)
			fmt.Fprintf(w, `{"creation_state":"created","attempt":"attempt1","server_label":"server1","server_id":42,"ip":"192.0.2.1","location":"fsn1","expires":2000000000,"setup_url":"http://%s/setup#owner=%s"}`, r.Host, secret)
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
	opts.PrepareRecipientHandoff = func(id int64) (func(context.Context, []byte) error, error) {
		deliver, err := prepareRecipientMigrationHandoffWith(paths, server.Client(), server.URL, id)
		if err != nil {
			return nil, err
		}
		return func(ctx context.Context, data []byte) error { deliveryCause = deliver(ctx, data); return deliveryCause }, nil
	}
	var logs strings.Builder
	restore := diagnostics.SetOutput(&logs)
	result := migration.CreateOnce(t.Context(), migration.Request{ServerType: "cx22"}, opts, 42)
	restore()
	if result.CreationState != "created" || result.Handoff != "failed" || result.Code != "handoff_failed" || creates.Load() != 1 || uploads.Load() != 1 {
		t.Fatalf("result=%+v creates=%d uploads=%d", result, creates.Load(), uploads.Load())
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
