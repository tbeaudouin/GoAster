# Required migration name: offline validation, November 17, 2026

## Scope and result

PASS: every active immediate and scheduled create requires `server_name` in the existing `goaster-migration-[0-9a-f]{16}` format. Tool schema and runtime validation reject omitted, empty, whitespace-only, null and malformed names. Direct one-shot validation precedes recipient/token/handoff callbacks, key generation and HTTP. The request always includes the name, and success requires the returned server label to match it. Retired request formats and stored records remain compatible and untouched.

The bundled guidance generates/proposes one name before normal conversational approval and reuses it in immediate creation and ordinary cron instructions. No separate naming workflow, approval record, scheduler or custom state was added. Same-name/type/location adherence across model turns remains instruction-led, not independently bound. Existing no-capacity-only retry, terminal uncertainty and private split handoff are preserved.

## Parent-run checks on final source

All PASS with `GOPROXY=off`:

- `/usr/local/go/bin/go test -count=1 ./...`
- `/usr/local/go/bin/go vet ./...`
- `/usr/local/go/bin/go test -race -count=1 ./internal/migration ./internal/chat ./internal/telegram ./cmd/goaster-agent`
- `bash packaging/test-skill-assets.sh`
- `git diff --check`

Terra review and subsequent final Sol review are SATISFIED. Their reports record coverage limits and the accepted instruction-led constraint. The generation-after-approval ambiguity found in the first review was corrected before final validation.

## Remaining live gaps

No provider/Redis/SSH/browser/Telegram live integration was exercised; no real cron or paid server was created. The running GoAster binary and existing user-owned installed skills were not updated. Publication is source delivery only and does not establish installed behavior. GoAsterWeb is tested separately in its repository.
