# Final migration diagnostics security/correctness review — 2026-11-17

Result: **CONCRETE ISSUES — two confirmed correctness findings.**

Read `agents.md`, `CONTRIBUTING.md`, and the latest accepted Terra review (`docs/migration-error-fixes-terra-accepted-20261117.md`), its preceding reviews, and validation report. Reviewed all tracked modifications and untracked implementation, tests, and review documents present at the start. Source remained read-only; this report is the only repository file written.

## Findings

1. **High: response validation still discards an explicit `created` outcome.** At `internal/migration/oneshot.go:306–308`, `target_exists` and `provider_lookup_failure` require `not_created`; any other state returns a fresh `unknown`/`response_unusable` result. For example, HTTP 409 with the following decoded response passes the initial envelope validation and sanitizer, then loses the explicit creation evidence:

   ```json
   {"creation_state":"created","server_id":42,"server_label":"goaster-migration-0123456789abcdef","error":{"code":"target_exists","stage":"provider","http_status":409}}
   ```

   An offline probe using an entirely fake HTTP transport returned `unknown`/`response_unusable`, with the server identity also discarded. The earlier error-envelope check at lines 293–294 likewise discards `created` when status/error metadata is missing or inconsistent. These branches predate parts of this change, but remain in the reviewed response path and violate the requested invariant and updated contract that explicit `created` survives contradictory metadata. The sanitizer cannot restore evidence already replaced before tool output and checkpointing. Preserve recognized creation evidence while rejecting inconsistent lookup/error details; keep handoff stopped. Add Web-decoder regressions for these branches, followed through tool output and persisted follow-up metadata. The existing regressions cover conflicting invalid-input and timeout codes, not these validation exits.

2. **High: a checkpoint failure discards the latest outcome even from the live session.** At `internal/chat/provider.go:849–858`, the store save runs before `s.migrationDiagnostics` is updated. On failure, lines 853–854 return immediately; the observer also returns before updating `turnMigrationDiagnostics` at line 873. A known `created` or `unknown` result is therefore lost from both retained in-memory collections. This exceeds the documented limitation that failed storage cannot guarantee restart durability.

   Reproduced sequence: save an initial `not_created` record; emit a new `created`/handoff-failed result; make the session directory temporarily unusable during the observer checkpoint, before file replacement; restore the original directory and file; ask a follow-up in the same live session. The failed turn correctly returned a checkpoint error, but the next turn proceeded successfully with only the old `not_created` record. Its successful save also retained only that stale metadata. Thus recovery of storage can leave a false latest no-create impression and permanently omit known creation evidence without a process restart. Retain the sanitized latest outcome in memory despite the save failure, preserve the error and stop model continuation, and ensure subsequent saves/reloads cannot silently overwrite that pending evidence. Add a pre-replacement failure/recovery/follow-up regression; the existing checkpoint failure test injects a directory-sync failure after replacement and does not inspect the next turn's outcome.

## Other reviewed boundaries

- Missing/unrecognized creation states normalize to `unknown`; HTTP 422/`invalid_input` does not infer `not_created`. The sanitizer preserves explicit `created` and `unknown` against the tested conflicting codes, and timeout codes make explicit `not_created` uncertain. Finding 1 concerns surrounding decoder validation.
- Under successful checkpoints, records append in occurrence order, retain the newest eight, survive failed model turns and restart, and render within 8 KiB including the prefix. Rendering trims details before discarding older records and preserves the latest outcome. The notice is charged against the model context budget. Finding 2 concerns checkpoint failure before replacement.
- New provider detail/log/metadata paths use fixed allowlists and bounded fields. Retained diagnostics exclude resource details, arguments, generic/full tool outputs, private setup URLs, keys, and tokens. No additional disclosure was identified in these changes. Existing session files without the additive metadata field remain loadable.
- Reviewed normal create, scheduled create/claim and repeated-attempt guards, scheduled cron mutation guards, private conversation/transport owner checks, and split private handoff/recipient revalidation. No additional actionable regression was identified. Guidance/package changes preserve credential boundaries and do not authorize creation for availability or pricing checks.

## Offline verification

- `git diff --check` — passed before this report was written.
- `sh packaging/test-skill-assets.sh` — passed.
- `GOTOOLCHAIN=local GOPROXY=off GOSUMDB=off /usr/local/go/bin/go test ./... -count=1 -timeout 300s` — passed.
- `GOTOOLCHAIN=local GOPROXY=off GOSUMDB=off /usr/local/go/bin/go vet ./...` — passed.
- Two additional assertions ran with `go test -overlay=… ./internal/chat -run '^TestFinalReview' -count=1 -timeout 30s`, using Linux memory-backed descriptors for the overlay and test source; no probe source file was written to the repository. Both failed on the required invariants, confirming the findings:

  ```text
  TestFinalReviewCreatedLookupContradiction:
    explicit created became "unknown" (response_unusable)
  TestFinalReviewCheckpointFailureRetainsLiveOutcome:
    live follow-up omitted created and retained stale metadata
  ```

The response probe used a fake transport with synthetic credentials and no network. The checkpoint probe used only temporary test storage. Existing suite tests use offline fakes/local test servers. No external network, real secrets, GitHub, deployment, commits, or background jobs were used. External provider guarantees and the stated availability endpoint/retirement date were not independently verified offline. Existing filesystem durability and the inability to checkpoint before the handler returns remain limitations; neither accounts for losing an already observed result in live memory.

Final disposition: **CONCRETE ISSUES: preserve `created` through response validation and retain the latest live outcome across checkpoint failure.**
