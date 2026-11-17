---
name: hetzner-auth
description: Safely use a protected Hetzner Cloud token for authenticated provider requests.
---

Use this skill when authenticated access to Hetzner Cloud is needed. For token availability and missing-token setup, follow `goaster-env`. The variable is exactly `HCLOUD_TOKEN` (uppercase). The migration workflow checks protected `<real user home>/.goaster/env` first and process `HCLOUD_TOKEN` only as fallback. If protected settings cannot be read, fail closed rather than bypassing them. Token presence does not establish that authentication succeeds.

Keep the token in memory only. Never expose it in model context, logs, chat, command-line arguments, new secret files or copies, or diagnostics. The existing approved protected env file remains a credential source. If documenting the header shape, use only the schematic `Authorization: Bearer <token>`; never substitute a real value.

Use HTTPS requests only to the fixed origin `https://api.hetzner.cloud`. For an authenticated request, load the exact token through `goaster-env`, place it only in the in-memory Bearer header, set a finite timeout and bounded response body, disable redirects and verbose tracing, and filter parsed responses to the safe fields needed. Discard raw bodies. Never shell-source the env file or pass the token as a curl argument. Never print an authorization header, provider response, or raw provider error.

When a current read-only availability clarification is actually needed, request
only `GET /v1/server_types?name=<url-encoded-exact-type>`. Match the requested
server type in `server_types[]` and inspect its `locations[].available` values;
apply any requested location list strictly. Do not use the retired generic
`GET /v1/datacenters` endpoint (removed 2026-10-01), shell around the API, or
invent a migration availability tool or endpoint. This is a bounded snapshot,
not a reservation or capacity guarantee. Keep the token and Authorization
header out of chat, model context, logs, and command output.

The API is appropriate for actual provider availability and creation. An ordinary read-only API check may be used when a specific clarification needs it, but public Hetzner pages are the source for indicative catalogue descriptions and rough public prices; API pricing is not a required discovery, pricing, or preapproval step. Do not imply that public figures are API figures or vice versa. A read-only API auth check can establish whether the token is accepted, but does not prove project identity. Do not invent a project metadata check. For existing authenticated target work, verify the target independently and stop if it is ambiguous. If authentication fails, stop auth-dependent work. At runtime failure boundaries, emit one bounded ERROR diagnostic with safe operation and status context as required by repository rules; never include secrets, raw bodies, or unsafe error text.

For reference, the official [Cloud API documentation](https://docs.hetzner.cloud/reference/cloud) describes the API. Do not mutate resources or invoke migration merely to produce a quote. Public rough prices remain informational and do not require an API pricing request or establish capacity. Authentication alone does not authorize creating resources or incurring costs; obtain the required approval separately.
