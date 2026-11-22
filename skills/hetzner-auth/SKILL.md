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
`GET /v1/datacenters` endpoint (removed 2026-10-01) or
invent a migration availability tool or endpoint. A token-safe read-only direct
provider API request through shell is allowed; keep credentials out of arguments,
tracing, output, and model context, and return only needed safe fields. This is a bounded snapshot,
not a reservation or capacity guarantee. Keep the token and Authorization
header out of chat, model context, logs, and command output.

The API is appropriate for actual provider availability and creation. An ordinary read-only API check may be used when a specific clarification needs it, but public Hetzner pages are the source for indicative catalogue descriptions and rough public prices; API pricing is not a required discovery, pricing, or preapproval step. Do not imply that public figures are API figures or vice versa. The token scopes its provider project. For existing target work, authenticate and use returned server evidence and local identity to verify the selected target; stop if the server is absent or remains ambiguous after selection in the existing approval. Do not ask for or invent a project name or ID, console screenshot, independent access proof, project metadata, or user questionnaire. If authentication fails, stop auth-dependent work. At runtime failure boundaries, emit one bounded ERROR diagnostic with safe operation and status context as required by repository rules; never include secrets, raw bodies, or unsafe error text.

For reference, the official [Cloud API documentation](https://docs.hetzner.cloud/reference/cloud) describes the API. Do not mutate resources or invoke migration merely to produce a quote. Public rough prices remain informational only, not a target, ceiling, ranking, or billing discussion; they do not require an API pricing request or establish capacity. Authentication alone does not authorize creating resources or incurring costs; obtain the required approval separately.

For migration approval, you MUST check the latest official public rough pricing
before requesting approval. Make a best-effort check and explicitly report
unknown if unavailable; no authenticated API pricing request is required.
