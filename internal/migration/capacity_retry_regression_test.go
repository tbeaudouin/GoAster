package migration

import (
	"context"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"goaster/internal/scheduler"
)

func capacityRetryOptions(t *testing.T, operationDirectory string, now time.Time, client httpDoer) Options {
	t.Helper()
	return Options{
		TempDirectory:           operationDirectory,
		WebOrigin:               "https://example.invalid",
		Now:                     func() time.Time { return now },
		HTTPClient:              client,
		LoadHetznerToken:        func() (string, error) { return "synthetic-token", nil },
		MigrationRecipient:      offlineRecipient,
		PrepareRecipientHandoff: offlinePrepareRecipient,
		DeliverHandoff:          func(context.Context, []byte) error { return nil },
	}
}

func capacityRetryResponse(status int, body string) *http.Response {
	return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader(body))}
}

func TestAvailabilityRetryBudgetPersistsAcrossRestartAndResetsOnSuccess(t *testing.T) {
	now := time.Unix(1900000000, 0).UTC()
	jobsPath := filepath.Join(t.TempDir(), "jobs.json")
	operationDirectory := t.TempDir()
	var mode atomic.Int32
	var availabilityCalls atomic.Int32
	options := capacityRetryOptions(t, operationDirectory, now, offlineDoer(func(request *http.Request) (*http.Response, error) {
		if request.URL.Path != "/migration/v1/machines/availability" {
			return nil, errors.New("unexpected create request")
		}
		availabilityCalls.Add(1)
		if mode.Load() == 0 {
			return capacityRetryResponse(http.StatusServiceUnavailable, `{"version":"v1","error":{"code":"unavailable","stage":"provider","http_status":503,"provider_code":"resource_unavailable"}}`), nil
		}
		return capacityRetryResponse(http.StatusOK, `{"version":"v1","server_type":"cx22","locations":[]}`), nil
	}))
	jobs, err := scheduler.New(scheduler.Options{Path: jobsPath, Now: func() time.Time { return now }})
	if err != nil {
		t.Fatal(err)
	}
	request := Request{ServerType: "cx22", Locations: []string{"fsn1"}, JobID: "migration-capacity-budget"}
	first, err := StartMonitor(t.Context(), request, options, jobs)
	if err != nil || first.Status != resultMonitoring || first.ServerType != request.ServerType || first.MonitorID != request.JobID || first.Diagnostic.RetryCount != 1 || first.Diagnostic.RetryState != retryStatePending || first.Diagnostic.MonitoringState != monitoringRunning || first.Diagnostic.CreationState != creationNotCreated {
		t.Fatalf("initial bounded retry = %+v, %v", first, err)
	}
	if job, ok := jobs.Get(request.JobID); !ok || !job.Enabled || job.NextRun == nil || !job.NextRun.Equal(now.Add(30*time.Minute)) {
		t.Fatalf("initial monitor state = %+v, exists=%v", job, ok)
	}

	jobs, err = scheduler.New(scheduler.Options{Path: jobsPath, Now: func() time.Time { return now }})
	if err != nil {
		t.Fatal(err)
	}
	job, ok := jobs.Get(request.JobID)
	if !ok {
		t.Fatal("monitor missing after scheduler restart")
	}
	if err := ScheduledExecutor(t.Context(), job.Message, jobs, options); !errors.Is(err, scheduler.ErrRetry) {
		t.Fatalf("second bounded retry = %v", err)
	}
	status, err := MigrationStatusWithOptions(request.JobID, jobs, options)
	if err != nil || status.Migration.Diagnostic.RetryCount != 2 || status.Diagnostic.RetryState != retryStatePending || status.MonitorOn != true || status.Diagnostic.MonitoringState != monitoringRunning {
		t.Fatalf("persisted second retry = %+v, %v", status, err)
	}

	mode.Store(1)
	job, _ = jobs.Get(request.JobID)
	if err := ScheduledExecutor(t.Context(), job.Message, jobs, options); !errors.Is(err, scheduler.ErrRetry) {
		t.Fatalf("successful no-capacity rearm = %v", err)
	}
	status, err = MigrationStatusWithOptions(request.JobID, jobs, options)
	if err != nil || status.Migration.Status != resultMonitoring || status.Diagnostic.Code != codeMonitorStarted || status.Diagnostic.RetryCount != 0 || status.Diagnostic.RetryState != retryStateNone || !status.MonitorOn || status.Diagnostic.MonitoringState != monitoringRunning {
		t.Fatalf("reset after successful empty availability = %+v, %v", status, err)
	}
	if job, ok = jobs.Get(request.JobID); !ok || !job.Enabled || job.NextRun == nil || !job.NextRun.Equal(now.Add(30*time.Minute)) {
		t.Fatalf("reset monitor rearm = %+v, exists=%v", job, ok)
	}

	mode.Store(0)
	for expectedCount := 1; expectedCount <= 2; expectedCount++ {
		job, _ = jobs.Get(request.JobID)
		if err := ScheduledExecutor(t.Context(), job.Message, jobs, options); !errors.Is(err, scheduler.ErrRetry) {
			t.Fatalf("retry after reset count=%d = %v", expectedCount, err)
		}
		status, err = MigrationStatusWithOptions(request.JobID, jobs, options)
		if err != nil || status.Diagnostic.RetryCount != expectedCount || status.Diagnostic.RetryState != retryStatePending || !status.MonitorOn {
			t.Fatalf("retry after reset count=%d = %+v, %v", expectedCount, status, err)
		}
	}
	job, _ = jobs.Get(request.JobID)
	if err := ScheduledExecutor(t.Context(), job.Message, jobs, options); !errors.Is(err, scheduler.ErrReviewRequired) {
		t.Fatalf("exhausted availability retry = %v", err)
	}
	status, err = MigrationStatusWithOptions(request.JobID, jobs, options)
	if err != nil || status.Migration.Status != resultManualReview || status.Diagnostic.Code != codeAvailabilityRetryExhausted || status.Diagnostic.Cause != causeForCode(codeAvailabilityRetryExhausted) || status.Diagnostic.RetryCount != availabilityRetryLimit || status.Diagnostic.RetryState != retryStateExhausted || status.Diagnostic.CreationState != creationNotCreated || status.MonitorOn || !status.Diagnostic.MonitoringStopped {
		t.Fatalf("exhausted availability retry state = %+v, %v", status, err)
	}
	if availabilityCalls.Load() != 6 {
		t.Fatalf("availability checks = %d, want initial + retry + reset + two retries + exhaustion", availabilityCalls.Load())
	}
	if job, _ = jobs.Get(request.JobID); job.Enabled || !job.ReviewRequired {
		t.Fatalf("exhausted scheduler job = %+v", job)
	}
	if err := ScheduledExecutor(t.Context(), job.Message, jobs, options); err == nil || availabilityCalls.Load() != 6 {
		t.Fatalf("disabled monitor retried after exhaustion: err=%v calls=%d", err, availabilityCalls.Load())
	}
}

