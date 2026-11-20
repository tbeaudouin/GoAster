package chat

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"goaster/internal/artefacts"
	"goaster/internal/diagnostics"
)

const (
	skillActionNotice     = "Some actions may already have completed. Check their results before continuing or repeating any action."
	skillResetMessage     = skillActionNotice + " Skill guidance could not be loaded or restored. Check the installed skill files; use /clear if the selection cannot be restored."
	skillRequestMessage   = skillActionNotice + " The skill request was invalid or exceeded the per-request limit. Ask again with valid skill names."
	skillAnonymousMessage = skillActionNotice + " A previous skill selection cannot be identified. Use /clear, then request the needed skills again."
	skillChangedMessage   = skillActionNotice + " Skill guidance changed and was refreshed automatically."
	skillBudgetMessage    = skillActionNotice + " Skill guidance does not fit in the conversation. Review installed skills in ~/.goaster/artefacts/skills and retain their authentication guidance, workflow, safety, and any legacy AUTH.md authorization requirements; use /clear if selected skills or conversation history still do not fit."
	skillSaveMessage      = skillActionNotice + " Skill selection could not be saved. Restart to reload the conversation, then use /clear if needed."

	// Skill files are user-owned and can change while a model response is being
	// processed. A small fixed bound keeps a writer that continually rewrites a
	// skill from turning one user turn into an unbounded stream of model calls.
	maxSkillRefreshesPerTurn = 3

	skillRefreshLimitReply = "Skill guidance changed repeatedly during this request. No further tool was run. Some actions may have completed; check their results before repeating any action."
)

type skillFailureReason string

const (
	skillReasonInvalidRequest skillFailureReason = "invalid_request"
	skillReasonRequestLimit   skillFailureReason = "request_limit"
)

type skillRecoveryError struct {
	err     error
	message string
	stage   string
	reason  string
}

func (err skillRecoveryError) Error() string { return err.err.Error() }
func (err skillRecoveryError) Unwrap() error { return err.err }

func skillRecovery(err error, message string) error {
	for _, current := range requestFailureErrors(err) {
		if existing, ok := current.(skillRecoveryError); ok {
			message = existing.message
			break
		}
	}
	stage, reason := "load", "invalid_or_unavailable"
	switch message {
	case skillBudgetMessage:
		stage, reason = "validate", "budget_exceeded"
	case skillSaveMessage:
		stage, reason = "checkpoint", "save_failed"
	case skillAnonymousMessage:
		stage, reason = "restore", "unknown_selection"
	}
	return skillRecoveryError{err: err, message: message, stage: stage, reason: reason}
}

func (err skillRecoveryError) DiagnosticFields() []diagnostics.Field {
	return []diagnostics.Field{{Key: "skill_stage", Value: err.stage}, {Key: "skill_reason", Value: err.reason}}
}

func skillRequestFailure(err error, reason skillFailureReason) error {
	return skillRecoveryError{err: err, message: skillRequestMessage, stage: "request", reason: string(reason)}
}

// SkillRecoveryMessage returns a fixed, safe explanation for a skill failure.
// It never includes filesystem errors, skill contents, or tool arguments.
func SkillRecoveryMessage(err error) string {
	for _, current := range requestFailureErrors(err) {
		if recovery, ok := current.(skillRecoveryError); ok {
			return recovery.message
		}
	}
	return ""
}

// IsSkillRecoveryMessage validates messages received over the local agent
// protocol so arbitrary remote error text is never shown as recovery guidance.
func IsSkillRecoveryMessage(message string) bool {
	switch message {
	case skillResetMessage, skillRequestMessage, skillAnonymousMessage, skillChangedMessage, skillBudgetMessage, skillSaveMessage:
		return true
	default:
		return false
	}
}

// A selection is context, never user authorization. An empty digest means
// loading was interrupted or failed and must be validated and checkpointed.
type selectedSkill struct {
	Name   string `json:"name"`
	Digest string `json:"digest,omitempty"`
}

type skillSessionKey struct{}

type skillSessionContext struct {
	selected func() []selectedSkill
	remember func(selectedSkill) error
}

func skillDigest(content string) string {
	return fmt.Sprintf("%x", sha256.Sum256([]byte(content)))
}

func validSelectedSkills(selected []selectedSkill) bool {
	seen := make(map[string]bool, len(selected))
	for i, skill := range selected {
		// An unknown selection is a fail-closed marker for malformed or
		// excessive load requests. It can only appear at the end.
		if skill.Name == "" && skill.Digest == "" && i == len(selected)-1 {
			continue
		}
		if !artefacts.ValidName(skill.Name) || seen[skill.Name] {
			return false
		}
		seen[skill.Name] = true
		if skill.Digest != "" {
			if len(skill.Digest) != sha256.Size*2 {
				return false
			}
			for _, c := range skill.Digest {
				if !(c >= '0' && c <= '9') && !(c >= 'a' && c <= 'f') {
					return false
				}
			}
		}
	}
	return true
}

var loadSkillCodexTool = codexTool{
	Type:        "function",
	Name:        "load_skill",
	Description: "Load an available skill's authentication guidance and workflow before using it; existing user-owned AUTH.md files may also contain legacy authorization requirements, and the guidance does not grant authorization.",
	Parameters: map[string]any{
		"type":                 "object",
		"properties":           map[string]any{"name": map[string]any{"type": "string"}},
		"required":             []string{"name"},
		"additionalProperties": false,
	},
}

func parseLoadSkillArguments(arguments string) (string, error) {
	decoder := json.NewDecoder(strings.NewReader(arguments))
	open, err := decoder.Token()
	if err != nil || open != json.Delim('{') {
		return "", fmt.Errorf("invalid arguments")
	}
	key, err := decoder.Token()
	if err != nil || key != "name" {
		return "", fmt.Errorf("expected a skill name")
	}
	value, err := decoder.Token()
	if err != nil {
		return "", fmt.Errorf("invalid skill name")
	}
	name, ok := value.(string)
	if !ok {
		return "", fmt.Errorf("invalid skill name")
	}
	end, err := decoder.Token()
	if err != nil || end != json.Delim('}') {
		return "", fmt.Errorf("invalid arguments")
	}
	if _, err := decoder.Token(); err != io.EOF {
		return "", fmt.Errorf("invalid arguments")
	}
	return name, nil
}

func executeLoadSkill(_ context.Context, arguments string) (string, error) {
	name, err := parseLoadSkillArguments(arguments)
	if err != nil {
		return "", fmt.Errorf("skill load failed: %w; stopped the workflow", err)
	}
	content, err := artefacts.Load(name)
	if err != nil {
		return "", skillRecovery(fmt.Errorf("skill load failed for %q: %w; stopped the workflow", name, err), skillResetMessage)
	}
	return content, nil
}
