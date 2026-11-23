package chat

import "goaster/internal/diagnostics"

// localFailure carries only fixed descriptions across the user-facing boundary.
type localFailure struct {
	reason string
	cause  error
}

func (err localFailure) Error() string {
	switch err.reason {
	case "tool_config_invalid":
		return "GOASTER_TOOL_CALL_LIMIT must be a positive integer."
	case "tool_config_unavailable":
		return "Tool call limit configuration is unavailable."
	case "tool_free_request":
		return "A tool was requested for a tool-free request."
	case "unsupported_tool":
		return "An unsupported tool was requested."
	case "finish_tools":
		return "The tool-free finish requested a tool."
	case "finish_empty":
		return "The tool-free finish returned an empty report."
	case "skill_budget":
		return "Skill guidance exceeded the conversation budget."
	case "skill_checkpoint":
		return "The skill guidance checkpoint could not be saved."
	case "evidence_save":
		return "Tool evidence could not be saved."
	default:
		return "A local processing step failed."
	}
}
func (err localFailure) Unwrap() error { return err.cause }
func (err localFailure) DiagnosticFields() []diagnostics.Field {
	stage := "post_stream"
	if err.reason == "tool_config_invalid" || err.reason == "tool_config_unavailable" {
		stage = "configuration"
	}
	return []diagnostics.Field{{Key: "local_stage", Value: stage}, {Key: "local_reason", Value: err.reason}}
}

// localFailureClassification exposes only fixed local configuration outcomes.
func localFailureClassification(err localFailure) string {
	switch err.reason {
	case "tool_config_invalid", "tool_config_unavailable":
		return err.reason
	default:
		return "local_failure"
	}
}
