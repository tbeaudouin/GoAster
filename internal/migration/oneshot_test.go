package migration

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"

	"goaster/internal/diagnostics"
)

const oneShotTargetName = "goaster-migration-0123456789abcdef"

func TestOneShotOffline(t *testing.T) {
	for _, locations := range [][]string{nil, {}, {"fsn1"}} {
		t.Run(fmt.Sprint(locations), func(t *testing.T) {
			calls := 0
			setupURL := ""
			privateKey := []byte{}
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
				wantFields := 4
				if len(locations) > 0 {
					wantFields++
				}
				if len(body) != wantFields || body["hcloud_token"] != "test-token" || body["server_type"] != "cx22" || body["server_name"] != oneShotTargetName || !strings.HasPrefix(fmt.Sprint(body["login_public_key"]), "ssh-ed25519 ") {
					t.Error("wrong contract")
				}
				public = fmt.Sprint(body["login_public_key"])
				if len(locations) > 0 && fmt.Sprint(body["locations"]) != "[fsn1]" {
					t.Error("locations widened")
				}
				w.WriteHeader(201)
				fmt.Fprintf(w, `{"creation_state":"created","attempt":"attempt1","server_label":"%s","server_id":42,"ip":"192.0.2.1","location":"fsn1","expires":2000000000,"setup_url":"http://%s/setup#owner=%s"}`, oneShotTargetName, r.Host, secret)
			}))
			defer server.Close()
			opts := DefaultOptions()
			opts.WebOrigin = server.URL
			opts.AllowInsecureHTTP = true
			opts.HTTPClient = server.Client()
			opts.LoadHetznerToken = func() (string, error) { return "test-token", nil }
			opts.MigrationRecipient = func() (int64, error) { return 7, nil }
			opts.PrepareRecipientHandoffParts = func(id int64) (func(context.Context, string, []byte) error, error) {
				return func(_ context.Context, url string, key []byte) error {
					setupURL = url
					privateKey = append([]byte{}, key...)
					return nil
				}, nil
			}
			result := CreateOnce(context.Background(), Request{ServerType: "cx22", ServerName: oneShotTargetName, Locations: locations}, opts, 7)
			block, _ := pem.Decode(privateKey)
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
			if calls != 1 || result.CreationState != "created" || result.Handoff != "delivered" || !strings.Contains(setupURL, "/setup#owner=") || !strings.Contains(string(privateKey), "PRIVATE KEY") || strings.Contains(string(encoded), "PRIVATE KEY") || strings.Contains(string(encoded), "setup_url") || strings.Contains(string(encoded), setupURL) {
				t.Fatalf("result %s calls %d setup=%q key=%q", encoded, calls, setupURL, privateKey)
			}
		})
	}
}
func TestOneShotOfflineFailures(t *testing.T) {
	calls := 0
	targetExists := false
	lookupFailure := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatal(err)
		}
		if targetExists {
			var fields map[string]any
			if err := json.Unmarshal(body, &fields); err != nil || fields["server_name"] != oneShotTargetName {
				t.Fatalf("named request body = %s", body)
			}
			secret := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
			if lookupFailure {
				w.WriteHeader(http.StatusBadGateway)
				fmt.Fprint(w, `{"creation_state":"not_created","error":{"code":"provider_lookup_failure","stage":"provider","http_status":502}}`)
				return
			}
			w.WriteHeader(http.StatusConflict)
			fmt.Fprintf(w, `{"creation_state":"not_created","server_id":42,"server_label":%q,"setup_url":"https://private.invalid/setup#owner=%s","error":{"code":"target_exists","stage":"provider","http_status":409}}`, oneShotTargetName, secret)
			return
		}
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
	opts.PrepareRecipientHandoffParts = func(int64) (func(context.Context, string, []byte) error, error) {
		return func(context.Context, string, []byte) error { return nil }, nil
	}
	if got := CreateOnce(context.Background(), Request{ServerType: "cx22", ServerName: oneShotTargetName, Locations: []string{"fsn1", "fsn1"}}, opts, 7); got.CreationState != "not_created" || calls != 0 {
		t.Fatal(got, calls)
	}
	if got := CreateOnce(context.Background(), Request{ServerType: "cx22", ServerName: "goaster-migration-0123456789abcde"}, opts, 7); got.CreationState != "not_created" || got.ServerName != "" || calls != 0 {
		t.Fatalf("invalid target name result = %+v calls=%d", got, calls)
	}
	if got := CreateOnce(context.Background(), Request{ServerType: "cax22", ServerName: oneShotTargetName}, opts, 7); got.CreationState != "unknown" || calls != 1 {
		t.Fatal(got, calls)
	}
	targetExists = true
	result := CreateOnce(context.Background(), Request{ServerType: "cx22", ServerName: oneShotTargetName}, opts, 7)
	encoded, _ := json.Marshal(result)
	if result.CreationState != "not_created" || result.Code != "target_exists" || result.Stage != "provider" || result.HTTPStatus != http.StatusConflict || result.ServerID != 42 || result.ServerLabel != oneShotTargetName || result.ServerName != oneShotTargetName || result.Handoff != "" || strings.Contains(string(encoded), "setup_url") || strings.Contains(string(encoded), "private.invalid") {
		t.Fatalf("existing target result = %+v encoded=%s", result, encoded)
	}
	lookupFailure = true
	result = CreateOnce(context.Background(), Request{ServerType: "cx22", ServerName: oneShotTargetName}, opts, 7)
	if result.CreationState != "not_created" || result.Code != "provider_lookup_failure" || result.Stage != "provider" || result.HTTPStatus != http.StatusBadGateway || calls != 3 {
		t.Fatalf("lookup failure result = %+v calls=%d", result, calls)
	}
}

