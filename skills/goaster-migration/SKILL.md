GoAster migration

Purpose

Create one new Hetzner server with the approved name, server type and location choice, then privately send the owner what they need to complete setup on GoAsterWeb.

This skill does not copy data, use an existing destination server, stop or delete the old server, or automatically switch between agents. Other providers and existing servers require a separate skill.

Use this workflow only in the paired owner’s private Telegram conversation.

1. Check the Hetzner token

Check the protected <real user home>/.goaster/env first, then process HCLOUD_TOKEN.

Never claim that the token is missing or unreadable without actually checking. Never display the token or ask the user to paste it into chat.

If it is missing, tell the owner to:

1. Send /getenv.
2. Add HCLOUD_TOKEN locally to the downloaded goaster.env.
3. Attach that file with /env in the same message.
4. Review the preview and reply exactly ACCEPT.

After acceptance, recheck the protected environment.

Keep tokens out of conversation text, model input, tool output, logs and extra local copies.

2. Choose the server

a. Ask whether the user needs help

Ask whether they know which server they want or need help choosing.

Use information they have already provided rather than asking again.

b. Recommend a suitable type

Prefer suitable CX or CAX servers.

c. Check availability when asked

If the user asks which server types are currently available, use the shell tool to call Hetzner’s API directly with the protected token.

Check availability for the requested server type and locations. Being listed in the catalogue does not mean the type is currently available.

Use:
GET /v1/server_types?name=<requested-server-type>
Inspect:
server_types[].locations[].available
Do not use the retired datacenters endpoint.

For example, after loading HCLOUD_TOKEN into the shell’s protected process environment through the intended credential loader:
python3 - <<'PYTHON'
import json
import os
import urllib.error
import urllib.parse
import urllib.request

server_type = "cx33"
token = os.environ.get("HCLOUD_TOKEN", "")
if not token:
    raise SystemExit(
        "HCLOUD_TOKEN is not loaded; complete the protected token setup first."
    )

url = (
    "https://api.hetzner.cloud/v1/server_types?"
    + urllib.parse.urlencode({"name": server_type})
)
request = urllib.request.Request(
    url,
    headers={"Authorization": "Bearer " + token},
)

try:
    with urllib.request.urlopen(request, timeout=30) as response:
        data = json.load(response)

    types = data["server_types"]
    if not types:
        raise SystemExit(
            "Requested server type was not found; availability is unknown."
        )

    for item in types:
        print(json.dumps({
            "name": item["name"],
            "locations": [
                {
                    "name": location["name"],
                    "available": location["available"],
                }
                for location in item["locations"]
            ],
        }))

except urllib.error.HTTPError as error:
    raise SystemExit(
        "Hetzner availability request failed: HTTP " + str(error.code)
    ) from None
except (
    urllib.error.URLError,
    TimeoutError,
    OSError,
    ValueError,
    KeyError,
    TypeError,
):
    raise SystemExit(
        "Hetzner availability request failed; availability is unknown."
    ) from None
PYTHON

Change the non-secret server type as needed. Never insert the token into command text, print environment variables, enable shell tracing, or execute the environment file as shell code.

An availability check does not reserve capacity. If the check fails, report availability as unknown rather than unavailable.

d. Give a rough price

Before requesting approval, check Hetzner’s latest official public pricing and give a rough estimate for the chosen server type.

State the currency and any relevant location difference. If current pricing cannot be checked, say so rather than inventing an estimate.

Pricing is informational only. Do not send a target price, impose a price ceiling, or choose locations based on price.

e. Allow no location preference

The user may leave the location open. Omitted or empty locations allow GoAsterWeb to try any compatible available location.

If the user specifies locations, keep creation within those approved locations.

3. Get approval for one exact choice

Generate one cryptographically random server name in this format using normal shell tools:

goaster-migration-<16 lowercase hex characters>
Present:

