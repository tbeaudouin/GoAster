# Environment export command rename validation, 2026-11-19

## Scope

Renamed the environment-download command to `/getenv` in the canonical catalog, tests, README, documentation, bundled skills and migration fixture. Added the command to Telegram's native menu and removed obsolete manual-only guidance. No legacy compatibility alias remains. A repository-wide literal scan for the previous hyphenated command and tracked filenames found no remaining references.

Private-chat-only export, pairing/authorization, bot-username suffix handling, starter-file fallback, file bytes and no-agent-context boundaries remain unchanged. Export/import instructions tell users of older agents to consult that agent's own `/help` before downloading environment settings. GoAsterWeb was scanned and has no matching references or changes.

## Validation

Parent independently verified these offline checks passed:

- `go test ./...`
- `go vet ./...`
- `go test -race ./internal/telegram ./internal/telegramcommands ./internal/chat`
- `bash packaging/test-skill-assets.sh`
- `git diff --check`
- Full source and filename scan for the previous command spelling.

Final read-only Terra review: SATISFIED after adding older-agent version guidance.

## Remaining gaps

No actual Telegram interaction, running-agent upgrade, installed-skill rollout, deployment, real export/import or cloud operation occurred. Existing running versions retain their current commands until explicitly upgraded; use their own `/help`.
