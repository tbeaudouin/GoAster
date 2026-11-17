# Migration diagnostics independent final follow-up — 2026-11-17

Result: **CONCRETE ISSUES — two confirmed correctness findings.**

Read `agents.md`, `CONTRIBUTING.md`, the latest Sol final review, preceding Terra reviews, and the updated validation report. Reviewed the current implementation and tests, concentrating on the two Sol findings, diagnostic disclosure, and normal session/tool behavior. Source was read-only; this report is the only repository file written.

## Findings

1. **High: a malformed error envelope can still discard explicit creation and identity before validation.** `internal/migration/oneshot.go:279–283` returns a fresh `unknown` result on any `decodeExactJSON` error, before the new `unusableOneShotResponse` path. A syntactically valid response such as HTTP 500 with `{"creation_state":"created","server_id":42,"server_label":"goaster-migration-0123456789abcdef","error":"malformed"}` fails to decode the error object and returns `unknown`/`response_unusable`, without the server ID or label. The same issue applies to incompatible types or unrecognized fields inside the error envelope. A fake-transport probe confirmed the loss and no handoff was attempted. Preserve an unambiguous explicit `created` and independently sanitized identity when the envelope cannot be decoded, while rejecting ambiguous creation-state fields and never delivering handoff. Add decoder-to-tool-to-checkpoint regressions for malformed envelope types and fields.

2. **High: a reload of the same `SessionStore` silently replaces pending evidence.** The failed checkpoint stores its pending diagnostic both in the `Session` and in the store cache (`internal/chat/provider.go:869–877`), but `SessionStore.Load` and a new session using that same store can call `loadStateUnderTurnLock`, which unconditionally replaces the store cache with the older disk records (`internal/chat/session.go:293–300`). A subsequent `SessionStore.Save` uses that stale cache (`session.go:372–376`) and succeeds without the pending record. An offline probe reproduced a pre-replacement checkpoint failure after an initial `not_created`, restored storage, called `Load` then `Save` on the shared store, and found only `not_created` on disk. The originating session still holds `created` in memory, but another user of the store can proceed with stale evidence; its next turn can also encounter a changed-version conflict rather than persist the pending record. Store-level pending state must survive or refuse reload and any stale replacement, including when sessions share a store; test Load/Save and a second session or concurrent turn after failure. Independent processes cannot know evidence that was never persisted, so the fix should state that durability boundary separately.

## Other observations

The specifically tested missing/status-inconsistent decoded envelopes and contradictory lookup codes now preserve sanitized `created` and stop handoff. The live originating session retains a failed checkpoint result and refuses a changed disk version; the remaining gaps are the earlier decoding exit and shared-store reload above. The focused tests cover successful checkpoint/restart, retention order, notice limits, cancellation and the checked concurrency conflict. Safe diagnostic fields are bounded and allowlisted, and persisted metadata omits resource details and handoff secrets; no additional actionable disclosure or normal tool/session regression was identified in the reviewed changes. No external service or provider guarantee was verified.

## Offline verification

- `git diff --check` — passed before this report.
- `sh packaging/test-skill-assets.sh` — passed.
- `GOTOOLCHAIN=local GOPROXY=off GOSUMDB=off /usr/local/go/bin/go test ./internal/chat ./internal/migration ./internal/diagnostics ./internal/artefacts -count=1 -timeout 120s` — passed.
- Two independent `go test -overlay` probes used Linux memory-backed descriptors for test sources and overlay; both failed their required invariants, demonstrating the findings above. The response used a fake transport and synthetic token; the store probe used temporary test storage.

No live services, real secrets, GitHub, deployment, commits, or background jobs were used. A failed checkpoint cannot guarantee restart durability, and no diagnostic checkpoint is possible before the handler returns.

Final disposition: **CONCRETE ISSUES**.
