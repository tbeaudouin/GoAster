# Migration capacity/retry acceptance — 2026-11-16

**SATISFIED** — No remaining actionable correctness, safety, duplicate-create, or permanent-error findings in the latest complete local capacity/retry diff after the final review correction.

Reviewed repository conventions (`agents.md` and `CONTRIBUTING.md`), the runtime and diagnostic changes, the new capacity/retry regressions, the migration skill policy changes, and the surrounding envelope decoder, create path, retained evidence, and scheduler claim/completion behavior.

Retries require a direct Web transport timeout/temporary failure or an exact allowlisted transient envelope status/stage/code/provider-code combination. Provider DNS, TLS, and generic transport envelopes are terminal. Permanent, malformed, inconsistent, unknown, and status-only errors stop for review. The three-consecutive-failure budget is persisted before rearming; successful empty capacity resets it while retaining the 30-minute monitor. Storage failures fail closed. Execution ownership, retained created/unknown evidence, and the durable dispatch marker guard the single create path; create failures do not enter the availability retry path.

Independent offline verification passed:

```text
GOTOOLCHAIN=local GOPROXY=off GOSUMDB=off /usr/local/go/bin/go test -timeout=120s -count=1 ./internal/migration ./internal/scheduler
git diff --check
```

The parent independently reports both repositories' full offline suites and vet, source migration/scheduler/chat race checks, and the Web full app race check passed. The supplied paired Web final review is SATISFIED. These broader results are supplied evidence, not checks rerun by this reviewer.

Review stayed within this repository root. No code was edited, and no network or live systems were contacted. This acceptance document is the only file written by this review.
