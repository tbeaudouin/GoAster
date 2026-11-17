# Generic cron create handoff

The migration skill places one exact approved name, type, and optional
locations in an ordinary private generic-cron message. The job uses a
1800-second interval and `max_runs: 1`. At execution the real chat session
receives one create capability after a lazy claim of the unchanged saved job.
The source validates the exact name before the create request, and the model
cannot provide an owner, destination, token, setup URL, or private key.

Only the exact `not_created` / `no_capacity` result returns the scheduler
retry signal. A second tool call, a cancelled or malformed call, a lost or
uncertain response, a target that already exists, a handoff or outcome
failure, and all session or delivery errors leave the job stopped or under
review. Ordinary cron management is available until the create attempt
begins, and a scheduled turn cannot add another job after that point.

The split handoff sends the setup URL as a private message and `id_ed25519`
as a key-only attachment, rechecking the paired owner before each part. No
secret material is returned to the model, persisted in the generic cron job,
or emitted in diagnostics. Tests use local model, HTTP, scheduler, and
Telegram fakes only.
