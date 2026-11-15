# Final independent migration diagnostics re-review — 2026-11-15

Verdict: **BLOCKED**.

Read root `agents.md`; repository search found no other agents/CLAUDE variants. Reviewed the current tracked diff, diagnostic implementation and tests, the previous final review, and the follow-up section of `migration-diagnostics-validation-20261115.md`. Also checked scheduler claim/stop implementation. This review changes only this report; no source or tests were edited.

## Remaining blocker

**P2 — A corrupt-history stop hides the precise history error from the agent despite correctly stopping the monitor.** At `internal/migration/runtime.go:435–439`, a failed history load constructs a successful `monitor_stopped` diagnostic and passes it to `failureWithDiagnostic` with `historyErr`. The error retains its wrapped cause, so `errors.Is(err, ErrPersistedStateInvalid)` works, but `migrationFailure.Error()` returns only the diagnostic cause (`internal/migration/diagnostic.go:131–135`). `diagnosticForFailure` gives the outer diagnostic carrier priority over classifying its underlying cause (`diagnostic.go:275–284,310–330`). Consequently `migrationToolOperationFailure` (`internal/chat/migration_tool.go:217–229`) emits this model-facing combination:

```json
{
  "status": "error",
  "stage": "stop",
  "code": "monitor_stopped",
  "cause": "the migration monitor is stopped",
  "result_state": "stopped",
  "creation_state": "unknown",
  "monitoring_state": "stopped",
  "monitoring_stopped": true,
  "next_action": "no automatic action will run; obtain fresh owner approval before a new attempt"
}
```

The actual response also includes the safe monitor ID. The generic `status=error` signals some problem, but neither identifies corrupt/unreadable history nor directs manual reconciliation. Even the Go error string describes only successful stopping. Wrapped-cause retention and runtime logging do not satisfy the requirement that the agent receive an honest, precise error.

Required behavior: keep the verified stopped state and unknown creation certainty, while reporting the history failure using its safe classification (for malformed JSON, `persisted_state_invalid`, with its fixed cause and manual-reconciliation next action; for storage failures, the corresponding safe storage code). Preserve the wrapped cause. Do not turn this into `stop_unconfirmed`, claim creation did not happen, expose parser/file text, or require valid history before disabling the job.

`TestCorruptHistoryDoesNotPreventStop` passes because it checks the wrapped sentinel, disabled job, stopped result and unknown creation. It does **not** assert the returned diagnostic code/cause/action or model-facing response. The output above is established by tracing the current code, not by adding a new end-to-end test. Add a regression assertion at the tool/diagnostic boundary when fixing this remaining issue. The follow-up validation document's statement that the result reports the history error is accurate for the wrapped Go cause, but incomplete for the agent-visible diagnostic.

## Assessment of the requested fixes

| Requirement | Assessment and evidence |
| --- | --- |
| Optional `provider_code` and HTTP match | **SATISFIED.** `decodeWebError` accepts the three required fields or those fields plus the optional provider code, rejects explicit null, and uses exact decoding for unknown fields, duplicate keys and types. Existing code/stage/provider allowlists remain. Body `http_status` must equal the actual response status. Omitted provider-code and status-mismatch fixtures pass. The same decoder handles the other allowlisted provider codes without requiring the optional field. |
| Stop cannot publish stale pre-create history | **SATISFIED for the reported race.** Stop loads history only after atomic stop verification. Terminal diagnostic publication moved into `executeMonitor`'s defer before direct claim completion; scheduled execution publishes before returning to the scheduler that owns completion. The direct-start path no longer writes another terminal record after completion. A running claim prevents a successful stop. History loading also preserves validated `created` evidence across records. The stop interleaving and before-completion publication regressions pass. |
| Repeated terminal start retains diagnostic and original terms | **SATISFIED.** Both terminal-job branches call `MigrationStatusWithOptions`, using retained validated diagnostics and the original scheduler request; they copy the original locations into the returned migration result. Missing-token failure with changed requested type/locations and successful creation with changed requested type are covered. The retained delivery-failure path uses the same status reconstruction and preserves created certainty. No execution is rearmed by these returns. |
| Corrupt history cannot leave a valid non-running monitor armed | **SATISFIED for stopping; BLOCKED for precise error reporting as above.** Coordinated disable precedes history access. The malformed-history regression verifies disabled state, a non-nil history error, true stopped state and unknown creation. In-progress execution continues to return an honest in-progress/unconfirmed outcome rather than falsely claiming a completed stop. |
| Repeated stop idempotence | **SATISFIED for sequential repeated calls.** An already retained `monitor_stopped` record with matching creation certainty returns without another append. The regression repeats stop three times and checks unchanged directory entry count and retained created certainty. Concurrent append collisions can still fail closed; this is not a claim of transactional history storage. |

## Duplicate-create, state and privacy checks

No additional blocker found in the reviewed changes. The scheduler claim still atomically disables execution before availability/create, validates the exact message and schedule, and rejects overlapping execution. The exclusive dispatch marker remains before create. Only a successful no-capacity result retries; terminal failure and uncertain create responses require review. Confirmed creation retains `created` certainty through delivery failure and subsequent status. Existing available/create/lost-response/stop coordination scenarios pass.

The tool still exposes only start/status/stop, rejects unknown input and model-selected start IDs, and prevents scheduled or non-private starts. Fixed diagnostic cause/action text, bounded records, exact persisted validation, provider-code allowlists, private handoff storage and paired-recipient checks remain. Arbitrary provider bodies, credentials, owner links, keys and private paths are not copied into model-facing diagnostics. The remaining blocker requires safe error classification, not raw history error disclosure. Append-only storage and fail-closed concurrent publication remain documented limitations; no wholesale storage redesign is required for this finding.

## Independent validation

Passed with `GOPROXY=off GOSUMDB=off` and `/usr/local/go/bin/go`:

```text
go test ./internal/migration ./internal/chat -run 'Test(Diagnostic|TerminalPublished|StopReloads|CorruptHistory|ScheduledFailureRetained|PersistedDiagnostic|OfflineClaim|OfflineVerification|OfflineScheduled|ClaimRejects|RunDueAvailable|MigrationTool)' -count=1
go test ./internal/migration ./internal/chat -count=1
git diff --check
```

Both test invocations passed for both packages. The full package run includes the successful terminal-start contract regression. Repository tests use synthetic fixtures and loopback HTTP servers. Terra's reported full offline suite, vet and migration/chat/scheduler race passes are acknowledged; they were not independently rerun here. Existing passing tests do not cover the remaining precise-error requirement.

No GitHub, cloud, live services, real secrets, deployment or commit was used. Work stayed in the specified repository root.

## Fix validation — 2026-11-15

The corrupt-history stop now classifies the history error while retaining the verified stopped state, unknown creation state, and wrapped cause. The regression checks `DiagnosticFor`, fixed malformed-history cause and manual-reconciliation action, JSON privacy, and the actual disabled scheduler job. Protected-storage failures use the existing safe storage classification and its fixed repair-and-status action.

Passed offline with `GOPROXY=off GOSUMDB=off` and `/usr/local/go/bin/go`:

```text
go test ./internal/migration -run '^TestCorruptHistoryDoesNotPreventStop$' -count=1
go test ./...
go vet ./...
go test -race ./internal/migration ./internal/chat ./internal/scheduler
git diff --check
```

No GitHub, cloud, live services, real secrets, deployment, or commit was used.
