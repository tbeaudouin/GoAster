# Migration capacity retry evidence

Update 2026-11-23: the later scheduled outcome change supersedes the no-capacity silence described below; each completed attempt queues a fixed safe status to its originating conversation.

Every scheduled turn binds the migration capability when runtime creation is configured, regardless of schedule or run limit. The migration handler still requires a private, topic-free owner and the scheduler must claim the live execution before creation. Only the confirmed normal result `creation_state: not_created` and `code: no_capacity` retains the same enabled job without consuming a run or notifying the owner. `capability_unavailable` is a tool capability failure, not a provider capacity result. A completed creation or confirmed target-exists result uses the owner-bound self-disable; uncertain results and errors after an attempt require review.

Self-disable remains immediate before a migration invocation. After confirmed no-capacity, self-disable returns `disabled:false` and leaves the retry enabled. The migration claim prevents a later create after an immediate self-disable. Ordinary reminders retain their existing behavior. No job intent marker or scheduler state was added.

Offline cron coverage exercises omitted, null, zero, and bounded run limits, repeated no-capacity, terminal results, unknown creation, partial handoff, and lookup failure, including execution after reload and no replay after 24 hours. It covers self-disable before and after invocation, repeated migration calls, claim rejection, and manual disable around the execution claim. A privacy regression checks an invocation with creation unavailable and excludes the message and job ID from the diagnostic.

The supplied GoAsterWeb verification identifies main commit `fb8a2e0259dfcd81f398172ad30e24a59910ae7b`: its actual pre-dispatch capacity result is `no_capacity` with `not_created`; `capacity_unavailable` and `capability_unavailable` are not provider codes there. No guessed aliases were added. This is a source-only change; runtime and deployed services were not changed.

The unrelated `TestAgentPromptUsesRuntimeSkillsAndMemorySetup` failure was independently reproduced by the parent on a pristine detached worktree at base `c35a5ac`: the unchanged test sets `GOASTER_STARTER_DIR` to the repository skills directory and expects `# Memory guidance`, which its starter memory lacks. The baseline worktree was removed after the check. No fixture was changed.

Normal and genuine scheduled turns share the runtime tool registry and schemas, except that scheduled cron actions exclude `add` and `cron_disable_self` is scheduled-only. Existing tool authorization and safety checks continue to apply.

## Final validation

- Parent full `go test ./...` exposed the five pre-existing fixture/policy tests listed below; each was independently reproduced on pristine base `c35a5ac`. The migration approved-wording fixture was updated to exactly match Thomas's newly approved skill changes, and its dedicated test passes.
- Full suite passes with only those five baseline failures excluded: `go test ./... -skip '^(TestAgentPromptUsesRuntimeSkillsAndMemorySetup|TestTaskPlanningDefaultSkill|TestFeatureInspirationPolicy|TestInstallBundledStartersOnce|TestBundledGitHubAuthDocumentsExact)$' -count=1`.
- Full chat/scheduler/diagnostics tests and focused agent cron/parity/one-shot wiring tests pass without exclusions. Focused race tests pass for the agent and chat parity/migration paths; helpers also ran relevant scheduler/diagnostics races. `go vet ./...`, `go build ./...`, and `git diff --check` pass.
- Final independent Terra review found no code issues, only the now-corrected baseline-evidence documentation. Sol review was attempted but the account rejected that model as unsupported; no Sol review is claimed.
- No live agent upgrade, cron creation/re-enablement, migration execution, server creation, or GoAsterWeb source change occurred.
