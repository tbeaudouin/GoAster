package chat

import "goaster/internal/diagnostics"

// localFailure carries only fixed descriptions across the user-facing boundary.
type localFailure struct {
	reason string
	cause  error
}

func (err localFailure) Error() string {
	switch err.reason {
	case "tool_free_request":
		return "A tool was requested for a tool-free request."
	case "unsupported_tool":
		return "An unsupported tool was requested."
	case "web_search_limit":
		return "Codex requested too many web_search calls."
	case "web_fetch_limit":
		return "Codex requested too many web_fetch calls."
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
	return []diagnostics.Field{{Key: "local_stage", Value: "post_stream"}, {Key: "local_reason", Value: err.reason}}
}