type temporaryAvailabilityNetworkError struct{}

func (temporaryAvailabilityNetworkError) Error() string   { return "temporary network failure" }
func (temporaryAvailabilityNetworkError) Timeout() bool   { return false }
func (temporaryAvailabilityNetworkError) Temporary() bool { return true }

func TestAvailabilityHTTPEnvelopeClassificationRequiresConsistentTransientEnvelope(t *testing.T) {
	cases := []struct {
		name   string
		status int
		body   string
		want   bool
	}{
		{
			name:   "provider timeout",
			status: http.StatusGatewayTimeout,
			body:   `{"version":"v1","error":{"code":"provider_timeout","stage":"provider","http_status":504}}`,
			want:   true,
		},
		{
			name:   "provider dns failure is terminal",
			status: http.StatusBadGateway,
			body:   `{"version":"v1","error":{"code":"provider_dns_failure","stage":"provider","http_status":502,"provider_code":""}}`,
			want:   false,
		},
		{
			name:   "provider tls failure is terminal",
			status: http.StatusBadGateway,
			body:   `{"version":"v1","error":{"code":"provider_tls_failure","stage":"provider","http_status":502,"provider_code":""}}`,
			want:   false,
		},
		{
			name:   "unknown provider transport failure is terminal",
			status: http.StatusBadGateway,
			body:   `{"version":"v1","error":{"code":"provider_transport_failure","stage":"provider","http_status":502,"provider_code":""}}`,
			want:   false,
		},
		{
			name:   "provider resource unavailable",
			status: http.StatusServiceUnavailable,
			body:   `{"version":"v1","error":{"code":"unavailable","stage":"provider","http_status":503,"provider_code":"resource_unavailable"}}`,
			want:   true,
		},
		{
			name:   "service unavailable",
			status: http.StatusInternalServerError,
			body:   `{"version":"v1","error":{"code":"unavailable","stage":"service","http_status":500,"provider_code":""}}`,
			want:   true,
		},
		{
			name:   "provider rate limit",
			status: http.StatusTooManyRequests,
			body:   `{"version":"v1","error":{"code":"rate_limited","stage":"provider","http_status":429,"provider_code":"rate_limit_exceeded"}}`,
			want:   true,
		},
		{
			name:   "service rate limit",
			status: http.StatusTooManyRequests,
			body:   `{"version":"v1","error":{"code":"rate_limited","stage":"service","http_status":429,"provider_code":""}}`,
			want:   true,
		},
		{
			name:   "plain gateway",
			status: http.StatusBadGateway,
			body:   "bad gateway",
			want:   false,
		},
		{
			name:   "status mismatch",
			status: http.StatusServiceUnavailable,
			body:   `{"version":"v1","error":{"code":"unavailable","stage":"provider","http_status":504,"provider_code":"resource_unavailable"}}`,
			want:   false,
		},
		{
			name:   "terminal unauthorized provider cause",
			status: http.StatusServiceUnavailable,
			body:   `{"version":"v1","error":{"code":"unavailable","stage":"provider","http_status":503,"provider_code":"unauthorized"}}`,
			want:   false,
		},
		{
			name:   "terminal forbidden provider cause",
			status: http.StatusServiceUnavailable,
			body:   `{"version":"v1","error":{"code":"unavailable","stage":"provider","http_status":503,"provider_code":"forbidden"}}`,
			want:   false,
		},
		{
			name:   "terminal invalid input provider cause",
			status: http.StatusServiceUnavailable,
			body:   `{"version":"v1","error":{"code":"unavailable","stage":"provider","http_status":503,"provider_code":"invalid_input"}}`,
			want:   false,
		},
		{
			name:   "storage stage",
			status: http.StatusServiceUnavailable,
			body:   `{"version":"v1","error":{"code":"unavailable","stage":"storage","http_status":503,"provider_code":"resource_unavailable"}}`,
			want:   false,
		},
		{
			name:   "create stage",
			status: http.StatusServiceUnavailable,
			body:   `{"version":"v1","error":{"code":"unavailable","stage":"create","http_status":503,"provider_code":"resource_unavailable"}}`,
			want:   false,
		},
		{
			name:   "unknown code",
			status: http.StatusServiceUnavailable,
			body:   `{"version":"v1","error":{"code":"unknown_failure","stage":"service","http_status":503,"provider_code":""}}`,
			want:   false,
		},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			err := decodeWebError([]byte(testCase.body), testCase.status)
			if got := availabilityFailureMayRetry(err); got != testCase.want {
				t.Fatalf("decoded HTTP envelope retry=%v, want %v (%T)", got, testCase.want, err)
			}
		})
	}
	for _, status := range []int{http.StatusTooManyRequests, http.StatusInternalServerError, http.StatusBadGateway, http.StatusServiceUnavailable, http.StatusGatewayTimeout} {
		if availabilityFailureMayRetry(webHTTPError{Status: status}) {
			t.Errorf("status-only HTTP %d was classified as retryable", status)
		}
	}
	for _, status := range []int{http.StatusBadRequest, http.StatusUnauthorized, http.StatusForbidden, http.StatusNotFound} {
		if availabilityFailureMayRetry(webHTTPError{Status: status}) {
			t.Errorf("HTTP %d was classified as retryable", status)
		}
	}
	if !availabilityFailureMayRetry(webTransportError{cause: context.DeadlineExceeded}) {
		t.Fatal("availability timeout was not classified as retryable")
	}
	if !availabilityFailureMayRetry(webTransportError{cause: temporaryAvailabilityNetworkError{}}) {
		t.Fatal("temporary network failure was not classified as retryable")
	}
	for _, failure := range []error{
		context.Canceled,
		errors.New("unknown transport failure"),
		webResponseError{cause: errors.New("invalid response")},
		errors.Join(ErrProtectedStorage, webTransportError{cause: context.DeadlineExceeded}),
		errors.Join(webHTTPError{Status: http.StatusServiceUnavailable, Code: "unavailable", Stage: "provider", ProviderCode: "resource_unavailable"}, providerResponseError{cause: errors.New("invalid provider response")}),
		migrationFailure{diagnostic: diagnosticTemplate(stageStorage, codeProtectedStorage), cause: webTransportError{cause: context.DeadlineExceeded}},
	} {
		if availabilityFailureMayRetry(failure) {
			t.Fatalf("non-provable failure was classified as retryable: %T", failure)
		}
	}
}

