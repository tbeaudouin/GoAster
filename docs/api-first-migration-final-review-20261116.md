# API-first migration final independent review — 2026-11-16

**BLOCKED — three concrete release blockers remain.** The Terra review's SATISFIED conclusion is not sufficient for this snapshot. The provisioning and private handoff paths pass the existing offline tests, but active legacy guidance and runtime diagnostics do not meet the requested acceptance criteria, and the package asset check fails.

Read `agents.md` first, then [the Terra review](api-first-migration-terra-review-20261116.md) and [the exact Web contract](api-first-web-contract-20261116.md). Reviewed all seven tracked modified files and all five pre-existing untracked migration files reported by `git status`, plus their actual runtime, skill-loading, Telegram, diagnostics, scheduling, and packaging dependencies. Only this review document was added. No application code or tests were changed; no live service, secret, GitHub, deployment, or paid action was used. HTTP tests used local test servers.

1. **P2: Legacy migration instructions still enter the active model context.**

   [skills/goaster-migration/AUTH.md](../skills/goaster-migration/AUTH.md), lines 3–9, says migration scheduler resumption is enabled and describes the former machine API/Redis dependency. Lines 25–26 require terminal disabling of the source monitor. Lines 38–41 require preserving a protected migration task directory for manual cleanup. These contradict the new one-shot, ephemeral flow and the replacement `SKILL.md`. [META.md](../skills/goaster-migration/META.md), line 2, still advertises a 30-minute availability monitor as part of the migration workflow. The public [README](../README.md), lines 3–9, also promises scheduler resumption.

   This is executable guidance, rather than merely historical documentation: [internal/artefacts/discovery.go](../internal/artefacts/discovery.go), lines 60–76, discovers `META.md`; lines 110–124 load and concatenate `AUTH.md` with `SKILL.md`. [internal/chat/skill_tool.go](../internal/chat/skill_tool.go), lines 152–162, exposes that content through `load_skill`. [internal/chat/openai.go](../internal/chat/openai.go), lines 1590 and 1615, explicitly tells the model to continue honoring legacy AUTH requirements. The starter installer copies all three files ([internal/artefacts/skills.go](../internal/artefacts/skills.go), line 160).

   A newly installed agent therefore receives incompatible instructions about monitoring and retained key material when using the new flow. The runtime rejects legacy actions, so this finding does not claim that a retired scheduler is actually started. Release requires updating the bundled META/AUTH guidance and current README to the one-shot contract. Existing user-owned skills are deliberately not overwritten by installation; document how their owners can reconcile the changed workflow without silently rewriting those files. Do not restore persistence or automatic creation to satisfy the stale text.

2. **P2: A lost create response is silently excluded from ERROR logging.**

   [internal/migration/oneshot.go](../internal/migration/oneshot.go), lines 115–117, correctly returns `unknown` for a transport failure after dispatch, but its `fail` closure at lines 65–69 relies on `diagnostics.Error`. [internal/diagnostics/diagnostics.go](../internal/diagnostics/diagnostics.go), lines 41–43 and 261–264, recursively unwraps errors and suppresses every cause ending in `io.EOF`. When the Web connection closes before returning the create response, the wrapped HTTP EOF is consequently treated as normal EOF and no ERROR is written. This is an uncertain billable operation, not normal end-of-input.

   Reproduced using the existing offline regression:

   ```sh
   GOTOOLCHAIN=local GOPROXY=off GOSUMDB=off /usr/local/go/bin/go test -count=1 ./internal/migration -run '^TestOneShotBoundaries$/^lost_response$' -v
   ```

   It passes, asserts exactly one received request and `creation_state=unknown`, and emits **no ERROR record**. The fake handler closes the connection at [internal/migration/oneshot_test.go](../internal/migration/oneshot_test.go), lines 135–138. The test currently checks the result and call count, not the required diagnostic.

   Release requires marking this HTTP failure as an unexpected operation failure while preserving the wrapped EOF and emitting one bounded ERROR with safe creation state/code context. Preserve the normal cancellation and normal EOF exceptions.

   The same new boundary also needs its diagnostic ownership/context corrected: line 164 logs service errors with only HTTP status despite already having sanitized stage, code, provider code, and creation state. The existing created-error test prints only `ERROR operation=migration.create status="500" causes="*errors.errorString"`, losing the available handoff stage and confirmed creation state. On real Telegram delivery failure, [internal/telegram/adapter.go](../internal/telegram/adapter.go), `telegramFailure`/`callMultipart`, already logs the complete safe cause; [internal/telegram/migration.go](../internal/telegram/migration.go), lines 107–108, substitutes a generic error, which the new caller logs again at `oneshot.go:209`. Keep one owner for that diagnostic and preserve the cause rather than adding a second generic record. These paths violate the explicit `agents.md` safe/useful, single-boundary logging rule; arbitrary error bodies or URLs must remain excluded.

