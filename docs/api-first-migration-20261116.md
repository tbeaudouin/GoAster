# One-shot migration integration

The active `goaster_migration` tool accepts only `action: "create"`, exact
`server_type` and exact `server_name`, plus optional strict `locations`. A
paired private conversation supplies approval and the owner identity. The
source sends one request to `POST /api/migration/create`, keeps credentials
and private handoff material out of model-visible results, and uses the
split Telegram handoff for the setup URL and `id_ed25519` key attachment.

Waiting uses the ordinary generic cron tool: the approved name, type, and
locations remain in the message, the interval is 1800 seconds, and
`max_runs` is 1. The scheduler grants one create call per claimed turn and
rearms only for the exact `not_created` / `no_capacity` result. Success,
`target_exists`, uncertainty, provider lookup failure, handoff failure, and
all other results are terminal. A later interactive attempt requires the
normal duplicate-charge warning and fresh approval.

Public catalogue discovery and rough pricing are normal informational
search/fetch work. They are not an authenticated provider prerequisite or a
price-approval field. Existing files from older installations are not read,
deleted, migrated, or resumed by this source runtime.

Offline validation uses local HTTP and delivery fakes; no live provider,
Telegram, Web, deployment, or cloud operation is part of this repository's
tests.