func TestAvailabilityTerminalFailuresStopWithoutRetry(t *testing.T) {
	cases := []struct {
		name        string
		configure   func(*Options)
		response    *http.Response
		responseErr error
		code        string
	}{
		{name: "auth", response: capacityRetryResponse(http.StatusUnauthorized, `{"error":"unauthorized"}`), code: codeWebAuthRejected},
		{name: "invalid-request", response: capacityRetryResponse(http.StatusBadRequest, `{"version":"v1","error":{"code":"invalid_request","stage":"request","http_status":400,"provider_code":""}}`), code: "invalid_request"},
		{name: "provider-unauthorized-envelope", response: capacityRetryResponse(http.StatusServiceUnavailable, `{"version":"v1","error":{"code":"unavailable","stage":"provider","http_status":503,"provider_code":"unauthorized"}}`), code: "unavailable"},
		{name: "provider-forbidden-envelope", response: capacityRetryResponse(http.StatusServiceUnavailable, `{"version":"v1","error":{"code":"unavailable","stage":"provider","http_status":503,"provider_code":"forbidden"}}`), code: "unavailable"},
		{name: "provider-invalid-input-envelope", response: capacityRetryResponse(http.StatusServiceUnavailable, `{"version":"v1","error":{"code":"unavailable","stage":"provider","http_status":503,"provider_code":"invalid_input"}}`), code: "unavailable"},
		{name: "permanent-provider-dns-envelope", response: capacityRetryResponse(http.StatusBadGateway, `{"version":"v1","error":{"code":"provider_dns_failure","stage":"provider","http_status":502,"provider_code":""}}`), code: "provider_dns_failure"},
		{name: "provider-certificate-validation-envelope", response: capacityRetryResponse(http.StatusBadGateway, `{"version":"v1","error":{"code":"provider_tls_failure","stage":"provider","http_status":502,"provider_code":""}}`), code: "provider_tls_failure"},
		{name: "unknown-provider-transport-envelope", response: capacityRetryResponse(http.StatusBadGateway, `{"version":"v1","error":{"code":"provider_transport_failure","stage":"provider","http_status":502,"provider_code":""}}`), code: "provider_transport_failure"},
		{name: "storage-envelope", response: capacityRetryResponse(http.StatusServiceUnavailable, `{"version":"v1","error":{"code":"unavailable","stage":"storage","http_status":503,"provider_code":"resource_unavailable"}}`), code: "unavailable"},
		{name: "create-envelope", response: capacityRetryResponse(http.StatusServiceUnavailable, `{"version":"v1","error":{"code":"unavailable","stage":"create","http_status":503,"provider_code":"resource_unavailable"}}`), code: "unavailable"},
		{name: "plain-gateway", response: capacityRetryResponse(http.StatusServiceUnavailable, "gateway unavailable"), code: codeWebHTTPError},
		{name: "invalid-response", response: capacityRetryResponse(http.StatusOK, `{"version":"v1","server_type":"cx22"}`), code: codeInvalidResponse},
		{name: "unknown-transport", responseErr: errors.New("unknown transport failure"), code: codeTransportFailure},
		{name: "invalid-token", configure: func(options *Options) {
			options.LoadHetznerToken = func() (string, error) { return "", ErrHetznerTokenInvalid }
		}, code: codeInvalidToken},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			jobsPath := filepath.Join(t.TempDir(), "jobs.json")
			operationDirectory := t.TempDir()
			var availabilityCalls atomic.Int32
			var createCalls atomic.Int32
			options := capacityRetryOptions(t, operationDirectory, time.Unix(1900000000, 0).UTC(), offlineDoer(func(request *http.Request) (*http.Response, error) {
				switch request.URL.Path {
				case "/migration/v1/machines/availability":
					availabilityCalls.Add(1)
					return testCase.response, testCase.responseErr
				case "/migration/v1/machines/create":
					createCalls.Add(1)
					return capacityRetryResponse(http.StatusInternalServerError, "unexpected create request"), nil
				default:
					return nil, errors.New("unexpected create request")
				}
			}))
			if testCase.configure != nil {
				testCase.configure(&options)
			}
			jobs, err := scheduler.New(scheduler.Options{Path: jobsPath})
			if err != nil {
				t.Fatal(err)
			}
			result, err := StartMonitor(t.Context(), Request{ServerType: "cx22", JobID: "migration-terminal-" + testCase.name}, options, jobs)
			if err == nil || result.Diagnostic.Code != testCase.code || result.Diagnostic.RetryCount != 0 || result.Diagnostic.RetryState != retryStateNone || result.Diagnostic.CreationState != creationNotCreated || result.Diagnostic.MonitoringState != monitoringStopped || !result.Diagnostic.MonitoringStopped {
				t.Fatalf("terminal result = %+v, %v", result, err)
			}
			job, ok := jobs.Get(result.MonitorID)
			if !ok || job.Enabled || !job.ReviewRequired {
				t.Fatalf("terminal job = %+v, exists=%v", job, ok)
			}
			wantAvailabilityCalls := int32(0)
			if testCase.response != nil || testCase.responseErr != nil {
				wantAvailabilityCalls = 1
			}
			if availabilityCalls.Load() != wantAvailabilityCalls || createCalls.Load() != 0 {
				t.Fatalf("terminal check made unexpected calls: availability=%d want=%d create=%d", availabilityCalls.Load(), wantAvailabilityCalls, createCalls.Load())
			}
		})
	}
}

