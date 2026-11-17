# Authentication access

Obtain the exact `HCLOUD_TOKEN` variable through `goaster-env`. The migration
workflow checks protected `<real user home>/.goaster/env` first, then falls
back to process `HCLOUD_TOKEN`. If protected settings cannot be read, fail
closed. Token presence does not prove that it authenticates successfully.
When it is missing, use the private Telegram `/get-env` and `/env` process in
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

Use authenticated API requests for actual provider availability and creation.
A safe read-only request can answer a needed clarification, but API pricing is
not a required pricing, discovery, or preapproval gate. Public Hetzner Cloud
pages are the source for indicative catalogue and rough public price
information. Authentication does not prove project identity; do not invent a
project metadata check. If authentication fails, stop auth-dependent work.
Emit one bounded ERROR diagnostic with safe operation and status context at
runtime failure boundaries, as repository rules require.

This skill does not authorize resource mutations or costs.
