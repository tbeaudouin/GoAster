# Migration

The active `goaster_migration` tool accepts only `action: "create"`, the
required exact `server_type`, the required exact
`goaster-migration-[0-9a-f]{16}` `server_name`, and optional strict
`locations`. It is available only in a paired private conversation. The
one-shot source loads the protected provider token, generates an ephemeral Ed25519 key
and owner secret, sends one HTTPS create request, and delivers the setup URL
and key as separate private handoff parts to the revalidated paired owner.

Public server discovery and rough pricing belong to the normal public
search/fetch tools and the migration skill. They are informational and do not
gate the create call. The Web endpoint performs its own admission, exact-name
lookup, capacity, and provider sequence. The source does not expose provider
credentials, setup URLs, private keys, or request bodies to the model, cron
messages, logs, or local persistence.

If an authenticated availability clarification is actually needed, use only
the read-only `GET /v1/server_types?name=<url-encoded-exact-type>` request,
match the requested type in `server_types[]`, and inspect its
`locations[].available` values. This is a bounded snapshot, not a reservation
or capacity guarantee. Do not use the retired generic `GET /v1/datacenters`
endpoint (removed 2026-10-01), shell around the API, or invent a migration
availability tool or endpoint. Keep the token and Authorization header out of
model context, chat, logs, and command output. Public rough prices remain
informational and are not an API pricing prerequisite.

An eventual request uses the ordinary generic `cron` tool with the approved
exact name, type, and locations in its message, `every_seconds: 1800`, and
`max_runs: 1`. A claimed turn can call create once and cannot add another
job. Only an explicit `creation_state: "not_created"` with `code:
"no_capacity"` rearms the job. A `target_exists` result stops normally without
creating another server; all other outcomes, including an uncertain response
or incomplete handoff, stop for safe private reporting and manual
reconciliation. Existing files from older installations are not read,
deleted, migrated, or resumed by this runtime.
