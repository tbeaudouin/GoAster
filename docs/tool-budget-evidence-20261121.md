# Tool budget validation, 2026-11-21

The limit is 100 requested calls per turn, including setup-suppressed calls. Eligible calls execute in order; over-limit calls receive paired `tool_limit_reached` results. A single tool-free finish receives the accumulated transcript and instructions, subject to the context estimate. Its answer carries an incomplete-work notice. Unexpected tool calls or whitespace-only output in a successful finish produce a safe fallback and one bounded diagnostic.

Offline validation used `/usr/local/go/bin/go` with `GOPROXY=off GOSUMDB=off`:

- Focused tool-budget, skill checkpoint, in-flight context, and web tests passed. Fixtures cover 98+5 calls, skipped pairs, observer counts, 100 sequential executed calls, a successful finish using collected results, persistence and restart, oversized finish context, setup suppression, and finish failures without replay.
- `go test ./internal/chat ./internal/diagnostics -count=1` passed. Focused race tests passed.
- `go test ./...` failed in three unrelated existing tests: `TestAgentPromptUsesRuntimeSkillsAndMemorySetup`, `TestFeatureInspirationPolicy`, and `TestInstallBundledStartersOnce`. All other packages passed.
- `go vet ./...`, `go build ./...`, and `git diff --check` passed.

The fake model backend uses local HTTP fixtures; no live provider was contacted. Redaction tests exercise fixture secrets in failure bodies, credentials, tool results and unexpected finish calls; cancellation is expected and need not produce a diagnostic. The checkpoint pressure tests seed older history so trimming remains covered without exceeding the per-turn quota. Existing checkpoint failure tests guard against proceeding after a failed save. Review of authentication handling confirms refresh is limited to a 401 before tool execution; after a tool starts, a 401 returns the post-tool error rather than replaying the request. Transient model retries remain inside `requestCodex` and do not execute tools.

Final diagnostic correction validation: `GOPROXY=off GOSUMDB=off /usr/local/go/bin/go test ./internal/chat ./internal/diagnostics -count=1` passed, and `git diff --check 99591be` passed. The unexpected-finish regression checks one bounded diagnostic, unknown-model and absent protocol detail states, reference, phase, status and progress, plus redaction in both diagnostic and fallback. The normal finish regression checks that no error is logged. Terra reviewed the complete change; the final Sol re-review was SATISFIED after correcting missing diagnostic metadata.

## Independent final validation

The parent independently ran these checks on the final source with `PATH=/usr/local/go/bin:$PATH`, `GOPROXY=off`, and `GOSUMDB=off`:

- PASS `go test ./internal/chat ./internal/diagnostics -count=1 -timeout=180s`.
- PASS `go test -race ./internal/chat ./internal/diagnostics ./internal/scheduler -run 'TestToolBudget|TestManyLoads|TestInflight|TestLocalFailure|Test.*Diagnostic|Test.*Retry' -count=1 -timeout=180s`.
- PASS `go vet ./...`, `go build ./...`, and `git diff --check`.
- `go test ./... -count=1 -timeout=180s` failed only in the three named stale tests; all other packages passed. Independently rerunning those exact three tests on unchanged baseline `99591be2a9a450155d634a2aa563398cfaa07c6a` reproduced all three failures. They are not fixed or described as passing.

Source-only delivery: no running-agent upgrade, server access, restart, live-provider acceptance, or GitHub issue creation. Completion-save and context-budget failures remain real errors, not guaranteed partial-answer recovery. Already completed actions are never automatically replayed.
