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
	LocalFailure    bool            `json:"-"`
	Status          string          `json:"status"`
	CreationState   string          `json:"creation_state"`
	Code            string          `json:"code,omitempty"`
	Stage           string          `json:"stage,omitempty"`
	HTTPStatus      int             `json:"http_status,omitempty"`
	ProviderCode    string          `json:"provider_code,omitempty"`
	ProviderMessage string          `json:"provider_message,omitempty"`
	ProviderFields  []ProviderField `json:"provider_fields,omitempty"`
	ServerName      string          `json:"server_name,omitempty"`
	ServerID        int64           `json:"server_id,omitempty"`
	ServerLabel     string          `json:"server_label,omitempty"`
	IP              string          `json:"ip,omitempty"`
	Location        string          `json:"location,omitempty"`
	Expires         int64           `json:"expires,omitempty"`
	Attempt         string          `json:"attempt,omitempty"`
	Handoff         string          `json:"handoff,omitempty"`
}

// ProviderField is the bounded, model-safe form of one provider validation
// field. Only fixed allowlisted field names and messages survive parsing.
type ProviderField struct {
	Field    string   `json:"field"`
	Messages []string `json:"messages"`
}

type oneShotResponse struct {
	Status  string `json:"status"`
	Details struct {
		Code    string `json:"code"`
		Stage   string `json:"stage"`
		Message string `json:"message"`
	} `json:"details"`
	CreationState string `json:"creation_state"`
	Attempt       string `json:"attempt"`
	ServerLabel   string `json:"server_label"`
	ServerID      int64  `json:"server_id"`
	IP            string `json:"ip"`
	Location      string `json:"location"`
	Expires       int64  `json:"expires"`
	SetupURL      string `json:"setup_url"`
	Error         struct {
		Code            string          `json:"code"`
		Stage           string          `json:"stage"`
		HTTPStatus      int             `json:"http_status"`
		ProviderCode    string          `json:"provider_code"`
		ProviderMessage string          `json:"provider_message"`
		ProviderFields  []ProviderField `json:"provider_fields"`
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

const (
	maxProviderFields        = 8
	maxProviderFieldMessages = 8
)

func safeProviderFields(fields []ProviderField) []ProviderField {
	if len(fields) > maxProviderFields {
		fields = fields[:maxProviderFields]
	}
	safe := make([]ProviderField, 0, len(fields))
	seen := make(map[string]struct{}, len(fields))
	for _, field := range fields {
		if len(field.Messages) > maxProviderFieldMessages {
			continue
		}
		detail := diagnostics.MigrationProviderFieldValue(field.Field, field.Messages)
		if detail == "" {
			continue
		}
		if _, exists := seen[detail]; exists {
			continue
		}
		seen[detail] = struct{}{}
		parts := strings.SplitN(detail, "=", 2)
		messages := strings.Split(parts[1], "|")
		safe = append(safe, ProviderField{Field: parts[0], Messages: messages})
	}
	return safe
}

func scrubProviderFields(fields []ProviderField, secrets ...string) []ProviderField {
	clean := make([]ProviderField, 0, len(fields))
	for _, field := range fields {
		private := false
		for _, secret := range secrets {
			if secret != "" && (strings.Contains(field.Field, secret) || anyStringContains(field.Messages, secret)) {
				private = true
				break
			}
		}
		if !private {
			clean = append(clean, field)
		}
	}
	return clean
}

func anyStringContains(values []string, needle string) bool {
	for _, value := range values {
		if strings.Contains(value, needle) {
			return true
		}
	}
	return false
}

func normalizeOneShotCreationState(result OneShotResult) string {
	state := diagnostics.MigrationCreationState(result.CreationState)
	if state == "" {
		return "unknown"
	}
	return state
}

// SanitizeOneShotResult applies the same fixed bounds and allowlists to every
// model-facing result, including results supplied by test or runtime seams.
// It never copies arbitrary provider text or private handoff material.
func SanitizeOneShotResult(result OneShotResult) OneShotResult {
	switch result.Status {
	case "success", "stop":
	default:
		result.Status = "retry_later"
	}
	result.Code = diagnostics.MigrationCode(result.Code)
	result.Stage = diagnostics.MigrationStage(result.Stage)
	if result.HTTPStatus < 100 || result.HTTPStatus > 599 {
		result.HTTPStatus = 0
	}
	result.ProviderCode = diagnostics.BackendValue(result.ProviderCode)
	result.ProviderMessage = diagnostics.MigrationProviderMessage(result.ProviderMessage)
	result.ProviderFields = safeProviderFields(result.ProviderFields)
	result.CreationState = normalizeOneShotCreationState(result)
	if !ValidMigrationServerName(result.ServerName) {
		result.ServerName = ""
	}
	result.ServerLabel = safeCreateField(result.ServerLabel)
	result.IP = safeCreateField(result.IP)
	if net.ParseIP(result.IP) == nil {
		result.IP = ""
	}
	result.Location = safeCreateField(result.Location)
	result.Attempt = safeCreateField(result.Attempt)
	if result.ServerID < 0 {
		result.ServerID = 0
	}
	if result.Expires < 0 || result.Expires > 4102444800 {
		result.Expires = 0
	}
	switch result.Handoff {
	case "delivered", "failed":
	default:
		result.Handoff = ""
	}
	if result.Status == "success" && result.Handoff != "delivered" {
		result.Status = "retry_later"
	}
	return result
}

func unusableOneShotResponse(result OneShotResult, status int, err error) OneShotResult {
	result.Status = "retry_later"
	result.HTTPStatus = status
	result = SanitizeOneShotResult(result)
	if result.CreationState != "created" {
		result.CreationState = "unknown"
	}
	result.Code = "response_unusable"
	result.Handoff = ""
	fields := []diagnostics.Field{
		{Key: "status", Value: fmt.Sprint(status)},
		{Key: "code", Value: result.Code},
		{Key: "stage", Value: result.Stage},
		{Key: "creation_state", Value: result.CreationState},
	}
	diagnostics.Error("migration.create", err, fields...)
	return result
}

// creationEvidenceFromTopLevel extracts only fields whose meaning can be
// validated without trusting the error envelope. It intentionally recognizes
// only an exact created state and a request-matching server label; all other
// raw response values stay discarded.
func creationEvidenceFromTopLevel(fields map[string]json.RawMessage, request Request, safeServerName string) OneShotResult {
	result := OneShotResult{ServerName: safeServerName}
	if raw, ok := fields["creation_state"]; ok {
		var state string
		if json.Unmarshal(raw, &state) == nil && state == "created" {
			result.CreationState = state
		}
	}
	if raw, ok := fields["server_id"]; ok {
		var serverID int64
		if json.Unmarshal(raw, &serverID) == nil && serverID > 0 {
			result.ServerID = serverID
		}
	}
	if raw, ok := fields["server_label"]; ok {
		var serverLabel string
		if json.Unmarshal(raw, &serverLabel) == nil && safeCreateField(serverLabel) == serverLabel && serverLabel == request.ServerName {
			result.ServerLabel = serverLabel
		}
	}
	return result
}

// CreateOnce sends one request and never persists or retries secret material.
// The setup URL and private key are delivered through the split owner handoff
// callback only.
func CreateOnce(ctx context.Context, request Request, options Options, owner int64) (result OneShotResult) {
	dispatchState := "pending"
	diagnostics.Info("migration.create",
		diagnostics.Field{Key: "event", Value: "entered"},
		diagnostics.Field{Key: "phase", Value: "create"},
		diagnostics.Field{Key: "dispatch_state", Value: dispatchState})
	defer func() {
		if dispatchState == "pending" {
			result.LocalFailure = true
		}
		if dispatchState != "pending" && options.MigrationRecipient != nil {
			current, err := options.MigrationRecipient()
			if err != nil || current != owner {
				result.LocalFailure = true
				diagnostics.Error("migration.recipient", errors.Join(errors.New("paired recipient changed"), err))
			}
		}
		safe := SanitizeOneShotResult(result)
		code, stage := safe.Code, safe.Stage
		if code == "" {
			code = "none"
		}
		if stage == "" {
			stage = "none"
		}
		handoff := "not_attempted"
		switch safe.Handoff {
		case "delivered":
			handoff = "delivered"
		case "failed":
			handoff = "failed"
		}
		diagnostics.Info("migration.create",
			diagnostics.Field{Key: "event", Value: "result"},
			diagnostics.Field{Key: "phase", Value: "result"},
			diagnostics.Field{Key: "dispatch_state", Value: dispatchState},
			diagnostics.Field{Key: "creation_state", Value: safe.CreationState},
			diagnostics.Field{Key: "code", Value: code},
			diagnostics.Field{Key: "stage", Value: stage},
			diagnostics.Field{Key: "handoff_delivery", Value: handoff})
	}()
	safeServerName := ""
	if ValidMigrationServerName(request.ServerName) {
		safeServerName = request.ServerName
	}
	fail := func(state, code string, err error) OneShotResult {
		if err != nil {
			diagnostics.Error("migration.create", err, diagnostics.Field{Key: "creation_state", Value: state}, diagnostics.Field{Key: "code", Value: code})
		}
		return OneShotResult{Status: "retry_later", CreationState: state, Code: code, ServerName: safeServerName}
	}
	if err := ctx.Err(); err != nil {
		return fail("not_created", "request_invalid", err)
	}
	if err := validateOneShotRequest(request); err != nil {
		return fail("not_created", "invalid_arguments", err)
	}
	options = completeOptions(options)
	if owner <= 0 || options.MigrationRecipient == nil || options.PrepareRecipientHandoffParts == nil {
		return fail("not_created", "recipient_unavailable", errors.New("paired recipient unavailable"))
	}
	current, err := options.MigrationRecipient()
	if err != nil || current != owner {
		return fail("not_created", "recipient_unavailable", errors.Join(errors.New("paired recipient mismatch"), err))
	}
	deliverParts, err := options.PrepareRecipientHandoffParts(owner)
	if err != nil || deliverParts == nil {
		return fail("not_created", "recipient_unavailable", errors.Join(errors.New("handoff unavailable"), err))
	}
	token, err := options.LoadHetznerToken()
	if err != nil {
		return fail("not_created", "credentials_unavailable", err)
	}
	pub, private, secret, err := generateLoginMaterial()
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
		ServerName string   `json:"server_name"`
		Locations  []string `json:"locations,omitempty"`
		PublicKey  string   `json:"login_public_key"`
	}{token, request.ServerType, request.ServerName, request.Locations, pub})
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
	dispatchState = "dispatched"
	diagnostics.Info("migration.create",
		diagnostics.Field{Key: "event", Value: "http_dispatch"},
		diagnostics.Field{Key: "phase", Value: "dispatch"},
		diagnostics.Field{Key: "dispatch_state", Value: dispatchState})
	response, err := options.HTTPClient.Do(httpRequest)
	dispatchState = "returned"
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
	topLevelFields, fieldsErr := decodeUniqueTopLevelFields(data)
	if fieldsErr != nil {
		return unusableOneShotResponse(OneShotResult{ServerName: safeServerName}, response.StatusCode, fmt.Errorf("invalid create response: %w", fieldsErr))
	}
	var decoded oneShotResponse
	if err := decodeExactJSON(data, &decoded); err != nil {
		return unusableOneShotResponse(creationEvidenceFromTopLevel(topLevelFields, request, safeServerName), response.StatusCode, fmt.Errorf("invalid create response: %w", err))
	}
	result = OneShotResult{Status: "retry_later", CreationState: decoded.CreationState, ServerName: safeServerName, ServerID: decoded.ServerID, ServerLabel: safeCreateField(decoded.ServerLabel), IP: safeCreateField(decoded.IP), Location: safeCreateField(decoded.Location), Expires: decoded.Expires, Attempt: safeCreateField(decoded.Attempt)}
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
	if result.ServerLabel != request.ServerName {
		result.ServerLabel = ""
	}
	if net.ParseIP(result.IP) == nil {
		result.IP = ""
	}
	if result.ServerID < 0 {
		result.ServerID = 0
	}
	if result.Expires < 0 {
		result.Expires = 0
	}
	result.Code = diagnostics.MigrationCode(scrub(decoded.Details.Code))
	result.Stage = diagnostics.MigrationStage(scrub(decoded.Details.Stage))
	if result.Code == "" {
		result.Code = diagnostics.MigrationCode(scrub(decoded.Error.Code))
	}
	if result.Stage == "" {
		result.Stage = diagnostics.MigrationStage(scrub(decoded.Error.Stage))
	}
	result.ProviderCode = diagnostics.BackendValue(scrub(decoded.Error.ProviderCode))
	result.ProviderMessage = diagnostics.MigrationProviderMessage(scrub(decoded.Error.ProviderMessage))
	result.ProviderFields = safeProviderFields(scrubProviderFields(decoded.Error.ProviderFields, secret, token, pub))
	result.HTTPStatus = response.StatusCode
	switch decoded.Status {
	case "stop":
		result.Status = "stop"
		return SanitizeOneShotResult(result)
	case "retry_later":
		fields := []diagnostics.Field{{Key: "status", Value: fmt.Sprint(response.StatusCode)}, {Key: "code", Value: result.Code}, {Key: "stage", Value: result.Stage}, {Key: "creation_state", Value: result.CreationState}, {Key: "backend_code", Value: result.ProviderCode}, {Key: "provider_message", Value: result.ProviderMessage}}
		for _, field := range result.ProviderFields {
			fields = append(fields, diagnostics.Field{Key: "provider_field", Value: diagnostics.MigrationProviderFieldValue(field.Field, field.Messages)})
		}
		diagnostics.Error("migration.create", errors.New("service requested retry"), fields...)
		return SanitizeOneShotResult(result)
	case "success":
	default:
		return unusableOneShotResponse(result, response.StatusCode, errors.New("missing or invalid create status"))
	}
	setup, parseErr := url.Parse(decoded.SetupURL)
	origin, originErr := url.Parse(options.WebOrigin)
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
	if decoded.ServerID <= 0 || result.Attempt == "" || result.ServerLabel == "" || result.ServerLabel != request.ServerName || result.IP == "" || result.Location == "" || result.Expires <= 0 || parseErr != nil || originErr != nil || setup.Scheme != origin.Scheme || setup.Host != origin.Host || setup.User != nil || !fragmentMatches {
		result.Code = "handoff_failed"
		result.Stage = "handoff"
		result.Handoff = "failed"
		diagnostics.Error("migration.handoff", errors.New("invalid create success handoff"))
		return result
	}
	current, err = options.MigrationRecipient()
	if err != nil || current != owner {
		result.Code = "handoff_failed"
		result.Stage = "handoff"
		result.Handoff = "failed"
		result.LocalFailure = true
		diagnostics.Error("migration.handoff", errors.Join(errors.New("paired recipient changed"), err))
		return result
	}
	err = deliverParts(ctx, decoded.SetupURL, private)
	if err != nil {
		result.Code = "handoff_failed"
		result.Stage = "handoff"
		result.Handoff = "failed"
		if _, logged := err.(interface{ DiagnosticLogged() }); !logged {
			diagnostics.Error("migration.handoff", err)
		}
		return result
	}
	result.Handoff = "delivered"
	result.Status = "success"
	return result
}
