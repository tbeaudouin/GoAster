# Tool budget validation, 2026-11-21

## Historical shared100 validation

The original delivery validated a 100-requested-call limit, including setup-suppressed calls. Eligible calls executed in order; over-limit calls received paired `tool_limit_reached` results. A tool-free finish received the accumulated transcript and instructions, subject to the context estimate. Its answer carried an incomplete-work notice. Unexpected tool calls or whitespace-only output in a successful finish produced a safe fallback and one bounded diagnostic.

That validation covered 98+5 calls, skipped pairs, observer counts, 100 sequential executed calls, a successful finish using collected results, persistence and restart, oversized finish context, setup suppression, and finish failures without replay. It is retained here as historical evidence for the original shared100 boundary.

## Current shared1000 validation

The default quota is 1000 requested calls per user turn. The private /env setting GOASTER_TOOL_CALL_LIMIT accepts a positive integer; an absent or blank value defaults to 1000. A saved key takes precedence over the process environment, including when blank. Invalid or overflowing values and unavailable saved settings fail safely before tool-enabled provider work; tool-free requests remain unaffected. Each provider turn snapshots the effective value for its initial instructions, enforcement and finish. After the upgraded binary is running, a saved change applies on the next request without another restart. Counting, skip, graceful-finish, authentication, no-replay, privacy, checkpoint, and context behavior remain in place.

The new fixtures exercise the actual 1000-call boundary: a 998+5 response produces 1000 eligible calls and three paired skipped outputs, then two independent turns verify counter reset and malicious final calls. A 101-call below-boundary turn finishes naturally without a budget notice. Persistent sequential execution reaches 1000 calls, checks the current request in the tool-free finish and in-flight checkpoint, finishes with collected results, and retains the answer after restart. Normal postcompletion bounded-history trimming can remove the original request. The oversize fixture makes only its first output large and later outputs empty; exhaustion still causes finish-size rejection before a second network request. Finish HTTP, unauthorized, cancellation, and unexpected-final-call cases continue to assert no replay, bounded diagnostics, safe metadata, and redaction.

Earlier current-boundary offline validation used `/usr/local/go/bin/go` with `GOPROXY=off GOSUMDB=off`:

- `go test ./internal/chat ./internal/diagnostics ./internal/localenv -count=1 -timeout=180s` — passed.
- `go test ./internal/chat -run 'TestToolBudget' -count=1 -timeout=180s` — passed, including actual 1000 boundary and environment cases.
- `go test ./cmd/goaster-agent -run 'Test.*(Environment|Env|RuntimeEnvironment)' -count=1 -timeout=180s` — passed.
- `go test -race ./internal/chat -run 'TestToolBudget(EnvironmentConfiguration|SavedEnvironmentAndSnapshot|InvalidConfigurationIsSafe|ProcessEnvironmentSnapshot|InvalidSessionConfigurationMessage)' -count=1 -timeout=180s` — passed.
- `go vet ./...`, `go build ./...`, and `git diff --check` — passed.

The saved-setting fixture writes and reloads an isolated private localenv store, changes the saved limit between turns, and supplies its copied values through the existing request context. Tests cover precedence, blank defaults, invalid and overflowing values, redaction, unavailable settings, tool-free requests, and a process environment change during a turn and on the following turn. No real saved /env was changed. These earlier helper runs were focused; independent final validation below includes the full suite.

The fake model backend uses local HTTP fixtures; no live provider was contacted. Redaction tests exercise fixture secrets in failure bodies, credentials, tool results, and unexpected finish calls; cancellation is expected and need not produce a diagnostic. The checkpoint pressure tests seed older history so trimming remains covered without exceeding the per-turn quota. Existing checkpoint failure tests guard against proceeding after a failed save. Authentication refresh remains limited to a 401 before tool execution; after a tool starts, a 401 returns the post-tool error rather than replaying the request. Transient model retries remain inside `requestCodex` and do not execute tools.

The unexpected-finish regression checks one bounded diagnostic, unknown-model and absent protocol detail states, reference, phase, status, 1000-call progress, and redaction in both diagnostic and fallback. The normal finish regression checks that no error is logged. Completion-save and context-budget failures remain real errors, not guaranteed partial-answer recovery. Already completed actions are never automatically replayed.

The known full-suite exceptions remain out of scope: `TestAgentPromptUsesRuntimeSkillsAndMemorySetup`, `TestFeatureInspirationPolicy`, and `TestInstallBundledStartersOnce` are stale unrelated failures reproduced on the unchanged baseline. No running-agent upgrade, server access, restart, live-provider acceptance, or GitHub issue creation is part of this source-only validation.


## Independent final validation after configuration review

The parent independently ran the following on the final source with `/usr/local/go/bin/go`, `GOPROXY=off`, and `GOSUMDB=off`:

- PASS `go test ./internal/chat ./internal/diagnostics ./internal/localenv -count=1 -timeout=180s`.
- PASS `go test -race ./internal/chat ./internal/diagnostics ./internal/localenv -run 'TestToolBudget|TestLocalFailure|Test.*Environment' -count=1 -timeout=240s`. Selected race tests ran in chat; diagnostics and localenv reported no matching tests in that selection, and their normal tests passed above.
- PASS `go test ./cmd/goaster-agent -run 'Test.*(Environment|Env|RuntimeEnvironment)' -count=1 -timeout=180s`.
- PASS `go vet ./...`, `go build ./...`, and `git diff --check`.
- `go test ./... -count=1 -timeout=180s` failed only in the three named existing stale tests. Independently rerunning those exact tests on unchanged baseline `b285cb622907008eeb5af1e69d6e37b4ec7c393e` reproduced all three failures. No new full-suite failure was found.

Final Terra review: SATISFIED. It verified saved-setting changes between turns, fixed-limit instructions/enforcement/final report, configuration redaction and one real session diagnostic, and active-turn original-request checkpoint coverage. The source still respects ordinary postcompletion context trimming; no claim is made that a completed 1000-action turn retains every historical message or output.
