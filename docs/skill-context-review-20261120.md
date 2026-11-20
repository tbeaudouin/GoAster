# Skill context independent review — 2026-11-20

Reviewed the uncommitted work at HEAD `1b27c205fba00bd144044be56598fe3180011758` and compared the full suite with main `a53599a8473cd86cd776954fdbd292a39a268156`. Read `agents.md`; no `CLAUDE.md` or other agent guidance variant exists under the repository. No permanent selection registry or digest freshness mechanism remains. Legacy `selected_skills` JSON is ignored on decode and is omitted on the next save; it does not cause reload, action replay, or conversation clearing.

## Findings and changes

- Removed the remaining ten-load per-turn limit and aggregate loaded-skill byte limit. Loads are constrained by the existing per-file limits and actual model context budget. Discovery's pre-existing 100-skill count limit was replaced with a 1 MiB aggregate metadata bound. Starter installation retains its existing bounded bundled-file handling.
- Preserved assistant text in a model continuation when the same response contains tool calls. Such intermediate text is passed to the next model request; only the final assistant answer is stored as the assistant turn.
- Skill load errors now propagate with their underlying causes to the existing owner diagnostic boundary instead of being silently converted into generic tool output. Skill diagnostics and checkpoint failures are checked for redaction. Context accounting uses the instructions already obtained for the request, avoiding another discovery during a load.
- Restored coverage for web search/fetch budgets with a suppressed mixed skill batch and for skill failure diagnostic redaction from the deleted tests. Added tests for eleven loads in one turn, assistant continuation text, ordering and restart, explicit reload, failed loads, checkpoint save failure, and more than 100 discoverable skills. Removed tests that required registry refresh, digest pinning, deduplication, or stale-batch rechecks describe superseded behavior.

## Review of behavior

The current user message precedes each loaded guidance message in persisted history. Guidance is saved before a subsequent tool action or model continuation; a save error stops processing and retains the wrapped cause. Successful checkpoints survive subsequent request errors or cancellation. The existing request retry machinery retries the model request rather than re-executing earlier tools; the existing retry tests cover completed actions and interruptions. Mixed batches suppress actions while loading skills. Explicit later loads read current file contents and append guidance rather than rewriting prior context. Intermediate assistant text is preserved for the active request, not as a completed answer.

Guidance participates in normal history token accounting, summarization, bounded trimming, save and restart. Summarization or existing fallback trimming can discard older verbatim context under pressure; this is ordinary conversation behavior, not permanent skill pinning. The model input fit check accounts for current instructions, input, calls and outputs. Limits remain for individual skill files, total discovery metadata, session files, message history, and model context. No new skill freshness or action recheck was added. Error diagnostics use the existing privacy boundary; skill content, prompts, private paths and arbitrary errors must not be logged. Existing action uncertainty and scheduled cancellation handling remain in force.

## Validation

All commands ran offline with `GOTOOLCHAIN=local GOPROXY=off` and `/root/go/pkg/mod/golang.org/toolchain@v0.0.1-go1.25.13.linux-amd64/bin/go` (Go 1.25.13). No Git writes were used. Baseline was created with `git archive a53599a | tar -x` in a disposable directory inside this repository; `go test ./...` there exited 1 with exactly these three test failures:

- `goaster/cmd/goaster-agent`: `TestAgentPromptUsesRuntimeSkillsAndMemorySetup` — bundled default memory differs from test expectation.
- `goaster/internal/artefacts`: `TestFeatureInspirationPolicy` — missing expected policy clauses `copy no code` and `existing private /env flow`.
- `goaster/internal/artefacts`: `TestInstallBundledStartersOnce` — bundled default memory differs from test expectation.

The working tree full-suite run reproduced the same three failures. The chat suite passed, including the existing retry/no-replay and compaction tests. The focused skill and discovery regression tests and race checks passed; `go vet ./...` passed. `git diff --check` passed. The baseline comparison establishes the three failures on the requested commit under the same toolchain and environment; it does not establish their cause beyond the reported assertions.

Final command evidence (all using the environment and Go binary above):

- `go test ./...` — exit 1, only the three baseline tests listed above; `goaster/internal/chat` passed.
- `go test -race ./internal/chat` — exit 0, full chat package.
- `go test -race ./internal/chat ./internal/artefacts -run 'TestManyLoadsInOneTurnAndCheckpointOrdering|TestSkill|TestWebBudgetsExcludeSuppressedSkillBatch|TestDiscoverMoreThanOneHundredSkills'` — exit 0 for both packages after the discovery change.
- `go vet ./...` — exit 0 after the final code change.
- `go test ./internal/artefacts -run TestDiscoverMoreThanOneHundredSkills` — exit 0.
