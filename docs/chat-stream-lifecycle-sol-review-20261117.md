# Final critical review — 2026-11-17

Verdict: **SATISFIED**. No concrete correctness, concurrency, diagnostic-safety, or migration-certainty blocker found in the current diff after Terra.

## Scope and evidence

Read `agents.md` first; no CLAUDE variants were present in this repository. Reviewed all nine changed Go files, Terra's review, the validation report, and the surrounding provider, session, diagnostic, migration, and scheduled-result paths. This review changed only this report. No new pointer declarations, production edits, network calls, deployment, commit, GitHub, or cloud actions were performed.

The parent reports full offline tests, vet, full race tests, build, and diff checks passing with `/usr/local/go/bin/go`. Those results are supplied evidence, not independently rerun results from this review. This review independently ran `git diff --check`, which passed. No live provider or migration behavior was tested.

## Stream cancellation and concurrency

- `cmd/goaster-agent/main.go:722–806`: the request gets a child context, and the first encode or flush failure preserves its wrapped cause and cancels that context immediately. Further emits stop. A failed initial recovery notice returns before environment loading or the session turn. A failed observer event prevents final/error writes after the session returns. Normal handler exits also cancel the child through a defer.
- `internal/chat/openai.go:1507–1528`: the real provider calls the observer synchronously, then checks `ctx.Err()` before marking an action started or calling its handler. Thus a stream failure inside the observer cancels before the upcoming action. This applies to the first action and a later action after an earlier action has completed; it does not undo earlier effects.
- The stream variables and encoder are local to one handler, and the current production provider invokes the observer inline in its sequential tool loop. No new concurrent access or goroutine is introduced. Cancellation is cooperative; the change does not promise rollback or forcibly terminate work that ignores its context.

## Deferred logging: duplicates and dropped causes

The real path is `requestReply` → `modelReply` → `Session.replyWithHistory` → `Session.replyWithAttachments` → the HTTP handler. When the stream observer fails, the immediately following cancellation check returns `context.Canceled`. `requestReply` wraps it as `providerFailure`, retaining the successful response status and `post-stream` phase. The session retains the chain through its model and request wrappers and joins the canceled parent context where appropriate.

The provider's own diagnostic defer is bypassed under `providerFailureBoundaryKey` because the session owns that boundary (`internal/chat/openai.go:913–925`, `internal/chat/provider.go:1005–1033`). The session does call its diagnostic routine, but `diagnostics.expectedOnly` recursively recognizes the cancellation-only wrapped/joined chain and suppresses it (`internal/diagnostics/diagnostics.go:404–427`). Therefore **the real provider cancellation does not already emit an ERROR describing the broken stream**. The handler's deferred stream diagnostic is needed.

The handler joins the returned cancellation chain to the original encode/flush error before logging. A broken pipe remains a nonexpected leaf, so the joined record is not suppressed. The new `providerFailure.DiagnosticFields` supplies safe status/phase even when the handler is the logging owner. The original `agent.chat.stream_encode` / `agent.chat.stream_flush` operations and bounded stream event/phase remain available. This produces one ERROR record for the observed stream failure on the current real-provider cancellation path.

The conditional join does not hide an independent noncancellation session failure: the real session logs such failures at its existing boundary. If writing the subsequent user-facing error event also fails, that is a separate stream failure with its own transport diagnostic. Conversely, immediately after a failed tool observer the current provider returns cancellation before entering another failure-producing action; there is no demonstrated independent cause discarded by the conditional join.

Two limits are nonblocking for this implementation. The stream diagnostic waits for the session to return, so a hypothetical provider that ignores cancellation indefinitely would delay it; the current provider's synchronous cancellation check does not have that behavior at this boundary. A future return containing both cancellation and an independent failure could be logged by the session and joined again by the stream handler; no such return is produced by the reviewed observer-cancellation path. These are not grounds for speculative blockers or a logging redesign here.

Provider metadata can append another `phase` field to the stream record (and repeat provider phase/status at existing session boundaries). This is bounded metadata in one record, not duplicate ERROR emission. The stream operation and event still identify the failure. Consumers must not interpret a provider `post-stream` phase as the stream event's phase.

## Migration journal and certainty

- `internal/migration/oneshot.go:243–273,329–335`: entry records `pending`; the client-dispatch record is immediately before `HTTPClient.Do`; the state changes to `returned` immediately after it returns. These are local client lifecycle observations. They do not establish remote receipt, server creation, absence of creation, or billing outcome. A returned transport failure still produces `creation_state=unknown`, including the lost-response case.
- The deferred result uses the actual named return. The decoded result now uses `result =`, avoiding shadowing. Explicit early returns assign the named result before the defer runs; sanitizing return expressions likewise become the deferred value. The local `fail` closure has its own returned value and does not shadow the outer result. No stale zero-value success journal was found.
- Handoff delivery is separate from creation. `delivered` is set only after the split delivery callback returns nil. Validation, recipient-change, and delivery failures retain `created` evidence where established and now include `stage=handoff`, `code=handoff_failed`, and `handoff_delivery=failed`. That failure label does not prove neither handoff part reached the recipient. A result without a handoff attempt records `not_attempted`; it is not delivery confirmation.
- The scheduled review predicate already requires both `created` and `delivered` for success (`cmd/goaster-agent/generic_cron.go:206`). The added created-without-handoff regression checks that this result needs review and is not replayed. No retry, replay, persistence, or remote-action machinery is added by this diff. The journal cannot be used as an idempotency or billing ledger.

## Secret safety and bounds

`diagnostics.Info` accepts at most 16 fields, emits only fixed allowlisted keys/values, and bounds/sanitizes the operation through the existing token function. It has no arbitrary error-text or result serialization path. Migration result logging uses `SanitizeOneShotResult` and emits only creation state, code, stage, dispatch state, and handoff state. It does not emit server identifiers/names, setup URLs, private keys, tokens, prompts, request/response bodies, or tool output.

The stream record includes fixed event/phase labels and the existing diagnostic cause walker, not event contents or arbitrary error messages. The added provider fields expose only phase and validated numeric HTTP status. No new secret/private-content logging path or control/newline injection path was found. The existing output mutex serializes complete INFO and ERROR writes.

## Regression validity and scope

The new handler test meaningfully covers initial-notice encode and flush failures: no session call, no side effects, one broken-pipe diagnostic, bounded event/phase, and no late final/error event. Both table cases are initial-notice cases; the unused noninitial branch in the fixture is not evidence of handler-level tool-observer coverage.

The existing real-provider fixture cancels from the actual tool observer before first and subsequent actions and checks action counts, scheduling/review behavior, and absence of replay. The new assertions verify retained provider status/phase. Combined with inspection of the handler's immediate `cancelStream` call, this supports the integration, although there is no new end-to-end broken-writer → real-provider diagnostic-count test. The current call graph and cancellation filter resolve that logging question directly; the coverage limit is not a demonstrated defect.

The lost-response migration regression checks all three lifecycle records and conservative unknown creation, while existing assertions retain request-count checks. The handoff regression checks the named-result journal after successful decoding and failed delivery, which would detect the shadowing bug. The already-logged delivery fixture now permits INFO records while continuing to reject duplicate ERROR records. The diagnostics test rejects arbitrary lifecycle values and private fields. The cron test verifies that created-without-delivery remains terminal and review-required.

The production changes stay within stream cancellation, bounded diagnostic metadata/journaling, named-result handling, and handoff stage accuracy. No additional scope or required fixes identified.
