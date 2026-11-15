package migration

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestArchitectureForServerTypeOnlyClassifiesNumericCAXFamily(t *testing.T) {
	for _, test := range []struct {
		serverType string
		want       string
	}{
		{serverType: "cx22", want: machineArchitectureX86},
		{serverType: "cax11", want: machineArchitectureArm},
		{serverType: "cax51", want: machineArchitectureArm},
		{serverType: "cax", want: machineArchitectureX86},
		{serverType: "cax11-extra", want: machineArchitectureX86},
		{serverType: "CAX11", want: machineArchitectureX86},
	} {
		t.Run(test.serverType, func(t *testing.T) {
			if got := architectureForServerType(test.serverType); got != test.want {
				t.Fatalf("architectureForServerType(%q) = %q, want %q", test.serverType, got, test.want)
			}
		})
	}
}

func TestMachineContractUsesDerivedArchitectureForCXAndCAX(t *testing.T) {
	for _, test := range []struct {
		name                 string
		serverType           string
		locations            []string
		wantArchitecture     string
		wantLocationPolicy   string
		wantResponseLocation []string
	}{
		{name: "cx any location", serverType: "cx22", wantArchitecture: machineArchitectureX86, wantLocationPolicy: LocationPolicyAnyEligible, wantResponseLocation: []string{"fsn1"}},
		{name: "cx strict allowlist", serverType: "cx22", locations: []string{"fsn1"}, wantArchitecture: machineArchitectureX86, wantLocationPolicy: LocationPolicyAllowlist, wantResponseLocation: []string{"fsn1"}},
		{name: "cax any location", serverType: "cax11", wantArchitecture: machineArchitectureArm, wantLocationPolicy: LocationPolicyAnyEligible, wantResponseLocation: []string{"fsn1"}},
		{name: "cax strict allowlist", serverType: "cax11", locations: []string{"fsn1"}, wantArchitecture: machineArchitectureArm, wantLocationPolicy: LocationPolicyAllowlist, wantResponseLocation: []string{"fsn1"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			options := Options{
				TempDirectory:    t.TempDir(),
				WebOrigin:        "https://example.invalid",
				LoadHetznerToken: func() (string, error) { return "synthetic", nil },
			}
			options.HTTPClient = offlineDoer(func(request *http.Request) (*http.Response, error) {
				body, err := io.ReadAll(request.Body)
				if err != nil {
					return nil, err
				}
				var payload struct {
					ServerType   string   `json:"server_type"`
					Locations    []string `json:"locations"`
					Architecture string   `json:"architecture"`
					Token        string   `json:"hetzner_token"`
				}
				if err := json.Unmarshal(body, &payload); err != nil {
					return nil, err
				}
				if payload.ServerType != test.serverType || !reflect.DeepEqual(payload.Locations, append([]string{}, test.locations...)) || payload.Architecture != test.wantArchitecture || payload.Token != "synthetic" {
					t.Fatalf("migration availability payload = %+v", payload)
				}
				response, err := json.Marshal(struct {
					Version    string   `json:"version"`
					ServerType string   `json:"server_type"`
					Locations  []string `json:"locations"`
				}{Version: "v1", ServerType: test.serverType, Locations: test.wantResponseLocation})
				if err != nil {
					return nil, err
				}
				return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(string(response)))}, nil
			})

			result, err := Availability(t.Context(), PriceRequest{ServerType: test.serverType, Locations: test.locations}, options)
			if err != nil {
				t.Fatal(err)
			}
			if result.ServerType != test.serverType || result.LocationPolicy != test.wantLocationPolicy || !reflect.DeepEqual(result.RequestedLocations, test.locations) || !reflect.DeepEqual(result.Locations, test.wantResponseLocation) {
				t.Fatalf("availability result = %+v", result)
			}
		})
	}
}

func TestPersistedOperationRequiresDerivedArchitectureForCXAndCAX(t *testing.T) {
	for _, test := range []struct {
		name             string
		serverType       string
		locations        []string
		wantArchitecture string
		badArchitecture  string
	}{
		{name: "cx any location", serverType: "cx22", wantArchitecture: machineArchitectureX86, badArchitecture: machineArchitectureArm},
		{name: "cx strict allowlist", serverType: "cx22", locations: []string{"fsn1"}, wantArchitecture: machineArchitectureX86, badArchitecture: machineArchitectureArm},
		{name: "cax any location", serverType: "cax11", wantArchitecture: machineArchitectureArm, badArchitecture: machineArchitectureX86},
		{name: "cax strict allowlist", serverType: "cax11", locations: []string{"fsn1"}, wantArchitecture: machineArchitectureArm, badArchitecture: machineArchitectureX86},
	} {
		t.Run(test.name, func(t *testing.T) {
			request := Request{ServerType: test.serverType, Locations: test.locations}
			state, err := newOperation(request, 42)
			if err != nil {
				t.Fatal(err)
			}
			if state.Terms.Architecture != test.wantArchitecture {
				t.Fatalf("new operation architecture = %q, want %q", state.Terms.Architecture, test.wantArchitecture)
			}
			if err := validateOperation(state, request, 42); err != nil {
				t.Fatalf("derived operation was rejected: %v", err)
			}

			goodDirectory := t.TempDir()
			if err := storeOperation(goodDirectory, "migration-good", state); err != nil {
				t.Fatal(err)
			}
			if _, err := loadOperation(goodDirectory, "migration-good", request, 42); err != nil {
				t.Fatalf("persisted derived operation was rejected: %v", err)
			}

			state.Terms.Architecture = test.badArchitecture
			badDirectory := t.TempDir()
			if err := storeOperation(badDirectory, "migration-mismatched", state); err != nil {
				t.Fatal(err)
			}
			if _, err := loadOperation(badDirectory, "migration-mismatched", request, 42); !errors.Is(err, errInvalidRequest) {
				t.Fatalf("mismatched persisted architecture error = %v, want %v", err, errInvalidRequest)
			}

			persisted, err := os.ReadFile(filepath.Join(badDirectory, "migration-mismatched.json"))
			if err != nil {
				t.Fatal(err)
			}
			var stored operationState
			if err := json.Unmarshal(persisted, &stored); err != nil {
				t.Fatal(err)
			}
			if stored.Terms.Architecture != test.badArchitecture {
				t.Fatalf("mismatched persisted operation was rewritten to %q", stored.Terms.Architecture)
			}
		})
	}
}
