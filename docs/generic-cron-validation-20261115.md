# Generic cron validation

The generic cron tool persists ordinary chat turns with transport-captured
conversation identity. The scheduler claims a due job before execution,
rejects changed or stopped jobs, and retains interrupted executions for
review. Scoped cron management remains available to the originating
conversation.

An immediate top-level `creation_state: "not_created"` and `code:
"no_capacity"` result creates one ordinary private recurring job with
`every_seconds: 1800` and `max_runs: 1`. Its turn can invoke the one-shot
create capability once. The same job silently waits only on that exact result;
a second tool call, session/provider error, delivery failure, or uncertain
result disables the job for review. Delivered success and `target_exists`
disable the job without adoption; other create results also disable/report.
The scheduled-only self-disable tool has no target ID and remains available
after the create attempt begins, while ordinary cron mutations remain blocked.

These rules share the generic scheduler's persistence and claim mechanisms;
there is no separate migration scheduler or migration lifecycle API. Existing
files from older installations are not read, removed, migrated, or resumed.

The current test suite covers ordinary recurrence, claim/replay guards,
origin isolation, one-shot create privacy, split handoff, and the
no-capacity-only waiting path using local fakes. It does not contact live
provider, Telegram, Web, deployment, cloud, or GitHub services.
