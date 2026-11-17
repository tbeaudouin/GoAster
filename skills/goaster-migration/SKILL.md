---
name: goaster-migration
description: Create one exact new Hetzner Cloud server after private approval and deliver its setup privately.
---

## 1. Check access privately

Use this workflow only in the paired private Telegram conversation. Before
claiming that `HCLOUD_TOKEN` is missing, actually check it without exposing its
value: read the protected `<real user home>/.goaster/env` first, then use the
process `HCLOUD_TOKEN` only as fallback. If protected settings cannot be read,
fail closed. Never ask for a pasted token. If it is missing, tell the owner to
send `/get-env`, edit the local `goaster.env`, attach that file with `/env` in
the same message, review the names-only preview, and reply exactly `ACCEPT`.
Recheck the protected environment after acceptance. Keep the token, request
headers, bodies, responses, setup URL, and private key out of model context,
tool results, chat, logs, and extra local copies.

## 2. Choose the exact server

Ask what exact server choice or help is needed. Prefer a suitable CX or CAX for
the workload; use CAX only when the software supports ARM. Locations are
optional: omit `locations` or use `[]` to allow any compatible location. A
user-supplied location list is strict and must not be widened.

If asked for availability, use only the token-safe read-only
`GET /v1/server_types?name=<url-encoded-exact-type>` request. Match the exact
type in `server_types[]` and inspect its `locations[].available` values. This is
a snapshot, not a capacity guarantee. Do not use retired datacenters or the
retired generic `GET /v1/datacenters` endpoint (removed 2026-10-01) or invent another availability endpoint.
A direct provider API request through shell is allowed when it is read-only
and token-safe: keep credentials out of arguments, tracing, output, and model
context; return only the needed safe availability fields.

Before requesting approval, you MUST check the latest official public Hetzner catalogue
information for a rough price, with source, currency, and date when available.
Make a best-effort check; if unavailable, explicitly report the price as unknown. Price is informational
only: it is not a target, ceiling, ranking, or billing discussion, and it is
not an API prerequisite.

## 3. Propose and approve one exact create

Propose one cryptographically random name matching
`goaster-migration-[0-9a-f]{16}`, the exact server type, the exact location
choice, and the checked official public rough price (or explicitly unknown
if the best-effort check was unavailable). Obtain one private
spending approval for those exact values. That approval covers the immediate
first create and any capacity-only retries every 30 minutes. Do not ask the
owner to choose now versus waiting, and do not change the approved name, type,
or locations.

## 4. Create, retry, and hand off safely

For the first create, call `goaster_migration` with exactly
`action: "create"`, the approved exact `server_name`, `server_type`, and
`locations`. The ordinary
retry job, if needed, must use the same literal action and fields in its
message, with `every_seconds: 1800` and `max_runs: 1`; it contains no token or
other secret. If the immediate result has the top-level pair
`creation_state: "not_created"` and `code: "no_capacity"`, create one such
ordinary recurring job once and record its returned ID. Never recursively
schedule another job; never add a scheduled job from a scheduled turn.

On that exact no-capacity result in a scheduled turn, do nothing and wait for
the same job; do not report or create another job. On confirmed delivered
success, tell the owner to use the separate private messages and, when running
from the retry job, disable self with the no-argument `cron_disable_self` tool.
For exact `target_exists`, disable self and report it without adopting the
existing server. Disable self and report every other error, uncertainty, or
partial handoff. Never replay a create or resend handoff after those outcomes.

The runtime generates keys internally. Only after validated confirmed success
does it send a key-only private attachment and a separate clickable setup link
to the revalidated paired owner, revalidating the paired recipient before each
delivery. The model sees safe status only; it never sees the key, URL, token,
request body, or provider response.