func TestOneShotRejectsInvalidServerNamesBeforeEffects(t *testing.T) {
	tokenCalls, recipientCalls, handoffCalls := 0, 0, 0
	options := Options{
		LoadHetznerToken: func() (string, error) {
			tokenCalls++
			return "", errors.New("token should not be loaded")
		},
		MigrationRecipient: func() (int64, error) {
			recipientCalls++
			return 7, nil
		},
		PrepareRecipientHandoffParts: func(int64) (func(context.Context, string, []byte) error, error) {
			handoffCalls++
			return func(context.Context, string, []byte) error { return nil }, nil
		},
	}
	for _, tc := range []struct {
		name    string
		request Request
	}{
		{name: "omitted", request: Request{ServerType: "cx22"}},
		{name: "empty", request: Request{ServerType: "cx22", ServerName: ""}},
		{name: "whitespace-only", request: Request{ServerType: "cx22", ServerName: "   "}},
		{name: "null decoded as empty", request: Request{ServerType: "cx22", ServerName: ""}},
		{name: "malformed", request: Request{ServerType: "cx22", ServerName: "goaster-migration-0123456789abcde"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			result := CreateOnce(context.Background(), tc.request, options, 7)
			if result.CreationState != "not_created" || result.Code != "invalid_arguments" || result.ServerName != "" {
				t.Fatalf("invalid name result = %+v", result)
			}
		})
	}
	if tokenCalls != 0 || recipientCalls != 0 || handoffCalls != 0 {
		t.Fatalf("invalid names reached effects: token=%d recipient=%d handoff=%d", tokenCalls, recipientCalls, handoffCalls)
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
		{"wrong returned name", "wrong_name", "created", "failed", 1},
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
				serverLabel := oneShotTargetName
				if tc.mode == "wrong_name" {
					serverLabel = "server1"
				}
				w.WriteHeader(201)
				fmt.Fprintf(w, `{"creation_state":"created","attempt":"attempt1","server_label":%q,"server_id":42,"ip":"192.0.2.1","location":"fsn1","expires":2000000000,"setup_url":%q}`, serverLabel, origin+"/setup#owner="+secret)
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
			opts.PrepareRecipientHandoffParts = func(int64) (func(context.Context, string, []byte) error, error) {
				if tc.mode == "prepare" {
					return nil, fmt.Errorf("private failure")
				}
				return func(context.Context, string, []byte) error {
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
			result := CreateOnce(context.Background(), Request{ServerType: "cx22", ServerName: oneShotTargetName}, opts, 7)
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
	opts.PrepareRecipientHandoffParts = func(int64) (func(context.Context, string, []byte) error, error) {
		return func(context.Context, string, []byte) error { return nil }, nil
	}
	var logs strings.Builder
	restore := diagnostics.SetOutput(&logs)
	defer restore()
	result := CreateOnce(context.Background(), Request{ServerType: "cx22", ServerName: oneShotTargetName}, opts, 7)
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

func TestOneShotCredentialFailureDiagnostics(t *testing.T) {
	for _, cause := range []error{ErrHetznerTokenMissing, ErrHetznerTokenInvalid, errors.Join(ErrCredentialStorageUnreadable, syscall.EACCES)} {
		t.Run(cause.Error(), func(t *testing.T) {
			var logs strings.Builder
			restore := diagnostics.SetOutput(&logs)
			defer restore()
			options := Options{
				MigrationRecipient: func() (int64, error) { return 7, nil },
				PrepareRecipientHandoffParts: func(int64) (func(context.Context, string, []byte) error, error) {
					return func(context.Context, string, []byte) error {
						t.Error("credential failure reached delivery")
						return nil
					}, nil
				},
				LoadHetznerToken: func() (string, error) { return "", cause },
				HTTPClient: oneShotDoer(func(*http.Request) (*http.Response, error) {
					t.Error("credential failure reached HTTP")
					return nil, nil
				}),
			}
			result := CreateOnce(t.Context(), Request{ServerType: "cx22", ServerName: oneShotTargetName}, options, 7)
			if result.CreationState != "not_created" || result.Code != "credentials_unavailable" || strings.Count(logs.String(), "ERROR") != 1 || !strings.Contains(logs.String(), `code="credentials_unavailable"`) {
				t.Fatalf("result=%+v log=%s", result, logs.String())
			}
			expected := cause.Error()
			if errors.Is(cause, ErrCredentialStorageUnreadable) {
				expected = ErrCredentialStorageUnreadable.Error()
			}
			if !strings.Contains(logs.String(), expected) || errors.Is(cause, syscall.EACCES) && !strings.Contains(logs.String(), "permission denied") {
				t.Fatalf("safe cause missing: %s", logs.String())
			}
		})
	}
}

type oneShotDoer func(*http.Request) (*http.Response, error)

func (do oneShotDoer) Do(request *http.Request) (*http.Response, error) { return do(request) }

func TestOneShotRejectsAmbiguousResponses(t *testing.T) {
	const valid = `{"creation_state":"not_created","error":{"code":"no_capacity","stage":"provider","http_status":409}}`
	for _, tc := range []struct {
		name, body  string
		code, state string
	}{
		{"no capacity", valid, "no_capacity", "not_created"},
		{"duplicate field", strings.Replace(valid, `"creation_state":"not_created"`, `"creation_state":"unknown","creation_state":"not_created"`, 1), "response_unusable", "unknown"},
		{"unknown field", strings.Replace(valid, `"error":`, `"private":"synthetic-response-secret","error":`, 1), "response_unusable", "unknown"},
		{"case alias", strings.Replace(valid, `"creation_state"`, `"Creation_State"`, 1), "response_unusable", "unknown"},
		{"trailing JSON", valid + ` {}`, "response_unusable", "unknown"},
		{"oversize", strings.Repeat(" ", 65537), "response_unusable", "unknown"},
		{"wrong existing name", `{"creation_state":"not_created","server_id":42,"server_label":"goaster-migration-fedcba9876543210","error":{"code":"target_exists","stage":"provider","http_status":409}}`, "response_unusable", "unknown"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls, deliveries := 0, 0
			var logs strings.Builder
			restore := diagnostics.SetOutput(&logs)
			defer restore()
			options := Options{
				MigrationRecipient: func() (int64, error) { return 7, nil },
				PrepareRecipientHandoffParts: func(int64) (func(context.Context, string, []byte) error, error) {
					return func(context.Context, string, []byte) error { deliveries++; return nil }, nil
				},
				LoadHetznerToken: func() (string, error) { return "synthetic-provider-token", nil },
				HTTPClient: oneShotDoer(func(*http.Request) (*http.Response, error) {
					calls++
					return &http.Response{StatusCode: 409, Body: io.NopCloser(strings.NewReader(tc.body))}, nil
				}),
			}
			result := CreateOnce(t.Context(), Request{ServerType: "cx22", ServerName: oneShotTargetName}, options, 7)
			encoded, _ := json.Marshal(result)
			if calls != 1 || deliveries != 0 || result.Code != tc.code || result.CreationState != tc.state || strings.Count(logs.String(), "ERROR") != 1 {
				t.Fatalf("result=%+v calls=%d deliveries=%d log=%s", result, calls, deliveries, logs.String())
			}
			for _, value := range []string{"synthetic-provider-token", "synthetic-response-secret"} {
				if strings.Contains(logs.String()+string(encoded), value) {
					t.Fatal("private response or credential leaked")
				}
			}
		})
	}
}
