# Recent conversation turns

`/clear` still clears the conversation and summary. `/clear n` removes up to n recent raw user turns, including following assistant evidence, guidance, and interruption records. The reply reports the number removed. A retained summary cannot be selectively edited; the reply says so when one exists. Telegram history remains scoped to a chat and shared across its topics. Valid counted clears use the existing clear ordering and authorization path; malformed commands do not cancel active turns.

Offline validation with `/usr/local/go/bin/go` and `GOPROXY=off`:

- Focused clear tests pass in `internal/chat`, `internal/telegram`, and `cmd/goaster-agent`; they cover parser tables, actual tool and guidance checkpoints and interruption grouping, persistence/reload, recovery and migration guards, a save failure and diagnostic redaction, terminal and Telegram routing, and local-agent routing without provider authentication. Existing polling ordering tests run for both clear forms; active topic tests cover counted clear and invalid or unauthorized commands.
- The same focused packages and test selection pass with `-race`.
- `go vet ./...` and `go build ./...` pass.
- `go test ./...` fails only the known baseline tests `TestAgentPromptUsesRuntimeSkillsAndMemorySetup`, `TestFeatureInspirationPolicy`, and `TestInstallBundledStartersOnce`; all other packages pass.

The existing atomic replacement store can report a failure after rename, such as a directory sync failure: the acknowledged in-memory state stays unchanged, but the complete replacement may already be on disk and requires reload/reconciliation. Tests do not simulate every filesystem failure or a live Telegram service; counted clears reuse the existing per-chat ordering path and the active-topic test covers its cross-topic cancellation behavior.

Independent parent validation on the final source passed:

- `GOPROXY=off /usr/local/go/bin/go test ./internal/chat ./internal/telegram ./internal/telegramcommands ./internal/diagnostics`
- `GOPROXY=off /usr/local/go/bin/go test -race ./internal/chat ./internal/telegram ./cmd/goaster-agent -run 'Test.*Clear|TestParseClear|TestStopCancelsOnlyTheAuthorizedCurrentThread'`
- `GOPROXY=off /usr/local/go/bin/go vet ./...`
- `GOPROXY=off /usr/local/go/bin/go build ./...`
- `git diff --check`

The parent separately reproduced all three named full-suite failures on unchanged base `e75871c871e1b56ab52d52762185e8ff4956e6c7` using the exact three-test selection. Final independent Terra review: SATISFIED. No live-agent upgrade or restart was performed; source delivery requires a separate runtime upgrade before use.
