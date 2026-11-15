package migration

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"goaster/internal/scheduler"
)

func TestContractUnavailableRestartCreateOnce(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(t.TempDir(), "jobs.json")
	calls, creates := 0, 0
	var first machineTerms
	options := Options{TempDirectory: dir, WebOrigin: "https://example.invalid", Now: func() time.Time { return time.Unix(1800000000, 0) }, MigrationRecipient: offlineRecipient, PrepareRecipientHandoff: offlinePrepareRecipient, DeliverHandoff: func(context.Context, []byte) error { return nil }, LoadHetznerToken: func() (string, error) { return "synthetic", nil }}
	options.HTTPClient = offlineDoer(func(r *http.Request) (*http.Response, error) {
		data, err := io.ReadAll(r.Body)
		if err != nil {
			return nil, err
		}
		var fields map[string]json.RawMessage
		if json.Unmarshal(data, &fields) != nil || len(fields) != 13 {
			t.Fatal("wrong request fields")
		}
		for _, name := range []string{"operation_id", "operation_capability", "server_type", "locations", "image", "architecture", "enable_ipv4", "enable_ipv6", "networks", "billing_terms", "login_public_key", "owner_secret_digest", "hetzner_token"} {
			if fields[name] == nil {
				t.Fatalf("missing %s", name)
			}
		}
		var payload struct {
			machineTerms
			HetznerToken string `json:"hetzner_token"`
		}
		dec := json.NewDecoder(bytes.NewReader(data))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&payload); err != nil {
			t.Fatal(err)
		}
		if payload.HetznerToken != "synthetic" || payload.Image != "ubuntu-24.04" || payload.Architecture != "x86" || !payload.EnableIPv4 || payload.EnableIPv6 || payload.Networks == nil || len(payload.Networks) != 0 || payload.BillingTerms != "provider-charges-uncapped-v1" {
			t.Fatal("invalid terms")
		}
		if calls == 0 {
			first = payload.machineTerms
		} else if !reflect.DeepEqual(first, payload.machineTerms) {
			t.Fatal("operation bindings changed")
		}
		calls++
		if r.URL.Path == "/migration/v1/machines/availability" {
			locations := "[]"
			if calls > 1 {
				locations = `["fsn1"]`
			}
			return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"version":"v1","server_type":"cx22","locations":` + locations + `}`))}, nil
		}
		if r.URL.Path != "/migration/v1/machines/create" {
			return nil, errors.New("unexpected route")
		}
		creates++
		id := payload.OperationID
		body := `{"version":"v1","state":"created","attempt":"` + id + `","server_label":"goaster-migration-` + id[:16] + `","server_id":123,"ip":"198.51.100.10","owner_url":"https://example.invalid/migration#attempt=` + id + `","expires":1800003600}`
		return &http.Response{StatusCode: 201, Body: io.NopCloser(strings.NewReader(body))}, nil
	})
	jobs, err := scheduler.New(scheduler.Options{Path: path})
	if err != nil {
		t.Fatal(err)
	}
	result, err := StartMonitor(t.Context(), Request{ServerType: "cx22"}, options, jobs)
	if err != nil || result.Status != "monitoring" {
		t.Fatalf("start: %+v %v", result, err)
	}
	jobs, err = scheduler.New(scheduler.Options{Path: path})
	if err != nil {
		t.Fatal(err)
	}
	job, ok := jobs.Get(result.MonitorID)
	if !ok {
		t.Fatal("missing job")
	}
	if err = ScheduledExecutor(t.Context(), job.Message, jobs, options); err != nil {
		t.Fatal(err)
	}
	if creates != 1 || calls != 3 {
		t.Fatalf("calls=%d creates=%d", calls, creates)
	}
	if err = ScheduledExecutor(t.Context(), job.Message, jobs, options); err == nil || creates != 1 {
		t.Fatal("create retried")
	}
	encoded, _ := json.Marshal(result)
	if bytes.Contains(encoded, []byte(first.OperationCapability)) || bytes.Contains(encoded, []byte("PRIVATE KEY")) {
		t.Fatal("secret in result")
	}
}
func TestOperationRejectsChangedBindings(t *testing.T) {
	req := Request{ServerType: "cx22", Locations: []string{"fsn1"}}
	state, err := newOperation(req, 42)
	if err != nil {
		t.Fatal(err)
	}
	if err = validateOperation(state, req, 42); err != nil {
		t.Fatal(err)
	}
	changes := []operationState{state, state, state, state, state, state}
	changes[0].Terms.Image = "other"
	changes[1].Terms.OperationCapability = "bad"
	changes[2].Terms.OwnerSecretDigest = strings.Repeat("0", 64)
	changes[3].Terms.LoginPublicKey = "bad"
	changes[4].Terms.EnableIPv6 = true
	changes[5].Terms.Networks = []string{"private"}
	for _, changed := range changes {
		if validateOperation(changed, req, 42) == nil {
			t.Fatal("changed binding accepted")
		}
	}

	if validateOperation(state, Request{ServerType: "cx22", Locations: []string{"nbg1"}}, 42) == nil || validateOperation(state, req, 43) == nil {
		t.Fatal("changed request or recipient accepted")
	}
}

func TestContractRejectsAmbiguousResponseFields(t *testing.T) {
	var response availabilityResponse
	if err := decodeExactJSON([]byte(`{"version":"v1","server_type":"cx22","locations":[],"locations":["fsn1"]}`), &response); err == nil {
		t.Fatal("duplicate response field accepted")
	}
}
