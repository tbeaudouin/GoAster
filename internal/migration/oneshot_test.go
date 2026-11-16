package migration

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"goaster/internal/diagnostics"
)

func TestOneShotOffline(t *testing.T) {
	for _, locations := range [][]string{nil, {}, {"fsn1"}} {
		t.Run(fmt.Sprint(locations), func(t *testing.T) {
			calls := 0
			delivered := ""
			public := ""
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				if r.URL.Path != "/api/migration/create" || r.Method != "POST" {
					t.Error("wrong request")
				}
				secret := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
				if len(secret) != 43 {
					t.Error("invalid secret")
				}
				var body map[string]any
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Fatal(err)
				}
				if len(body) != 3 && len(body) != 4 || body["hcloud_token"] != "test-token" || body["server_type"] != "cx22" || !strings.HasPrefix(fmt.Sprint(body["login_public_key"]), "ssh-ed25519 ") {
					t.Error("wrong contract")
				}
				public = fmt.Sprint(body["login_public_key"])
				if len(locations) > 0 && fmt.Sprint(body["locations"]) != "[fsn1]" {
					t.Error("locations widened")
				}
				w.WriteHeader(201)
				fmt.Fprintf(w, `{"creation_state":"created","attempt":"attempt1","server_label":"server1","server_id":42,"ip":"192.0.2.1","location":"fsn1","expires":2000000000,"setup_url":"http://%s/setup#owner=%s"}`, r.Host, secret)
			}))
			defer server.Close()
			opts := DefaultOptions()
			opts.WebOrigin = server.URL
			opts.AllowInsecureHTTP = true
			opts.HTTPClient = server.Client()
			opts.LoadHetznerToken = func() (string, error) { return "test-token", nil }
			opts.MigrationRecipient = func() (int64, error) { return 7, nil }
			opts.PrepareRecipientHandoff = func(id int64) (func(context.Context, []byte) error, error) {
				return func(_ context.Context, b []byte) error { delivered = string(b); return nil }, nil
			}
			result := CreateOnce(context.Background(), Request{ServerType: "cx22", Locations: locations}, opts, 7)
			block, _ := pem.Decode([]byte(delivered[strings.Index(delivered, "-----BEGIN"):]))
			if block == nil {
				t.Fatal("missing private key")
			}
			key, err := x509.ParsePKCS8PrivateKey(block.Bytes)
			if err != nil {
				t.Fatal(err)
			}
			wire, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(public, "ssh-ed25519 "))
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(wire[len(wire)-ed25519.PublicKeySize:], key.(ed25519.PrivateKey).Public().(ed25519.PublicKey)) {
				t.Fatal("handoff key does not match request")
			}
			encoded, _ := json.Marshal(result)
			if calls != 1 || result.CreationState != "created" || result.Handoff != "delivered" || !strings.Contains(delivered, "PRIVATE KEY") || strings.Contains(string(encoded), "PRIVATE KEY") || strings.Contains(string(encoded), "setup_url") {
				t.Fatalf("result %s calls %d", encoded, calls)
			}
		})
	}
}
func TestOneShotOfflineFailures(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		io.Copy(io.Discard, r.Body)
		w.WriteHeader(503)
		fmt.Fprint(w, `{"creation_state":"unknown","error":{"code":"unavailable","stage":"provider","http_status":503}}`)
	}))
	defer server.Close()
	opts := DefaultOptions()
	opts.WebOrigin = server.URL
	opts.AllowInsecureHTTP = true
	opts.HTTPClient = server.Client()
	opts.LoadHetznerToken = func() (string, error) { return "token", nil }
	opts.MigrationRecipient = func() (int64, error) { return 7, nil }
	opts.PrepareRecipientHandoff = func(int64) (func(context.Context, []byte) error, error) {
		return func(context.Context, []byte) error { return nil }, nil
	}
	if got := CreateOnce(context.Background(), Request{ServerType: "cx22", Locations: []string{"fsn1", "fsn1"}}, opts, 7); got.CreationState != "not_created" || calls != 0 {
		t.Fatal(got, calls)
	}
	if got := CreateOnce(context.Background(), Request{ServerType: "cax22"}, opts, 7); got.CreationState != "unknown" || calls != 1 {
		t.Fatal(got, calls)
	}
}