3. **P2: The offline package asset validation fails on the migration edits.**

   Reproduced from the repository root:

   ```sh
   TMPDIR="$PWD" sh packaging/test-skill-assets.sh
   ```

   Exit status **1**, with:

   ```text
   goaster-migration workflow missing required guidance: 'source-side `goaster_migration` tool'
   ```

   [packaging/test-skill-assets.sh](../packaging/test-skill-assets.sh), lines 73–142, still requires the old availability/create routes, operation capability, monitor behavior, and retained private-key files in both source and installed workflow text. Lines 149–198 similarly require the deleted legacy documentation. This is an existing offline package validation regression, independent of the passing Go suite. Release requires migrating these assertions to the API-first contract and validating the installed META/AUTH/SKILL combination. Keep meaningful checks for owner privacy, conversational duplicate warning, no scheduled creation, and no secret persistence; do not reintroduce the retired workflow simply to make the assertions pass.

The following reviewed behavior does not add another blocker:

- Production `serve` registers `migration.CreateOnce` and does not start `newMigrationScheduler` or `startMigrationRuntime`. The remaining legacy implementation is inactive through that entry point; its dedicated scheduler path is separate from ordinary cron. New creation does not read or write migration records, retained retries, or monitor state. Dead legacy helpers alone are not a release finding.
- The model tool accepts only `create`, exact type, and optional locations. Private, interactive transport context and a positive conversation ID are required; `CreateOnce` checks that ID against the paired owner before dispatch. Telegram validates the recipient again at delivery. Explicit approval and duplicate-billing warnings are conversational requirements, as intentionally requested, not a newly required persisted authorization mechanism.
- The request uses the exact `/api/migration/create` route, `hcloud_token`, `server_type`, optional strict `locations`, and an Ed25519 `login_public_key`, with a fresh 32-byte unpadded-base64url bearer secret. The production client uses HTTPS, no proxy, no redirects, and one POST without an application retry loop. Architecture/image selection remains with Web. Tests verify one received request for lost responses and redirects, strict location forwarding, and matching delivered private/public keys.
- Model results exclude the private key and setup URL, scrub reflected request credentials, bound response fields, and parse IP addresses. Private handoff checks configured URL scheme/host, absence of URL userinfo, and the generated fragment secret. Owner changes, bad origin/secret, and delivery failure prevent handoff and preserve `created` plus safe reconciliation fields. A service error explicitly confirming creation also preserves `created`. Lost/unusable responses remain `unknown`; no automatic resend or create retry follows.
- `no_capacity` is retained as a tool result code. The new skill describes indicative pricing, optional strict locations, duplicate-charge warning and fresh approval, and read-only ordinary cron notifications. Its explicit lack of automatic eventual creation is an accepted conservative gap. That intentional gap, absent persistence, and optional account inspection are not blockers.
- No additional concrete credential disclosure or provisioning correctness blocker was established. This is an offline source review against the supplied contract; it does not certify the separate Web implementation or live Telegram delivery.

Independent validation used the installed Go **1.25.9** binary with `GOTOOLCHAIN=local GOPROXY=off GOSUMDB=off`:

| Check | Result |
| --- | --- |
| `/usr/local/go/bin/go test ./...` | Passed |
| `/usr/local/go/bin/go vet ./...` | Passed |
| `/usr/local/go/bin/go test -race -count=1 -timeout=180s ./internal/migration ./internal/chat ./internal/telegram ./cmd/goaster-agent` | Passed |
| Existing one-shot failure/boundary tests, uncached and verbose | Passed; exposed the missing EOF diagnostic and sparse service-error context |
| `TMPDIR="$PWD" sh packaging/test-skill-assets.sh` | Failed as reproduced above |
| `git diff --check` | Passed |

The default `/usr/bin/go` is 1.22.2 and could not compile this Go 1.25 module; invoking the already installed toolchain resolved that local check limitation without downloading anything. Passing Go checks do not resolve the three blockers above. **Final disposition: BLOCKED.**
