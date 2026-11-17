# Cron self-disable lifetime follow-up — 2026-11-17

**Own review verdict: the identified self-disable lifetime finding is resolved; ready for final Sol review.** This verdict covers the requested change and its regression, not a new approval-design review.

`DisableOwnExecution` still obtains its target from the private scheduler-bound owner and validates the origin. Its transaction now rechecks that exact owner's `done` channel after acquiring the scheduler mutex and durable state lock, before inspecting or changing the job. Executor completion closes that channel under the same scheduler mutex. Consequently a self-disable transaction either finishes while its owner is live or observes revocation; an old owner cannot authorize a later run of the same ID. The executor releases the mutex before receiving from `owner.state`, which avoids reversing the existing claim path's state-channel-then-mutex order. No approval behavior was expanded.

The controlled scheduler regression pauses the transaction after the initial owner check, completes the first executor, starts a later execution of the same recurring job, and resumes the transaction while that execution is running. It checks for `ErrExecutionClaimUnavailable` and verifies the later job remains enabled. The test uses the same internal transaction helper called by `DisableOwnExecution` to control the pause. Existing self-disable/retry coverage also passed. No pointers were introduced by this follow-up.

Offline checks passed:

- `GOTOOLCHAIN=local GOPROXY=off GOSUMDB=off /usr/local/go/bin/go test ./...`
- `GOTOOLCHAIN=local GOPROXY=off GOSUMDB=off /usr/local/go/bin/go vet ./...`
- `GOTOOLCHAIN=local GOPROXY=off GOSUMDB=off /usr/local/go/bin/go test -race ./internal/scheduler ./internal/chat ./cmd/goaster-agent -count=1`
- `packaging/test-skill-assets.sh`, with its temporary directory inside the repository and removed afterward
- `git diff --check`

Review considered serialization of revocation and durable mutation, same-ID reuse, the state-channel lock order, and the existing finalization path. The concurrency test controls the authorization interleaving; race-detector success alone would not establish it. No network, GitHub, cloud, secrets, live cron, or commits were used. This is an offline review and does not validate deployed provider or Telegram behavior.
