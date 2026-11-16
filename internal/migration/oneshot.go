package migration

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"

	"goaster/internal/diagnostics"
)

// OneShotResult contains only information safe to return to the model.
type OneShotResult struct {
	CreationState string `json:"creation_state"`
	Code          string `json:"code,omitempty"`
	Stage         string `json:"stage,omitempty"`
	HTTPStatus    int    `json:"http_status,omitempty"`
	ProviderCode  string `json:"provider_code,omitempty"`
	ServerID      int64  `json:"server_id,omitempty"`
	ServerLabel   string `json:"server_label,omitempty"`
	IP            string `json:"ip,omitempty"`
	Location      string `json:"location,omitempty"`
	Expires       int64  `json:"expires,omitempty"`
	Attempt       string `json:"attempt,omitempty"`
	Handoff       string `json:"handoff,omitempty"`
}

type oneShotResponse struct {
	CreationState string `json:"creation_state"`
	Attempt       string `json:"attempt"`
	ServerLabel   string `json:"server_label"`
	ServerID      int64  `json:"server_id"`
	IP            string `json:"ip"`
	Location      string `json:"location"`
	Expires       int64  `json:"expires"`
	SetupURL      string `json:"setup_url"`
	Error         struct {
		Code         string `json:"code"`
		Stage        string `json:"stage"`
		HTTPStatus   int    `json:"http_status"`
		ProviderCode string `json:"provider_code"`
	} `json:"error"`
}

func safeCreateField(s string) string {
	if len(s) > 128 {
		return ""
	}
	for _, c := range s {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || strings.ContainsRune("-_.:", c)) {
			return ""
		}
	}
	return s
}