- The exact server name.
- The exact server type.
- The location choice, including “any compatible available location” when there is no preference.
- The rough price estimate.
- An immediate creation attempt, followed by unlimited automatic attempts every 30 minutes after any migration error, including uncertain creation or incomplete handoff, using the same exact name, type and locations. GoAsterWeb checks the exact name before each create; an uncertain timeout still carries a residual duplicate risk.

Explain briefly that capacity can take days to become available. Do not promise a completion time.

Obtain one explicit spending approval in the paired private conversation covering these terms.

Always try immediately first. Do not ask the user to choose between creating now and waiting.

Keep the approved name, type and location choice unchanged throughout the attempts. A changed choice requires fresh approval.

4. Create or arrange a retry

a. Try immediately

Call goaster_migration with:

action: "create"
server_name: <exact approved name>
server_type: <exact approved type>
locations: <approved locations, or [] for no preference>
The create action generates its SSH key pair internally and sends only the public key to GoAsterWeb.

Do not generate or supply a separate SSH key. Do not place private keys in the conversation or cron instructions.

b. Successful creation

Only after confirmed successful creation and validation of the returned setup link, the create action automatically sends the paired owner:

- The private SSH key as an attachment.
- The clickable setup link in a separate private message.

The model receives creation and delivery status, not the private key or secret-bearing link.

When creation and delivery are confirmed, tell the owner to download the key and use the private setup link to finish setup on GoAsterWeb.

The same automatic private delivery applies to immediate and cron-triggered creation.

Do not claim that setup or migration is complete merely because the server was created.

c. Handle an existing target or arrange retries

If the immediate result is validated `target_exists` for the exact approved server name, report that the named server already exists and finish without creating a retry job. Do not adopt it or retrieve a key or setup link.

After any immediate migration error, including no capacity, timeout, provider lookup,
authentication or network failure, unavailable capability, unknown creation, or
incomplete handoff, create one ordinary recurring cron job, once, with a 30-minute interval, max_runs null or 0 (unlimited), and delete_after_run false. Do not set a final run cap.

Record its job ID. Keep the exact approved server name, type and locations. Do not create another job for the same request.

The job’s message must contain the actual approved values, not unresolved placeholders. It must not contain tokens, SSH keys or instructions to schedule another job.

Use the following wording, substituting the actual approved values and the job’s own ID:

Run goaster_migration once with these exact parameters:

action: "create"
server_name: "<approved server name>"
server_type: "<approved server type>"
locations: <approved location list, or []>

This job's ID is: <own job ID>.

Never create or schedule another cron job.
Never change the approved server name, type or locations.

Handle the result as follows:

1. If creation_state is "created" and handoff is "delivered":
   Disable this job using its own job ID.
   Tell the owner that the server was created and that the create
   action sent the SSH key attachment and separate private setup link.
   Tell them to use those private messages to finish setup on GoAsterWeb.

2. If code is "target_exists" for the exact approved server name:
   Disable this job.
   Report that the named server already exists.
   Do not adopt it or try to retrieve a key or setup link.

3. If creation_state is "not_created" and code is "no_capacity":
   Capacity unavailable means retry, not disable.
   Send no notification and make no cron changes.
   Wait for this same job's next scheduled run.

4. For every other error, uncertain creation outcome, or failed, partial or
   unknown delivery:
   Keep this job enabled and report useful safe error details and whether creation
   is confirmed, ruled out or unknown. Wait for the next scheduled run.

Do not run overlapping attempts or immediately retry a tool call. Each scheduled
run makes at most one normal create call with the same approved parameters. The
existing GoAsterWeb exact-name check precedes each create. Never adopt an existing
server, recover keys, or resend or replay a handoff. After partial delivery the
next normal run should find the existing name and stop; it must not resend.

Report useful safe details after initial errors and arrange the same single job.
Manual cancellation remains available. Local scheduler ownership, claim or storage
corruption requires review and must fail closed.
