package migration

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestStatusContractClassification(t *testing.T) {
	for _, tc := range []struct {
		name, body, want string
		httpStatus       int
	}{
		{"stop despite metadata", `{"status":"stop","creation_state":"created","details":{"code":"no_capacity","stage":"provider","message":"private"}}`, "stop", 500},
		{"retry despite metadata", `{"status":"retry_later","creation_state":"created","details":{"code":"target_exists","stage":"provider"}}`, "retry_later", 201},
		{"missing", `{"creation_state":"created"}`, "retry_later", 201},
		{"unknown", `{"status":"other"}`, "retry_later", 200},
		{"malformed", `{"status":`, "retry_later", 200},
		{"invalid handoff", `{"status":"success","creation_state":"not_created"}`, "retry_later", 500},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(tc.httpStatus); fmt.Fprint(w, tc.body) }))
			defer server.Close()
			opts := DefaultOptions()
			opts.WebOrigin = server.URL
			opts.AllowInsecureHTTP = true
			opts.HTTPClient = server.Client()
			opts.LoadHetznerToken = func() (string, error) { return "test-secret", nil }
			opts.MigrationRecipient = func() (int64, error) { return 7, nil }
			opts.PrepareRecipientHandoffParts = func(int64) (func(context.Context, string, []byte) error, error) {
				return func(context.Context, string, []byte) error { t.Error("unexpected handoff"); return nil }, nil
			}
			result := CreateOnce(context.Background(), Request{ServerType: "cx22", ServerName: oneShotTargetName}, opts, 7)
			if result.Status != tc.want {
				t.Fatalf("status %q want %q", result.Status, tc.want)
			}
			if strings.Contains(fmt.Sprint(result), "test-secret") || strings.Contains(fmt.Sprint(result), "private") {
				t.Fatal("private data in result")
			}
		})
	}
}

func TestLocalAuthorizationAndPairingChanges(t *testing.T) {
	for _, tc := range []struct {
		name              string
		first, second     int64
		credentialFailure bool
		wantCalls         int
	}{
		{"mismatched owner", 8, 8, false, 0}, {"local credentials", 7, 7, true, 0}, {"owner changes", 7, 8, false, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls, checks := 0, 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls++; fmt.Fprint(w, `{"status":"retry_later"}`) }))
			defer server.Close()
			opts := DefaultOptions()
			opts.WebOrigin = server.URL
			opts.AllowInsecureHTTP = true
			opts.HTTPClient = server.Client()
			opts.MigrationRecipient = func() (int64, error) {
				checks++
				if checks == 1 {
					return tc.first, nil
				}
				return tc.second, nil
			}
			opts.PrepareRecipientHandoffParts = func(int64) (func(context.Context, string, []byte) error, error) {
				return func(context.Context, string, []byte) error { return nil }, nil
			}
			opts.LoadHetznerToken = func() (string, error) {
				if tc.credentialFailure {
					return "", fmt.Errorf("unavailable")
				}
				return "test", nil
			}
			got := CreateOnce(context.Background(), Request{ServerType: "cx22", ServerName: oneShotTargetName}, opts, 7)
			if !got.LocalFailure || calls != tc.wantCalls {
				t.Fatalf("result=%+v calls=%d", got, calls)
			}
		})
	}
}
