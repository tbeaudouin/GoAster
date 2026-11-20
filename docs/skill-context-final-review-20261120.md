# Final independent skill context review — 2026-11-20

Verdict: **READY — no blocking defect found** in the current diff against main `a53599a` for the approved conversation-only lifecycle. No implementation or test files changed during this review.

Read `agents.md` and the immediately preceding `docs/skill-context-review-20261120.md`. Searched tracked and hidden repository files for agent and CLAUDE guidance variants; only `agents.md` was found. Inspected the actual implementation and test diffs against main, including deleted registry/refresh tests, plus the surrounding session persistence, compaction, provider continuation, retry, and diagnostic code. All work stayed inside this repository; no Git writes, credentials operations, external runtime, or deployment actions were performed.

## Findings

- No runtime skill count limit, selected-skill registry, digest refresh, or automatic reload remains. Runtime discovery uses a metadata byte bound; the existing bundled starter asset count bound is separate from runtime discovery and loading. Per-file, history, session-file, and model-context bounds remain.
- Successful loads append ordinary persisted `guidance` messages after the current user message. Explicit reload appends newly read content without replacing earlier guidance. Guidance participates in ordinary token accounting, compaction input, fallback trimming, saving, and restart. No special instruction pinning or safety recheck was introduced.
- Legacy `selected_skills` is accepted as opaque JSON and ignored, including anonymous pending entries. The next encoding omits it. Its contents cannot trigger skill reads, action replay, or history clearing. Invalid JSON and unrelated invalid conversation data still use normal recovery.
- The guidance checkpoint commits in-memory history and the expected store version only after save succeeds. A failed checkpoint stops the continuation; previously successful checkpoints remain. A later request failure likewise preserves successful guidance checkpoints. Compaction checkpoints preserve the old conversation without prematurely recording the new user turn. These conclusions follow from the reviewed mutation ordering and the exercised persistence tests.
- Save failure is not a promise of filesystem rollback: a directory-sync failure after rename can leave the replacement file present. The existing expected-version comparison prevents a subsequent turn from silently overwriting that file; restart reads the checkpoint. This is existing store behavior, not a new skill recovery mechanism.
- Transient retries occur inside `requestCodex` with the same model request payload, below tool execution. Completed tools are not replayed by that retry. Post-tool authentication failure keeps its existing review marker rather than restarting the workflow. Mixed skill/action batches suppress actions and do not consume execution budgets for suppressed web calls.
- Persisted guidance maps to a valid user/input-text message in model input, without synthetic historical function calls. Active continuation retains assistant output text and paired function calls/results. The final request budget check includes instructions and the accumulated input. No live backend schema validation was performed.
- Skill load and checkpoint failures retain wrapped causes, stop the workflow, and reach the existing owner diagnostic boundary. Skill diagnostic fields use fixed allowlists; private contents, paths, prompts, and arbitrary error text are excluded from logs by the existing boundary. The exercised redaction tests passed.

## Validation performed in this review

Commands used the local Go 1.25.13 binary at `/root/go/pkg/mod/golang.org/toolchain@v0.0.1-go1.25.13.linux-amd64/bin/go`, with `GOTOOLCHAIN=local GOPROXY=off`.

- `go test -count=1 ./internal/chat ./internal/diagnostics` — passed, uncached. Includes retry/no-replay, summarization, migration diagnostic persistence, session durability, skill restart/reload, legacy marker, and failure redaction regressions.
- `go test -race ./internal/chat ./internal/artefacts -run 'TestSkill|TestManyLoadsInOneTurnAndCheckpointOrdering|TestWebBudgetsExcludeSuppressedSkillBatch|TestDiscoverMoreThanOneHundredSkills|TestCodexPostToolTransientRetryDoesNotReplayTool|TestSessionFailedSaveDoesNotRetainUndisplayedTurn|TestCompactionIsCheckpointedBeforeReplyFailure'` — passed for both packages.
- `git diff --check` — passed.

The full suite and its main comparison were not repeated in this review. The preceding Terra report records the same three starter-content expectation failures on main and the working tree: `TestAgentPromptUsesRuntimeSkillsAndMemorySetup`, `TestFeatureInspirationPolicy`, and `TestInstallBundledStartersOnce`. That baseline comparison is prior evidence, not an independently reproduced result here. Real provider acceptance, real filesystem crash durability, and model summary quality are inferred from code/contracts rather than established by these offline tests.