func TestMalformedAvailabilityLocationsStopWithoutRetry(t *testing.T) {
	var availabilityCalls atomic.Int32
	options := capacityRetryOptions(t, t.TempDir(), time.Unix(1900000000, 0).UTC(), offlineDoer(func(request *http.Request) (*http.Response, error) {
		if request.URL.Path != "/migration/v1/machines/availability" {
			return nil, errors.New("unexpected create request")
		}
		if availabilityCalls.Add(1) == 1 {
			return capacityRetryResponse(http.StatusOK, `{"version":"v1","server_type":"cx22","locations":[]}`), nil
		}
		return capacityRetryResponse(http.StatusOK, `{"version":"v1","server_type":"cx22","locations":[{"id":"fsn1"}]}`), nil
	}))
	jobs, err := scheduler.New(scheduler.Options{Path: filepath.Join(t.TempDir(), "jobs.json")})
	if err != nil {
		t.Fatal(err)
	}
	request := Request{ServerType: "cx22", Locations: []string{"fsn1"}, JobID: "migration-malformed-locations"}
	result, err := StartMonitor(t.Context(), request, options, jobs)
	if err != nil || result.Status != resultMonitoring {
		t.Fatalf("initial unavailable monitor = %+v, %v", result, err)
	}
	job, ok := jobs.Get(result.MonitorID)
	if !ok {
		t.Fatal("monitor missing")
	}
	if err := ScheduledExecutor(t.Context(), job.Message, jobs, options); !errors.Is(err, scheduler.ErrReviewRequired) {
		t.Fatalf("malformed location response = %v", err)
	}
	status, err := MigrationStatusWithOptions(result.MonitorID, jobs, options)
	if err != nil || status.Diagnostic.Code != codeInvalidResponse || status.Diagnostic.RetryCount != 0 || status.Diagnostic.RetryState != retryStateNone || status.Diagnostic.CreationState != creationNotCreated || status.MonitorOn || availabilityCalls.Load() != 2 {
		t.Fatalf("malformed location terminal status = %+v, %v calls=%d", status, err, availabilityCalls.Load())
	}
}

