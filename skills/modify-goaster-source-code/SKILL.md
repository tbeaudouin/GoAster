# GoAster source customization

You can customize GoAster in the fork created using [goaster-fork](../goaster-fork/SKILL.md). Make the changes you want on the `goaster-customization` branch and, after approval, commit and push them to your fork on GitHub.

The next steps explain how to build and test those changes, then safely switch
the running GoAster to the new version. Ask the user before making any source,
Markdown skill, packaging, documentation, or test change; preserve existing
user changes. Do not silently narrow the request to a fixed patch list.
Before implementation, offer one optional choice: explore shared GoAster contributions with
[goaster-find-contributions](../goaster-find-contributions/SKILL.md), inspect
other agents' implementations with
[goaster-feature-inspiration](../goaster-feature-inspiration/SKILL.md), choose
both, or skip research. Do not repeat this offer if the coordinator already
made it. Either research path is optional and never blocks approved work.

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

Work only in the approved fork checkout on the `goaster-customization` branch.
Keep upstream read-only. Do not merge, rebase, reset, overwrite, delete, or
create a pull request unless that exact action is separately approved. Never
write secrets, tokens, private keys, or credential files into the checkout. A
changed repository, branch, checkout path, or host requires fresh approval. An
unexpected SHA change also requires fresh approval; a SHA change from a commit
made as part of the requested customization does not.
