# Migration

The bundled workflow is a four-step private journey: check `HCLOUD_TOKEN` in
protected `~/.goaster/env` before process fallback; choose an exact CX/CAX,
optional compatible locations, and an informational official rough price;
propose a cryptorandom `goaster-migration-[0-9a-f]{16}` name and obtain one
spending approval; then create and hand off safely. Before requesting approval,
MUST check the latest official public rough pricing on a best-effort basis and
explicitly report unknown if unavailable. Missing-token setup is
`/get-env`, local `goaster.env` editing, same-message `/env` attachment, and
exact `ACCEPT`. Secrets never enter model context or logs.

The active one-shot `goaster_migration` tool accepts only `action: "create"`, exact
`server_type`, exact `server_name`, and optional strict `locations`. The
runtime loads the protected provider token, generates the key internally, and
keeps credentials, request bodies, setup URLs, private keys, and provider
responses out of safe model results, cron messages, logs, and local
persistence. Confirmed success sends a key-only private attachment and a
separate clickable setup link to the revalidated paired owner.

If availability is explicitly needed, use only token-safe read-only
`GET /v1/server_types?name=<url-encoded-exact-type>`, match `server_types[]`,
and inspect `locations[].available`. This is a snapshot, not a reservation or
capacity guarantee. Do not use the retired generic `GET /v1/datacenters`
endpoint (removed 2026-10-01) or invent another availability endpoint.
A token-safe read-only direct provider API request through shell is allowed;
keep credentials out of arguments, tracing, output, and model context, and
return only needed safe fields. Public rough prices are informational only, not a
target, ceiling, ranking, or billing discussion.

Only an immediate top-level `creation_state: "not_created"` with
`code: "no_capacity"` creates one ordinary generic cron job with literal
`action=create/server_name/server_type/locations`, `every_seconds: 1800`, and
`max_runs: 1`; the returned job ID is recorded. The same job silently waits on
the exact no-capacity result. A scheduled turn cannot add another job. The
scheduled-only `cron_disable_self` capability uses the runtime-bound job and
conversation/topic identity, accepts no target ID, and remains available after
the migration create begins. Confirmed delivered success, `target_exists`,
uncertainty, partial handoff, and other errors disable/report as appropriate;
an existing target is never adopted. Existing files from older installations
are not read, deleted, migrated, or resumed by this runtime.
