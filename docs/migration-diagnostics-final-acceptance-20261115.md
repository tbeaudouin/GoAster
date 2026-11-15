# Migration diagnostics final acceptance — 2026-11-15

Verdict: **SATISFIED**.

Read root `agents.md`; repository search found no other agents/CLAUDE variants. Acceptance focuses on the remaining P2 in `migration-diagnostics-final-rereview-20261115.md`; the earlier four blockers remain accepted as fixed there.

- `internal/migration/runtime.go:435–439` disables and verifies the monitor before reading history, then returns `failureResult(stageStop, historyErr, stoppedResult)` on history failure. The error now reports the history failure rather than successful `monitor_stopped`.
- `failureResult` and `diagnosticForFailure` classify malformed history as `persisted_state_invalid`; protected-storage failures classify as `protected_storage_failure`. Fixed causes and actions direct manual record reconciliation or protected-storage repair and status inspection before a new attempt. The original history error remains wrapped through `migrationFailure.Unwrap`.
- Diagnostic normalization preserves `stage=stop`, `result_state=stopped`, `creation_state=unknown`, `monitoring_state=stopped`, and `monitoring_stopped=true`. It retains the classified error code and its fixed cause/action rather than deriving `monitor_stopped` from the result status. No normalization regression found in this path.
- The agent stop-error path calls `migrationToolOperationFailure`, which uses `DiagnosticFor(err, result)` and copies those fields into JSON with `status=error`. Thus the precise history/storage failure reaches the agent while retaining confirmed stopping and unknown creation certainty; parser text and private paths are not exposed.
- Strengthened `TestCorruptHistoryDoesNotPreventStop` checks the wrapped sentinel, actual disabled scheduler job, stopped/unknown states, precise malformed-history code/cause/action through `DiagnosticFor`, and JSON privacy. Storage classification and final tool field forwarding were checked by source tracing; this test does not itself exercise an unreadable-storage fixture or the full chat stop route.

Focused offline regression passed for both packages:

```text
GOPROXY=off GOSUMDB=off /usr/local/go/bin/go test ./internal/migration ./internal/chat -run 'Test(CorruptHistoryDoesNotPreventStop|Diagnostic|PersistedDiagnostic|MigrationTool)' -count=1
```

No remaining concrete blocker found within this acceptance scope. Only this document was edited; no source changes, commits, GitHub/cloud access, external network calls, live services, or real secrets were used.
