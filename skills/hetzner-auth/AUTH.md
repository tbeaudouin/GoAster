# Authentication access

Obtain the exact `HCLOUD_TOKEN` variable through `goaster-env`. The migration
workflow checks protected `<real user home>/.goaster/env` first, then falls
back to process `HCLOUD_TOKEN`. If protected settings cannot be read, fail
closed. Token presence does not prove that it authenticates successfully.
When it is missing, use the private Telegram `/getenv` and `/env` process in
`goaster-env`; never ask for secrets in chat.

Keep credentials in memory only: never put them in model context, logs, chat,
command-line arguments, new secret files or copies, or diagnostics. The
existing approved protected env file remains a credential source. A displayed
header may only use the placeholder form `Authorization: Bearer <token>`.

Use HTTPS at the fixed origin `https://api.hetzner.cloud`. Load the token
through `goaster-env`, send it only in the in-memory Bearer header, bound
request time and response size, disable redirects and verbose tracing, filter
parsed JSON to safe fields, and discard the raw body. Do not shell-source the
env file or put the token in a curl argument. Never expose raw provider
responses or errors. Follow the official [Cloud API documentation](https://docs.hetzner.cloud/reference/cloud).

For a current read-only availability clarification, use only
`GET /v1/server_types?name=<url-encoded-exact-type>`, match the requested
server type in `server_types[]`, and inspect the requested type's
`locations[].available` values. Do not use the retired generic
`GET /v1/datacenters` endpoint (removed 2026-10-01) or
invent a migration availability tool or endpoint. A token-safe read-only direct
provider API request through shell is allowed; keep credentials out of arguments,
tracing, output, and model context, and return only needed safe fields. The result is a snapshot,
not a reservation or capacity guarantee; never expose the token or
Authorization header in chat, model context, logs, or command output.

Use authenticated API requests for actual provider availability and creation.
A safe read-only request can answer a needed clarification, but API pricing is
not a required pricing, discovery, or preapproval gate. Public Hetzner Cloud
pages are the source for indicative catalogue and rough public price
information; public rough prices are informational only, not a target, ceiling,
ranking, or billing discussion, and do not guarantee
capacity. The token scopes its provider project. Authenticate and verify the selected
server using returned server evidence and local identity; stop if it is absent or
remains ambiguous after selection in the existing approval. Do not ask for or invent a project name or ID, console screenshot,
independent access proof, project metadata, or user questionnaire. If authentication
fails, stop auth-dependent work.
Emit one bounded ERROR diagnostic with safe operation and status context at
runtime failure boundaries, as repository rules require.

This skill does not authorize resource mutations or costs.

For migration approval, you MUST check the latest official public rough pricing
before requesting approval. Make a best-effort check and explicitly report
unknown if unavailable; no authenticated API pricing request is required.
