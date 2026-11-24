package migration

import (
	"encoding/json"
	"fmt"
	"strings"
)

// FormatAttempt renders only sanitized migration information. retry describes
// the actual scheduling outcome: 30m, scheduled, failed, stopped, or unknown.
func FormatAttempt(result OneShotResult, retry string) string {
	status := result.Status
	result = SanitizeOneShotResult(result)
	switch status {
	case "success", "stop", "retry_later", "schedule_failed", "failed":
	default:
		status = "unknown"
	}
	var lines []string
	switch {
	case status == "retry_later" && result.Code == "no_capacity" && result.CreationState != "created":
		lines = append(lines, "⏳ Migration waiting for capacity", "Capacity is not available yet.")
		if result.CreationState == "not_created" && result.ServerID == 0 && result.IP == "" {
			lines = append(lines, "No server was created.")
		} else if result.CreationState == "unknown" {
			lines = append(lines, "It is unknown whether a server was created.")
		} else {
			lines = append(lines, "Server creation details conflict; check the outcome.")
		}
	case status == "success" && result.Handoff == "delivered" && result.CreationState == "created":
		lines = append(lines, "Server created", "The private key and setup link were delivered. Complete the setup using the link.")
	case status == "stop" && result.Code == "target_exists":
		lines = append(lines, "Migration stopped", "The exact target server already exists.")
	case status == "stop":
		lines = append(lines, "Migration stopped")
	case result.CreationState == "created" && result.Handoff != "delivered":
		lines = append(lines, "Migration delivery incomplete", "The server was created, but private delivery was incomplete. The handoff will not be resent.")
	default:
		lines = append(lines, "Migration attempt outcome")
		switch result.Code {
		case "provider_auth_rejected":
			lines = append(lines, "Provider authentication failed.")
		case "provider_lookup_failure":
			lines = append(lines, "Provider lookup failed.")
		case "provider_http_error", "web_http_error":
			lines = append(lines, "Provider request failed.")
		case "timeout", "provider_timeout":
			lines = append(lines, "Provider request timed out.")
		}
		switch result.CreationState {
		case "not_created":
			if result.ServerID == 0 && result.IP == "" {
				lines = append(lines, "No server was created.")
			} else {
				lines = append(lines, "Server creation details conflict; check the outcome.")
			}
		case "created":
			lines = append(lines, "The server was created; check the delivery and setup status.")
		default:
			lines = append(lines, "It is unknown whether a server was created.")
		}
	}
	if result.ServerName != "" {
		lines = append(lines, "", "Target: "+result.ServerName)
	}
	switch retry {
	case "30m":
		lines = append(lines, "Next step: Retry automatically every 30 minutes and check the existing name.")
	case "scheduled":
		lines = append(lines, "Next step: Retry automatically at the next scheduled run and check the existing name.")
	case "manual":
		lines = append(lines, "Next step: Check the outcome manually before retrying.")
	case "setup":
		lines = append(lines, "Next step: Complete the server setup.")
	case "existing":
		lines = append(lines, "Next step: Check the existing target before taking further action.")
	case "failed":
		lines = append(lines, "Next step: Automatic retry could not be scheduled; check the outcome before retrying.")
	case "stopped":
		lines = append(lines, "Next step: Automatic retry stopped.")
	}
	lines = append(lines, "", "Technical details", "Status: "+status, "Creation: "+result.CreationState)
	add := func(label, value string) {
		if value != "" {
			lines = append(lines, label+": "+value)
		}
	}
	add("Reason (code)", result.Code)
	add("Stage", result.Stage)
	if result.HTTPStatus != 0 {
		add("HTTP status", fmt.Sprint(result.HTTPStatus))
	}
	add("Provider code", result.ProviderCode)
	add("Provider message", result.ProviderMessage)
	for _, field := range result.ProviderFields {
		add("Provider field "+field.Field, strings.Join(field.Messages, ", "))
	}
	if result.ServerID != 0 {
		add("Server ID", fmt.Sprint(result.ServerID))
	}
	add("Server label", result.ServerLabel)
	add("IP", result.IP)
	add("Location", result.Location)
	if result.Expires != 0 {
		add("Expires", fmt.Sprint(result.Expires))
	}
	add("Attempt", result.Attempt)
	add("Handoff", result.Handoff)
	return strings.Join(lines, "\n")
}

// FormatToolCompletion converts the existing structured result at the display boundary.
func FormatToolCompletion(output string) string {
	var value struct {
		OneShotResult
		Message string `json:"message"`
	}
	if json.Unmarshal([]byte(output), &value) != nil {
		return FormatAttempt(OneShotResult{}, "unknown")
	}
	retry := "manual"
	if value.Status == "success" && value.CreationState == "created" && value.Handoff == "delivered" {
		retry = "setup"
	} else if value.Status == "stop" {
		retry = "existing"
	}
	if value.Status == "schedule_failed" {
		retry = "failed"
	}
	if value.Status == "retry_later" && value.Message == "Migration retry scheduled every 30 minutes." {
		retry = "30m"
	}
	return FormatAttempt(value.OneShotResult, retry)
}
