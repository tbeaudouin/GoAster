package migration

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"goaster/internal/diagnostics"
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

func TestHTTP422DiagnosticAndLogAreBoundedOffline(t *testing.T) {
	const monitorID = "migration-http422"
	valid := decodeWebError([]byte(`{"version":"v1","error":{"code":"invalid_request","stage":"request","http_status":422}}`), http.StatusUnprocessableEntity)
	if valid.Code != "invalid_request" || valid.Stage != "request" || valid.Status != http.StatusUnprocessableEntity {
		t.Fatalf("valid HTTP 422 envelope = %+v", valid)
	}
	result, err := failureResult(stageAvailability, valid, Result{Status: resultManualReview, MonitorID: monitorID, Diagnostic: Diagnostic{Stage: stageAvailability}})
	if err == nil || result.Diagnostic.Stage != stageInput || result.Diagnostic.Code != "invalid_request" || result.Diagnostic.HTTPStatus != http.StatusUnprocessableEntity || result.Diagnostic.CreationState != creationNotCreated {
		t.Fatalf("valid HTTP 422 result = %+v, %v", result, err)
	}

	var output bytes.Buffer
	restore := diagnostics.SetOutput(&output)
	defer restore()
	logMigrationFailure("migration.http422", err, result.Diagnostic)
	log := output.String()
	for _, want := range []string{
		`status="422"`,
		`stage="input"`,
		`code="invalid_request"`,
		`creation_state="not_created"`,
		`monitor_id="migration-http422"`,
	} {
		if !strings.Contains(log, want) {
			t.Fatalf("HTTP 422 log = %q, missing %q", log, want)
		}
	}

	malformed := decodeWebError([]byte(`{"version":"v1","error":{"code":"invalid_request","stage":"request","http_status":422,"message":"private response body","provider_code":"invalid_input"}}`), http.StatusUnprocessableEntity)
	if malformed.Code != "" || malformed.Status != http.StatusUnprocessableEntity || malformed.Stage != "" || malformed.ProviderCode != "" {
		t.Fatalf("malformed HTTP 422 envelope was trusted = %+v", malformed)
	}
	malformedDiagnostic := DiagnosticFor(malformed, Result{Status: resultManualReview, MonitorID: monitorID, Diagnostic: Diagnostic{Stage: stageAvailability}})
	encoded, marshalErr := json.Marshal(malformedDiagnostic)
	if marshalErr != nil || strings.Contains(string(encoded), "private response body") {
		t.Fatalf("malformed HTTP 422 diagnostic leaked response data: %s (%v)", encoded, marshalErr)
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

func TestPersistedDiagnosticAcceptsLegacyChangedNextActions(t *testing.T) {
	cases := []struct {
		code   string
		legacy string
	}{
		{codeRetryConfirmation, legacyRetryConfirmationNextAction},
		{codeHandoffFailure, legacyHandoffFailureNextAction},
		{codeCreatedDeliveryFailed, legacyCreatedDeliveryFailedAction},
		{codeTimeout, legacyServiceNextAction},
		{codeTransportFailure, legacyServiceNextAction},
		{codeInvalidResponse, legacyInvalidResponseNextAction},
		{codeManualReview, legacyManualReviewNextAction},
		{"provider_timeout", legacyManualReviewNextAction},
	}
	for _, tc := range cases {
		diagnostic := diagnosticTemplate(stageMonitor, tc.code)
		diagnostic.MonitorID = "migration-legacy-guidance"
		diagnostic = NormalizeResult(Result{Status: diagnostic.ResultState, MonitorID: diagnostic.MonitorID, Diagnostic: diagnostic}).Diagnostic
		diagnostic.NextAction = tc.legacy
		if err := validatePersistedDiagnostic(diagnostic, diagnostic.MonitorID); err != nil {
			t.Fatalf("legacy %s action rejected: %v", tc.code, err)
		}
		current := NormalizeResult(Result{Status: diagnostic.ResultState, MonitorID: diagnostic.MonitorID, Diagnostic: diagnostic}).Diagnostic
		if current.NextAction != nextActionForCode(tc.code) {
			t.Fatalf("legacy %s action was not normalized: %q", tc.code, current.NextAction)
		}
	}
}

func TestLegacyDiagnosticLoadAndRetryValidation(t *testing.T) {
	options := Options{TempDirectory: t.TempDir()}
	dir, err := operationDirectory(options)
	if err != nil {
		t.Fatal(err)
	}
	d := diagnosticTemplate(stageCreate, codeTimeout)
	d.MonitorID = "migration-legacy-load"
	d.NextAction = legacyServiceNextAction
	record := monitorDiagnosticRecord{Version: monitorDiagnosticVersion, MonitorID: d.MonitorID, Sequence: 1, Diagnostic: d}
	path := filepath.Join(dir, fmt.Sprintf("%s%s%s.%020d%s", d.MonitorID, diagnosticFileSeparator, d.Code, 1, diagnosticFileSuffix))
	write := func() {
		data, err := json.Marshal(record)
		if err != nil {
			t.Fatal(err)
		}
		if err = os.WriteFile(path, data, 0600); err != nil {
			t.Fatal(err)
		}
	}
	write()
	got, found, err := loadMonitorDiagnostic(options, d.MonitorID)
	if err != nil || !found || got.NextAction != uncertainNextAction {
		t.Fatalf("loaded diagnostic: %+v %v %v", got, found, err)
	}
	record.Diagnostic.NextAction += " injected"
	write()
	if _, _, err = loadMonitorDiagnostic(options, d.MonitorID); err == nil {
		t.Fatal("accepted modified legacy action")
	}
	record.Diagnostic.NextAction = legacyServiceNextAction
	record.Diagnostic.RetryState = retryStatePending
	record.Diagnostic.RetryCount = 1
	write()
	if _, _, err = loadMonitorDiagnostic(options, d.MonitorID); err == nil {
		t.Fatal("accepted legacy action for pending retry")
	}
	record.Diagnostic.NextAction = retryPendingNextAction
	record.Diagnostic.RetryCount = -1
	write()
	if _, _, err = loadMonitorDiagnostic(options, d.MonitorID); err == nil {
		t.Fatal("accepted invalid retry count")
	}
}

func TestUncertainNextActionExact(t *testing.T) {
	expected := "Status and provider account checks are optional. If creation is unknown or already created, say: The previous attempt may already have created a server. Trying again could create a duplicate and add charges. I recommend checking your Hetzner account for duplicate servers. Are you sure you want me to try again? After one yes, start exactly one fresh attempt using the exact prior monitor reference from the current retry-required result; never retry automatically. Respect retained-history, in-flight, and unconfirmed-stop blockers."
	for _, code := range []string{codeTimeout, codeTransportFailure, codeInvalidResponse, codeManualReview, "provider_timeout"} {
		if got := nextActionForCode(code); got != expected {
			t.Errorf("%s: %q", code, got)
		}
	}
}
