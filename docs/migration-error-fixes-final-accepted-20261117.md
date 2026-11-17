# Final migration diagnostics acceptance re-review — 2026-11-17

Result: **NOT ACCEPTED — two actionable correctness defects remain.**

Read `agents.md`, `CONTRIBUTING.md`, the original Sol final review, latest Terra final-followup, preceding Terra reviews and validation report. Reviewed the entire tracked diff, untracked migration diagnostic implementation/tests, and relevant surrounding response, tool, session, private handoff and cron paths. Source remained read-only; this report is the only repository file written. The requested report filename does not imply acceptance.

## Acceptance blockers

1. **High: strict response decoding still discards explicit creation evidence.** At `internal/migration/oneshot.go:289–290`, a `decodeExactJSON` error returns a fresh `unknown` result before sanitizing independently usable creation state and identity. HTTP 500 with the syntactically valid response below reproduces the latest Terra finding:

   ```json
   {"creation_state":"created","server_id":42,"server_label":"goaster-migration-0123456789abcdef","error":"malformed"}
   ```

   An independent offline overlay probe followed this response through `CreateOnce`, the registered migration tool, the diagnostic observer, persisted session state, restart and follow-up model context. Tool output was `unknown`/`response_unusable`, with server ID zero and label absent; persisted and reconstructed metadata also said `unknown`. No private handoff occurred. Missing/status-inconsistent decoded error envelopes and contradictory lookup codes do preserve `created`; the earlier typed-decoding failure remains unresolved.

   **Required fix:** preserve an unambiguous explicit `created` and independently sanitized identity when an error envelope has incompatible types or unknown fields; reject ambiguous/duplicate creation-state fields, keep the response unusable and stop handoff. Add decoder-to-tool-to-checkpoint/restarted-follow-up regressions for malformed envelope types and fields. Do not broadly accept malformed responses or expose raw response data.

2. **High: shared-store reload can still erase pending latest diagnostics after a pre-replacement checkpoint failure.** `internal/chat/provider.go:867–877` retains the latest record in session and store memory, but `internal/chat/session.go:295–304` unconditionally replaces the store's diagnostic cache with disk records during `Load`. The subsequent ordinary save uses that cache at `session.go:372–375`. Pending status exists on the originating session, without a store-level guard protecting reload and stale replacement.

   An independent offline overlay probe first persisted `not_created`, observed `created`/`response_unusable`, injected a checkpoint failure before replacement, and restored the original storage path. The originating session correctly retained pending `created`. Calling `Load` on that same `SessionStore` then returned only the older `not_created`; `Save` succeeded and persisted only that older record. A fresh load confirmed the loss. Another session sharing this store can therefore receive stale evidence. The existing conflict regression uses a separate store and checks only the originating session's refusal; it does not cover this shared-store cache replacement.

   **Required fix:** protect pending evidence at store level so shared-store `Load`, session construction/reload and subsequent saves either retain the pending latest outcome or refuse to proceed without discarding it. Cover restored storage followed by same-store Load/Save and a second session or concurrent turn. Preserve the existing refusal when disk changed concurrently. A separate process cannot recover evidence that never reached disk; that restart-durability boundary does not excuse losing evidence already held by the shared live store.

## Other reviewed boundaries

- The original Sol cases are addressed for successfully decoded envelopes and originating-session recovery: sanitized `created` survives contradictory metadata; live pending records precede the save attempt; checkpoint failure stops model continuation; unchanged storage permits later persistence, while changed disk state is refused by that session. The two latest Terra extensions above remain reproducible.
- Provider detail/log fields use fixed allowlists and bounds. Persisted metadata excludes resource identity, arguments, generic tool output, setup URLs, credentials and private keys. No additional secret/private-data disclosure or prompt-injection path was identified in the relevant diff. Diagnostic system context contains sanitized fixed values and includes its prefix in the 8 KiB budget.
- Records retain occurrence order and the newest outcome under trimming. Missing/invalid creation states remain uncertain. Existing session files lacking the additive metadata field remain compatible. Successful checkpoint/restart and failure/cancellation coverage pass.
- Reviewed normal create and generic cron behavior, claimed scheduled create, repeated-attempt and cron mutation guards, private conversation and transport-owner checks, and recipient revalidation during split handoff. No additional actionable regression was identified. Guidance/package edits preserve credential and approval boundaries. No external availability endpoint, retirement date or provider guarantee was verified during this offline review.

## Verification

- `git diff --check` — passed.
- `GOTOOLCHAIN=local GOPROXY=off GOSUMDB=off /usr/local/go/bin/go test ./internal/chat ./internal/migration ./internal/diagnostics ./internal/telegram -count=1 -timeout 120s` — passed.
- Independent `go test -overlay` probes, with source and overlay held in Linux memory-backed descriptors, failed the required acceptance invariants: malformed-envelope creation evidence was lost through persisted follow-up, and shared-store reload/save omitted pending `created`. No probe source was written to the repository. Tests used synthetic credentials, local HTTP fakes and temporary storage only.
- Parent-reported full uncached tests, vet, full race, build and packaging passed; this review does not claim to have independently repeated those broader checks. Passing suites do not resolve the reproduced gaps in their coverage.

No live services, real secrets, GitHub operations, commits, deployments or background jobs were used. No additional live validation is required for these findings.

Final disposition: **ACTIONABLE DEFECTS: preserve explicit creation evidence through malformed error-envelope decoding, and protect pending latest diagnostics across shared-store reload/save.**
