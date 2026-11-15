package migration

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"path/filepath"
	"strings"
	"testing"

	"goaster/internal/scheduler"
)

func TestDiagnosticWebEnvelopeOffline(t *testing.T) {
	cases := []struct {
		body, code string
		status     int
	}{
		{`{"version":"v1","error":{"code":"provider_auth_rejected","stage":"provider","http_status":401,"provider_code":"unauthorized"}}`, codeProviderAuthRejected, 401},
		{`{"version":"v1","error":{"code":"provider_dns_failure","stage":"provider","http_status":502,"provider_code":""}}`, "provider_dns_failure", 502},
		{`{"version":"v1","error":{"code":"provider_timeout","stage":"provider","http_status":504,"provider_code":""}}`, "provider_timeout", 504},
		{`{"version":"v1","error":{"code":"provider_tls_failure","stage":"provider","http_status":502,"provider_code":""}}`, "provider_tls_failure", 502},
		{`{"version":"v1","error":{"code":"provider_auth_rejected","stage":"provider","http_status":401,"provider_code":"secret-injected"}}`, codeWebAuthRejected, 401},
		{`{"version":"v1","error":{"code":"provider_auth_rejected","code":"provider_auth_rejected","stage":"provider","http_status":401,"provider_code":""}}`, codeWebAuthRejected, 401},
		{`provider auth rejected token=secret`, codeWebHTTPError, 502},
		{`{"version":"v1","error":{"code":"provider_timeout","stage":"provider","http_status":504}}`, "provider_timeout", 504},
		{`{"version":"v1","error":{"code":"provider_timeout","stage":"provider","http_status":504}}`, codeWebHTTPError, 502},
		{`{"version":"v1","error":{"code":"provider_timeout","stage":"provider","http_status":504,"extra":1}}`, codeWebHTTPError, 504},
		{`{"version":"v1","error":{"code":"provider_timeout","stage":"provider","http_status":504,"provider_code":null}}`, codeWebHTTPError, 504},
	}
	for _, tc := range cases {
		err := decodeWebError([]byte(tc.body), tc.status)
		got := diagnosticForFailure(stageCreate, err, Result{Status: resultManualReview, Diagnostic: Diagnostic{Stage: stageCreate}})
		if got.Code != tc.code || got.CreationState != creationUnknown {
			t.Errorf("body %q: %+v", tc.body, got)
		}
		data, _ := json.Marshal(got)
		if strings.Contains(string(data), "secret") {
			t.Fatalf("private text leaked: %s", data)
		}
	}
}

func TestDiagnosticTransportOffline(t *testing.T) {
	cases := []struct {
		err  error
		code string
	}{
		{context.DeadlineExceeded, codeTimeout},
		{&net.DNSError{Err: "synthetic secret", Name: "private.invalid"}, codeDNSFailure},
		{tls.RecordHeaderError{}, codeTLSFailure},
		{webTransportError{cause: errors.New("token=secret\n\x1b[31m")}, codeTransportFailure},
	}
	for _, tc := range cases {
		d := DiagnosticFor(tc.err, Result{Status: resultManualReview, Diagnostic: Diagnostic{Stage: stageCreate}})
		b, _ := json.Marshal(d)
		if d.Code != tc.code || d.CreationState != creationUnknown || strings.Contains(string(b), "secret") || strings.Contains(string(b), `\\u001b`) {
			t.Errorf("diagnostic: %s", b)
		}
	}
}

func TestScheduledFailureRetainedOffline(t *testing.T) {
	jobs, err := scheduler.New(scheduler.Options{Path: filepath.Join(t.TempDir(), "jobs.json")})
	if err != nil {
		t.Fatal(err)
	}
	fails := false
	options := Options{TempDirectory: t.TempDir(), MigrationRecipient: offlineRecipient, PrepareRecipientHandoff: offlinePrepareRecipient, WebOrigin: "https://example.invalid", LoadHetznerToken: func() (string, error) { return "synthetic", nil }, DeliverHandoff: func(context.Context, []byte) error { return nil }, HTTPClient: offlineDoer(func(*http.Request) (*http.Response, error) {
		if fails {
			return nil, context.DeadlineExceeded
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"version":"v1","server_type":"cx22","locations":[]}`))}, nil
	})}
	result, err := StartMonitor(t.Context(), Request{ServerType: "cx22", JobID: "migration-diagnostic-test"}, options, jobs)
	if err != nil {
		t.Fatal(err)
	}
	job, ok := jobs.Get(result.MonitorID)
	if !ok {
		t.Fatal("missing job")
	}
	fails = true
	if err := ScheduledExecutor(t.Context(), job.Message, jobs, options); err == nil {
		t.Fatal("missing failure")
	}
	status, err := MigrationStatusWithOptions(result.MonitorID, jobs, options)
	if err != nil {
		t.Fatal(err)
	}
	if status.Diagnostic.Code != codeTimeout || status.Diagnostic.Stage != stageAvailability {
		t.Fatalf("status: %+v", status.Diagnostic)
	}
}

func TestPersistedDiagnosticRejectsInjectedText(t *testing.T) {
	d := initialMonitorDiagnostic("migration-diagnostic-test")
	d.Cause = "secret\n\x1b[31m"
	if validatePersistedDiagnostic(d, d.MonitorID) == nil {
		t.Fatal("accepted injected cause")
	}
	d = initialMonitorDiagnostic("migration-diagnostic-test")
	d.NextAction = "secret"
	if validatePersistedDiagnostic(d, d.MonitorID) == nil {
		t.Fatal("accepted injected action")
	}
}
