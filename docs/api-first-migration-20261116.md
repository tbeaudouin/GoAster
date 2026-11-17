# One-shot migration integration

The active `goaster_migration` tool accepts only `action: "create"`, exact
`server_type` and exact `server_name`, plus optional strict `locations`. A
paired private conversation supplies approval and the owner identity. The
source sends one request to `POST /api/migration/create`, keeps credentials
and private handoff material out of model-visible results, and uses the
split Telegram handoff for the setup URL and `id_ed25519` key attachment.

Only an immediate top-level `not_created` / `no_capacity` result creates one
ordinary generic cron retry: the approved name, type, and locations remain in
the literal message, the interval is 1800 seconds, and `max_runs` is 1. The
scheduler grants one create call per claimed turn and silently waits on the
same exact no-capacity result. Success, `target_exists`, uncertainty, provider
lookup failure, handoff failure, and all other results disable/report as
appropriate; no new approval or duplicate retry is invented by the runtime.

Public catalogue discovery and rough pricing are normal informational
search/fetch work. Before requesting approval, MUST check the latest official
public rough pricing on a best-effort basis and explicitly report unknown if
unavailable. This is not an authenticated provider API pricing prerequisite. Existing files from older installations are not read,
deleted, migrated, or resumed by this source runtime.

Offline validation uses local HTTP and delivery fakes; no live provider,
Telegram, Web, deployment, or cloud operation is part of this repository's
tests.