func TestAvailabilityIntegrityFailureStopsWithoutRetry(t *testing.T) {
	var availabilityCalls atomic.Int32
	operationDirectory := t.TempDir()
	options := capacityRetryOptions(t, operationDirectory, time.Unix(1900000000, 0).UTC(), offlineDoer(func(request *http.Request) (*http.Response, error) {
		if request.URL.Path != "/migration/v1/machines/availability" {
			return nil, errors.New("unexpected create request")
		}
		availabilityCalls.Add(1)
		return capacityRetryResponse(http.StatusOK, `{"version":"v1","server_type":"cx22","locations":[]}`), nil
	}))
	jobs, err := scheduler.New(scheduler.Options{Path: filepath.Join(t.TempDir(), "jobs.json")})
	if err != nil {
		t.Fatal(err)
	}
	request := Request{ServerType: "cx22", JobID: "migration-integrity-terminal"}
	result, err := StartMonitor(t.Context(), request, options, jobs)
	if err != nil || result.Status != resultMonitoring {
		t.Fatalf("initial no-capacity monitor = %+v, %v", result, err)
	}
	if err := os.WriteFile(filepath.Join(operationDirectory, request.JobID+".json"), []byte("{}"), 0600); err != nil {
		t.Fatal(err)
	}
	job, ok := jobs.Get(request.JobID)
	if !ok {
		t.Fatal("monitor missing")
	}
	if err := ScheduledExecutor(t.Context(), job.Message, jobs, options); !errors.Is(err, scheduler.ErrReviewRequired) {
		t.Fatalf("integrity failure = %v", err)
	}
	status, err := MigrationStatusWithOptions(request.JobID, jobs, options)
	if err != nil || status.Diagnostic.Code != codeProtectedStorage || status.Diagnostic.RetryCount != 0 || status.Diagnostic.RetryState != retryStateNone || status.Diagnostic.CreationState != creationNotCreated || status.MonitorOn || availabilityCalls.Load() != 1 {
		t.Fatalf("integrity terminal status = %+v, %v calls=%d", status, err, availabilityCalls.Load())
	}
}