func TestOneShotBoundaries(t *testing.T) {
	for _, tc := range []struct {
		name    string
		mode    string
		state   string
		handoff string
		calls   int
	}{
		{"owner initially changed", "owner_initial", "not_created", "", 0},
		{"handoff unavailable", "prepare", "not_created", "", 0},
		{"owner changed after creation", "owner_changed", "created", "failed", 1},
		{"delivery failure", "delivery", "created", "failed", 1},
		{"delivery already logged", "delivery_logged", "created", "failed", 1},
		{"wrong secret", "wrong_secret", "created", "failed", 1},
		{"wrong origin", "wrong_origin", "created", "failed", 1},
		{"lost response", "lost", "unknown", "", 1},
		{"redirect", "redirect", "unknown", "", 1},
		{"created error", "created_error", "created", "", 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var calls atomic.Int32
			recipients, deliveries := 0, 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				secret := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
				switch tc.mode {
				case "lost":
					h, _, _ := w.(http.Hijacker).Hijack()
					h.Close()
					return
				case "redirect":
					http.Redirect(w, r, "/other", http.StatusTemporaryRedirect)
					return
				case "created_error":
					w.WriteHeader(500)
					fmt.Fprintf(w, `{"creation_state":"created","server_id":42,"error":{"code":"unavailable","stage":"handoff","provider_code":"rate_limit_exceeded","http_status":500}}`)
					return
				}
				origin := "http://" + r.Host
				if tc.mode == "wrong_origin" {
					origin = "http://example.invalid"
				}
				if tc.mode == "wrong_secret" {
					secret += "extra"
				}
				w.WriteHeader(201)
				fmt.Fprintf(w, `{"creation_state":"created","attempt":"attempt1","server_label":"server1","server_id":42,"ip":"192.0.2.1","location":"fsn1","expires":2000000000,"setup_url":%q}`, origin+"/setup#owner="+secret)
			}))
			defer server.Close()
			opts := DefaultOptions()
			opts.WebOrigin = server.URL
			opts.AllowInsecureHTTP = true
			opts.LoadHetznerToken = func() (string, error) { return "test-token", nil }
			opts.MigrationRecipient = func() (int64, error) {
				recipients++
				if tc.mode == "owner_initial" || tc.mode == "owner_changed" && recipients > 1 {
					return 8, nil
				}
				return 7, nil
			}
			opts.PrepareRecipientHandoff = func(int64) (func(context.Context, []byte) error, error) {
				if tc.mode == "prepare" {
					return nil, fmt.Errorf("private failure")
				}
				return func(context.Context, []byte) error {
					deliveries++
					if tc.mode == "delivery" {
						return fmt.Errorf("delivery failure")
					}
					if tc.mode == "delivery_logged" {
						return loggedDeliveryFailure{}
					}
					return nil
				}, nil
			}
			logs := make(testLog, 8)
			restore := diagnostics.SetOutput(logs)
			result := CreateOnce(context.Background(), Request{ServerType: "cx22"}, opts, 7)
			restore()
			logText := logs.text()
			if tc.mode == "lost" && (strings.Count(logText, "ERROR") != 1 || !strings.Contains(logText, "unexpected EOF") || !strings.Contains(logText, `creation_state="unknown"`) || !strings.Contains(logText, `code="response_unavailable"`)) {
				t.Fatalf("lost response diagnostic: %s", logText)
			}
			if tc.mode == "delivery_logged" && logText != "" {
				t.Fatalf("duplicate diagnostic: %s", logText)
			}
			if tc.mode == "created_error" {
				for _, field := range []string{`status="500"`, `stage="handoff"`, `code="unavailable"`, `creation_state="created"`, `backend_code="rate_limit_exceeded"`} {
					if !strings.Contains(logText, field) {
						t.Fatalf("missing %s in %s", field, logText)
					}
				}
			}
			if result.CreationState != tc.state || result.Handoff != tc.handoff || int(calls.Load()) != tc.calls {
				t.Fatalf("result=%+v calls=%d", result, calls.Load())
			}
			if tc.handoff == "failed" && (result.ServerID != 42 || tc.mode != "delivery" && tc.mode != "delivery_logged" && deliveries != 0) {
				t.Fatalf("unsafe handoff: %+v deliveries=%d", result, deliveries)
			}
		})
	}
}

func TestOneShotRemoteFieldsRedacted(t *testing.T) {
	var secret string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		secret = strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		w.WriteHeader(500)
		fmt.Fprintf(w, `{"creation_state":"created","server_id":42,"attempt":%q,"server_label":%q,"ip":%q,"location":%q,"error":{"code":%q,"stage":%q,"provider_code":%q,"http_status":500}}`, secret, "test-token", secret, secret, secret, secret, secret)
	}))
	defer server.Close()
	opts := DefaultOptions()
	opts.WebOrigin = server.URL
	opts.AllowInsecureHTTP = true
	opts.LoadHetznerToken = func() (string, error) { return "test-token", nil }
	opts.MigrationRecipient = func() (int64, error) { return 7, nil }
	opts.PrepareRecipientHandoff = func(int64) (func(context.Context, []byte) error, error) {
		return func(context.Context, []byte) error { return nil }, nil
	}
	var logs strings.Builder
	restore := diagnostics.SetOutput(&logs)
	defer restore()
	result := CreateOnce(context.Background(), Request{ServerType: "cx22"}, opts, 7)
	data, _ := json.Marshal(result)
	for _, value := range []string{secret, "test-token", server.URL} {
		if strings.Contains(string(data), value) || strings.Contains(logs.String(), value) {
			t.Fatalf("secret leaked in %s / %s", data, logs.String())
		}
	}
	if result.CreationState != "created" || result.ServerID != 42 || !strings.Contains(logs.String(), "ERROR") {
		t.Fatalf("%+v %s", result, logs.String())
	}
}

type testLog chan string

func (log testLog) Write(data []byte) (int, error) { log <- string(data); return len(data), nil }
func (log testLog) text() string {
	close(log)
	var result string
	for line := range log {
		result += line
	}
	return result
}

type loggedDeliveryFailure struct{}

func (loggedDeliveryFailure) Error() string     { return "delivery failed" }
func (loggedDeliveryFailure) DiagnosticLogged() {}
