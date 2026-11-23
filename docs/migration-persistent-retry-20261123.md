# Migration persistent retry — 2026-11-23

Update 2026-11-23: the later scheduled outcome change supersedes the no-capacity silence described below; each completed attempt queues a fixed safe status to its originating conversation.

One private approval now covers the immediate create and the same unlimited 30-minute job after any migration error. The instructions direct the job to retain the approved name, type and locations; no-capacity remains silent. Confirmed delivered creation and validated exact-name target-exists are terminal. Unknown outcomes retain their classification, and partial handoff is never replayed. Local claim, ownership and durable-storage failures still require review; generic interrupted execution remains unchanged. The existing GoAsterWeb exact-name check is relied on, with the accepted residual check-then-create duplicate risk after an uncertain timeout.

Offline validation used `/usr/local/go/bin/go`, `PATH=/usr/local/go/bin:$PATH`, `GOPROXY=off`, and `GOSUMDB=off`:

- `go test -race ./cmd/goaster-agent ./internal/artefacts -run 'TestGenericCron.*Migration|TestGenericCron.*SelfDisableCannotStopRetry|TestScheduledMigration|TestScheduledOutcome|TestMigrationRetryPolicy|TestGoasterMigrationSkill|TestMigrationPartialDelivery|TestMigrationTerminal'` — passed. Covers recurring result matrix, terminal results, repeated call rejection, self-disable, partial delivery, model failure, claim/ownership checks, and installed/loaded skill content.
- `go test -race ./cmd/goaster-agent ./internal/chat ./internal/migration ./internal/scheduler ./internal/telegram ./internal/artefacts` — chat, migration, scheduler and Telegram passed; agent and artefacts hit the four existing fixture failures listed below. No race report.
- `go test ./...` — five previously reproduced fixture failures: `TestAgentPromptUsesRuntimeSkillsAndMemorySetup`, `TestTaskPlanningDefaultSkill`, `TestFeatureInspirationPolicy`, `TestInstallBundledStartersOnce`, and `TestBundledGitHubAuthDocumentsExact`; other packages passed.
- `go vet ./...` and `go build ./...` — passed.

This changes repository defaults and code only. Existing installed skills are not automatically replaced; rollout needs a reviewed update to the installed skill and deployment of the runtime. No live provider, model, scheduler, or deployment operation was performed.

Review follow-up: retry after a nonterminal result now requires a positively identified completed-tool provider failure and no unrelated tool calls. Handler, storage, checkpoint, interrupted-action and unclassified failures require review, as does a later model failure following any unrelated tool call. The existing tool observer is preserved. This is conservative for provider failures without sufficient typed evidence. The ordinary instruction-led cron design does not bind approved parameters at runtime or prevent self-disable before a migration call; those remain preexisting model-compliance limits.

Follow-up offline validation (`PATH=/usr/local/go/bin:$PATH GOPROXY=off GOSUMDB=off`):

- `go test -race ./cmd/goaster-agent ./internal/artefacts -run 'TestGenericCron.*Migration|TestGenericCron.*SelfDisableCannotStopRetry|TestScheduledMigration|TestScheduledOutcome|TestMigrationRetryPolicy|TestGoasterMigrationSkill|TestMigrationPartialDelivery|TestMigrationTerminal'` — passed, including a real offline provider HTTP failure after a nonterminal migration result, unrelated successful tools before and after it, handler/storage/interruption failures, and preservation of an existing observer.
- `go vet ./...`, `go build ./...`, and `git diff --check` — passed.
- `go test -race ./internal/chat ./internal/migration ./internal/scheduler ./internal/telegram` — passed; this run started before the final conservative handling of joined provider errors, which was covered by the subsequent focused agent race run.

## Independent final validation

- Parent reran `go test ./... -skip '^(TestAgentPromptUsesRuntimeSkillsAndMemorySetup|TestTaskPlanningDefaultSkill|TestFeatureInspirationPolicy|TestInstallBundledStartersOnce|TestBundledGitHubAuthDocumentsExact)$'`: all remaining tests passed. This is not an unconditional full-suite pass; the five known baseline failures remain excluded.
- Parent reran focused migration/installed-skill race tests across `./cmd/goaster-agent ./internal/chat ./internal/artefacts`, plus `go vet ./...`, `go build ./...`, and `git diff --check`: passed on the final source.
- Independent final read-only Terra review: SATISFIED, no in-scope blockers. Existing ordinary instruction-led approval/parameter/pre-first-call model compliance limits are retained, not claimed as new runtime enforcement.
- Final Sol review was attempted after Terra but could not run: the Codex ChatGPT account rejected `gpt-6.2-sol` with `model_not_found`. No Sol approval is claimed.