func TestAvailabilityRetryStopRaceKeepsTruthfulPendingState(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	options := capacityRetryOptions(t, t.TempDir(), time.Unix(1900000000, 0).UTC(), offlineDoer(func(request *http.Request) (*http.Response, error) {
		if request.URL.Path != "/migration/v1/machines/availability" {
			return nil, errors.New("unexpected create request")
		}
		close(started)
		<-release
		return nil, context.DeadlineExceeded
	}))
	jobs, err := scheduler.New(scheduler.Options{Path: filepath.Join(t.TempDir(), "jobs.json")})
	if err != nil {
		t.Fatal(err)
	}
	request := Request{ServerType: "cx22", JobID: "migration-retry-stop-race"}
	type startResult struct {
		result Result
		err    error
	}
	done := make(chan startResult, 1)
	go func() {
		result, startErr := StartMonitor(t.Context(), request, options, jobs)
		done <- startResult{result: result, err: startErr}
	}()
	<-started
	if stopped, stopErr := StopMonitorWithOptions(request.JobID, jobs, options); !errors.Is(stopErr, scheduler.ErrExecutionInProgress) || stopped.Diagnostic.MonitoringState != monitoringRunning {
		t.Fatalf("stop during retryable availability = %+v, %v", stopped, stopErr)
	}
	close(release)
	startedResult := <-done
	if startedResult.err != nil || startedResult.result.Status != resultMonitoring || startedResult.result.Diagnostic.RetryCount != 1 || startedResult.result.Diagnostic.RetryState != retryStatePending || startedResult.result.Diagnostic.MonitoringState != monitoringRunning {
		t.Fatalf("retry race result = %+v, %v", startedResult.result, startedResult.err)
	}
	job, ok := jobs.Get(request.JobID)
	if !ok || !job.Enabled || job.Status == scheduler.StatusRunning {
		t.Fatalf("retry race scheduler state = %+v, exists=%v", job, ok)
	}
}