// CreateOnce sends one request and never persists or retries secret material.
func CreateOnce(ctx context.Context, request Request, options Options, owner int64) OneShotResult {
	fail := func(state, code string, err error) OneShotResult {
		if err != nil {
			diagnostics.Error("migration.create", err, diagnostics.Field{Key: "creation_state", Value: state}, diagnostics.Field{Key: "code", Value: code})
		}
		return OneShotResult{CreationState: state, Code: code}
	}
	if err := validateMachineRequest(request.ServerType, request.Locations); err != nil {
		return fail("not_created", "invalid_arguments", err)
	}
	options = completeOptions(options)
	if owner <= 0 || options.MigrationRecipient == nil || options.PrepareRecipientHandoff == nil {
		return fail("not_created", "recipient_unavailable", errors.New("paired recipient unavailable"))
	}
	current, err := options.MigrationRecipient()
	if err != nil || current != owner {
		return fail("not_created", "recipient_unavailable", errors.Join(errors.New("paired recipient mismatch"), err))
	}
	deliver, err := options.PrepareRecipientHandoff(owner)
	if err != nil || deliver == nil {
		return fail("not_created", "recipient_unavailable", errors.Join(errors.New("handoff unavailable"), err))
	}
	token, err := options.LoadHetznerToken()
	if err != nil {
		return fail("not_created", "credentials_unavailable", err)
	}
	pub, private, secret, _, err := generateLoginMaterial()
	if err != nil {
		return fail("not_created", "key_generation_failed", err)
	}
	defer clear(private)
	endpoint, err := webEndpoint(options.WebOrigin, "/api/migration/create", options.AllowInsecureHTTP)
	if err != nil {
		return fail("not_created", "configuration_invalid", err)
	}
	payload, err := json.Marshal(struct {
		Token      string   `json:"hcloud_token"`
		ServerType string   `json:"server_type"`
		Locations  []string `json:"locations,omitempty"`
		PublicKey  string   `json:"login_public_key"`
	}{token, request.ServerType, request.Locations, pub})
	if err != nil {
		return fail("not_created", "request_invalid", err)
	}
	defer clear(payload)
	httpRequest, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(payload))
	if err != nil {
		return fail("not_created", "request_invalid", err)
	}
	httpRequest.Header.Set("Content-Type", "application/json")
	httpRequest.Header.Set("Authorization", "Bearer "+secret)
	response, err := options.HTTPClient.Do(httpRequest)
	if err != nil {
		if errors.Is(err, io.EOF) && !errors.Is(err, context.Canceled) {
			err = errors.Join(io.ErrUnexpectedEOF, err)
		}
		return fail("unknown", "response_unavailable", fmt.Errorf("create transport failed: %w", err))
	}
	if response == nil || response.Body == nil {
		return fail("unknown", "response_unavailable", errors.New("empty create response"))
	}
	defer response.Body.Close()
	data, err := io.ReadAll(io.LimitReader(response.Body, 65537))
	if err != nil || len(data) > 65536 {
		return fail("unknown", "response_unusable", errors.Join(errors.New("create response unreadable"), err))
	}
	defer clear(data)
	var decoded oneShotResponse
	if err := decodeExactJSON(data, &decoded); err != nil {
		return fail("unknown", "response_unusable", err)
	}
	result := OneShotResult{CreationState: decoded.CreationState, ServerID: decoded.ServerID, ServerLabel: safeCreateField(decoded.ServerLabel), IP: safeCreateField(decoded.IP), Location: safeCreateField(decoded.Location), Expires: decoded.Expires, Attempt: safeCreateField(decoded.Attempt)}
	// Treat all service-provided labels as untrusted; never return request secrets.
	scrub := func(value string) string {
		if strings.Contains(value, secret) || token != "" && strings.Contains(value, token) || strings.Contains(value, pub) {
			return ""
		}
		return value
	}
	result.ServerLabel = scrub(result.ServerLabel)
	result.IP = scrub(result.IP)
	result.Location = scrub(result.Location)
	result.Attempt = scrub(result.Attempt)
	if net.ParseIP(result.IP) == nil {
		result.IP = ""
	}
	if result.ServerID < 0 {
		result.ServerID = 0
	}
	if result.Expires < 0 {
		result.Expires = 0
	}
	if response.StatusCode != http.StatusCreated {
		if decoded.CreationState != "not_created" && decoded.CreationState != "unknown" && decoded.CreationState != "created" || decoded.Error.HTTPStatus != response.StatusCode || safeCreateField(decoded.Error.Code) == "" || safeCreateField(decoded.Error.Stage) == "" {
			return fail("unknown", "response_unusable", errors.New("invalid create error response"))
		}
		result.Code = diagnostics.MigrationCode(scrub(decoded.Error.Code))
		if scrub(decoded.Error.Code) == "no_capacity" {
			result.Code = "no_capacity"
		}
		result.Stage = diagnostics.MigrationStage(scrub(decoded.Error.Stage))
		result.HTTPStatus = response.StatusCode
		result.ProviderCode = diagnostics.BackendValue(scrub(decoded.Error.ProviderCode))
		diagnostics.Error("migration.create", errors.New("service returned create error"), diagnostics.Field{Key: "status", Value: fmt.Sprint(response.StatusCode)}, diagnostics.Field{Key: "code", Value: result.Code}, diagnostics.Field{Key: "stage", Value: result.Stage}, diagnostics.Field{Key: "creation_state", Value: result.CreationState}, diagnostics.Field{Key: "backend_code", Value: result.ProviderCode})
		return result
	}
	setup, parseErr := url.Parse(decoded.SetupURL)
	origin, originErr := url.Parse(options.WebOrigin)
	if decoded.CreationState != "created" {
		return fail("unknown", "response_unusable", errors.New("invalid create success response"))
	}
	fragmentMatches := false
	if parseErr == nil {
		values, fragmentErr := url.ParseQuery(setup.Fragment)
		fragmentMatches = setup.Fragment == secret
		if fragmentErr == nil && !fragmentMatches {
			matches := 0
			for _, entries := range values {
				for _, entry := range entries {
					if entry == secret {
						matches++
					}
				}
			}
			fragmentMatches = matches == 1
			if owners, present := values["owner"]; present {
				fragmentMatches = fragmentMatches && len(owners) == 1 && owners[0] == secret
			}
		}
	}
	if decoded.ServerID <= 0 || result.Attempt == "" || result.ServerLabel == "" || result.IP == "" || result.Location == "" || result.Expires <= 0 || parseErr != nil || originErr != nil || setup.Scheme != origin.Scheme || setup.Host != origin.Host || setup.User != nil || !fragmentMatches {
		result.Code = "handoff_failed"
		result.Handoff = "failed"
		diagnostics.Error("migration.handoff", errors.New("invalid create success handoff"))
		return result
	}
	current, err = options.MigrationRecipient()
	if err != nil || current != owner {
		result.Code = "handoff_failed"
		result.Handoff = "failed"
		diagnostics.Error("migration.handoff", errors.Join(errors.New("paired recipient changed"), err))
		return result
	}
	handoff := buildHandoff(decoded.SetupURL, decoded.ServerID, decoded.IP, private)
	defer clear(handoff)
	if err := deliver(ctx, handoff); err != nil {
		result.Code = "handoff_failed"
		result.Handoff = "failed"
		if _, logged := err.(interface{ DiagnosticLogged() }); !logged {
			diagnostics.Error("migration.handoff", err)
		}
		return result
	}
	result.Handoff = "delivered"
	return result
}
