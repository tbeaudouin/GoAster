# Authentication and pricing access

Obtain the exact `HCLOUD_TOKEN` variable through `goaster-env`. The current
migration lookup reads protected `<real user home>/.goaster/env` first, then
falls back to process `HCLOUD_TOKEN`. If protected settings cannot be read,
stop rather than bypassing them. Keep credentials in memory only: never put
them in model context, logs, chat, command-line arguments, new secret files
or copies, or diagnostics. The existing approved protected env file remains
a credential source. A displayed header may only be the placeholder form
`Authorization: Bearer <token>`.

Use HTTPS at `https://api.hetzner.cloud`. Read-only price and discovery calls
are `GET /v1/pricing`, `GET /v1/server_types?name=<exact-type>`, and
`GET /v1/locations`. They return provider data, not a Web quote. Authenticated
provider list prices differ in source from the anonymous public website.
Pricing does not verify the selected project; do not invent a project
metadata API. If authentication fails, stop auth-dependent work. Informational
pricing may remain best-effort and use public documentation as fallback.

Use the in-process HTTP recipe in `hetzner-auth/SKILL.md`: load the token
through `goaster-env`, send it only in the in-memory Bearer header to the fixed
HTTPS origin, bound time and body size, disable redirects and verbose tracing,
filter parsed JSON to safe fields, and discard the raw body. Do not shell-source
the env file or put the token in a curl argument. Follow the official
[Cloud API documentation](https://docs.hetzner.cloud/reference/cloud) and
[hcloud-go pricing schema](https://github.com/hetznercloud/hcloud-go/blob/main/hcloud/schema/pricing.go).

Match `pricing.server_types[].name` and requested exact locations. Compute
prices use `pricing.server_types[].prices[].location`,
`pricing.server_types[].prices[].price_hourly.gross`,
`pricing.server_types[].prices[].price_hourly.net`,
`pricing.server_types[].prices[].price_monthly.gross`, and
`pricing.server_types[].prices[].price_monthly.net`. For IPv4 match
`pricing.primary_ips[].type` to `ipv4`; its corresponding fields are
`pricing.primary_ips[].prices[].location`,
`pricing.primary_ips[].prices[].price_hourly.gross`,
`pricing.primary_ips[].prices[].price_hourly.net`,
`pricing.primary_ips[].prices[].price_monthly.gross`, and
`pricing.primary_ips[].prices[].price_monthly.net`. Amounts are decimal
strings; currency, names and locations are ordinary strings, and the server
type ID is int64. Preserve price precision and report `pricing.currency`.
Gross includes VAT; net excludes it. Show available IPv4 estimates separately
from compute. For an empty location list, summarize matching per-location
prices or a range without narrowing the policy or ranking locations by price.
Return only allowlisted type, location, currency, hourly/monthly compute and
optional IPv4 estimates, and source timestamp. Never expose raw provider
listings, bodies, or errors. Emit loud, bounded ERROR diagnostics with safe
operation and status context at runtime failure boundaries as repository rules
require. Do not mutate resources or invoke migration for a quote.

This skill does not authorize snapshots, server changes, or costs.
