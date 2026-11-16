# Hetzner authentication and indicative pricing

Use this skill when GoAster needs read-only access to Hetzner Cloud or needs
indicative server pricing. For credential access, follow `goaster-env`. The
credential variable is exactly `HCLOUD_TOKEN` (uppercase); do not use
`hc_token` or another spelling. The current migration lookup checks the
protected `<real user home>/.goaster/env` setting first and uses the process
`HCLOUD_TOKEN` only as fallback. If protected settings cannot be read, stop
and report a safe configuration error rather than bypassing them.

Keep the token in memory only. Never expose it in model context, logs, chat,
command-line arguments, new secret files or copies, or diagnostics. The existing
approved protected env file remains a credential source. Never print an
authorization header or provider response. If documenting the header shape, the only
permitted schematic is `Authorization: Bearer <token>`; `<token>` is a
placeholder and must never be replaced with a real value in displayed text.

Use HTTPS requests to `https://api.hetzner.cloud`. Read-only authentication
can verify that the credential is accepted, but pricing data does not verify
which project is selected or establish project identity. Do not invent a
project metadata check. For existing authenticated target work, verify the
target independently; if authentication fails or the target is ambiguous,
stop. A read-only auth failure stops auth-dependent work. Informational
pricing can remain best-effort and fall back to public documentation.

For indicative provider list prices, use the authenticated read-only
`GET https://api.hetzner.cloud/v1/pricing` endpoint. Discover locations with
`GET https://api.hetzner.cloud/v1/locations`; discover an exact type with
`GET https://api.hetzner.cloud/v1/server_types?name=<exact-type>` (URL-encode
the type query value). These provider responses are distinct from the
anonymous public Hetzner website, which may show different public prices or
availability. Do not attribute a website figure to the authenticated API or
vice versa. There is no Web quote endpoint: do not call one or imply that
pricing verifies project identity.

For a practical in-process request, load the exact `HCLOUD_TOKEN` through
`goaster-env` without displaying it, set the request's Authorization header to
`Bearer ` plus the loaded value in memory, and GET `/v1/pricing` only at the
fixed HTTPS origin above. Use a finite timeout and a bounded response body;
disable redirects so credentials cannot be forwarded, and disable verbose
tracing. Parse JSON in process, filter to safe fields, and discard the raw
body. Never shell-source the env file or pass the token as a curl argument.
Follow the official [Cloud API documentation](https://docs.hetzner.cloud/reference/cloud)
and [hcloud-go pricing schema](https://github.com/hetznercloud/hcloud-go/blob/main/hcloud/schema/pricing.go).

Match `pricing.server_types[].name` and each requested exact location. The
server type `pricing.server_types[].id` is an int64; names, currency, and
locations are ordinary strings. Compute price fields are
`pricing.server_types[].prices[].location`,
`pricing.server_types[].prices[].price_hourly.gross`,
`pricing.server_types[].prices[].price_hourly.net`,
`pricing.server_types[].prices[].price_monthly.gross`, and
`pricing.server_types[].prices[].price_monthly.net`. For IPv4, match
`pricing.primary_ips[].type` to `ipv4` and use
`pricing.primary_ips[].prices[].location`,
`pricing.primary_ips[].prices[].price_hourly.gross`,
`pricing.primary_ips[].prices[].price_hourly.net`,
`pricing.primary_ips[].prices[].price_monthly.gross`, and
`pricing.primary_ips[].prices[].price_monthly.net`. Price amounts are decimal
strings; preserve precision without binary floating-point rounding. Report
`pricing.currency` without assuming a currency. Gross includes VAT and net
excludes VAT. For an empty location list, summarize matching per-location
prices or a range without narrowing the location policy or ranking locations
by price. Do not silently substitute a type or location.

The current migration enables IPv4, so present a separate optional IPv4 price
estimate when an exact-location IPv4 price exists. Show compute and IPv4
separately; do not claim base compute is all-in. Return only an allowlisted
type, location, currency, hourly/monthly compute estimate, optional hourly/
monthly IPv4 estimate, and source timestamp. Never return raw authorization,
server listings, provider bodies, or error text. If a safe allowlisted result
cannot be produced, say pricing is unavailable without exposing the response
or error. At runtime failure boundaries, emit a loud, bounded ERROR diagnostic
with safe operation and status context as required by repository rules; never
include raw provider bodies or errors. Keep estimates indicative: do not
apply a price cap, rank types, or imply guaranteed availability. Do not perform resource mutations or invoke
migration to produce a quote.

Authentication does not authorize creating snapshots, changing servers, or
incurring costs. Obtain the required approval for those actions separately.