func TestAvailabilityDoesNotRetryAfterDispatchOrUnknownEvidence(t *testing.T) {
	for _, evidence := range []string{"dispatch", "unknown"} {
		t.Run(evidence, func(t *testing.T) {
			var availabilityCalls atomic.Int32
			options := capacityRetryOptions(t, t.TempDir(), time.Unix(1900000000, 0).UTC(), offlineDoer(func(request *http.Request) (*http.Response, error) {
				if request.URL.Path == "/migration/v1/machines/availability" {
					availabilityCalls.Add(1)
					return capacityRetryResponse(http.StatusOK, `{"version":"v1","server_type":"cx22","locations":[]}`), nil
				}
				return nil, errors.New("unexpected provider call")
			}))
			jobs, err := scheduler.New(scheduler.Options{Path: filepath.Join(t.TempDir(), "jobs.json")})
			if err != nil {
				t.Fatal(err)
			}
			request := Request{ServerType: "cx22", JobID: "migration-no-retry-" + evidence}
			result, err := StartMonitor(t.Context(), request, options, jobs)
			if err != nil || result.Status != resultMonitoring {
				t.Fatalf("initial monitor = %+v, %v", result, err)
			}
			if evidence == "dispatch" {
				if err := markDispatch(options.TempDirectory, result.MonitorID); err != nil {
					t.Fatal(err)
				}
			} else {
				diagnostic := diagnosticTemplate(stageCreate, codeManualReview)
				diagnostic.MonitorID = result.MonitorID
				diagnostic.CreationState = creationUnknown
				diagnostic.MonitoringState = monitoringRunning
				if err := storeMonitorDiagnostic(options, diagnostic); err != nil {
					t.Fatal(err)
				}
			}
			job, ok := jobs.Get(result.MonitorID)
			if !ok {
				t.Fatal("monitor missing")
			}
			if err := ScheduledExecutor(t.Context(), job.Message, jobs, options); !errors.Is(err, scheduler.ErrReviewRequired) {
				t.Fatalf("evidence guard = %v", err)
			}
			status, statusErr := MigrationStatusWithOptions(result.MonitorID, jobs, options)
			if statusErr != nil || status.MonitorOn || status.Diagnostic.CreationState != creationUnknown || availabilityCalls.Load() != 1 {
				t.Fatalf("evidence guard status = %+v, %v calls=%d", status, statusErr, availabilityCalls.Load())
			}
		})
	}
}
