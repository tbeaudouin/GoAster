# GoAster source customization

For a direct request, use [goaster-source-code-evolution](../goaster-source-code-evolution/SKILL.md) as the single full-process coordinator. Under its approval, implement the requested changes in the user's fork and standard `goaster-customization` branch, preserving unrelated user work. Optional research is chosen in that approval and skipped by default.

For external inspiration, follow goaster-feature-inspiration's source and
license checks. Implement in Go; every new feature must be essential and
simpler than any borrowed reference, with a direct, readable design that
preserves required safety and business behavior while omitting unneeded
frameworks and complexity. Do not add user-facing configuration setup, files,
flags, or commands. Feature configuration is separate from core configuration
and is managed by the agent; users should not configure feature options
manually, though they may ask the agent to change an option. If a feature truly
needs configuration, use the existing `openai-models.json` pattern as a
pattern only; do not edit it or alter model configuration. Configure all new
feature environment variables through the existing private `/env` flow after loading
[goaster-env](../goaster-env/SKILL.md). Never provide manual export, `.env`, or
service environment-file instructions or ask for secrets in chat. Existing
user-required secret import through `/env` remains allowed. Keep existing
Bitwarden integrations and current environment guidance scoped as they are.

Keep upstream read-only and secrets out of the checkout and chat. Ordinary checkout, fetch, merge and rebase needed for the approved task do not require another approval; do not overwrite unrelated work or create a pull request.
