# Fly app origin validation, 2026-11-25

Source scope: changed only the production migration Web origin to `https://goaster.fly.dev`. Offline regression coverage checks the default create endpoint and redirect policy, and strengthens the same-origin successful handoff assertion; existing redirect and cross-origin rejection tests remain in place. Historical evidence was left unchanged. This is source-only validation; no live service, provisioning, or deployment was exercised.

Commands run from the repository root (logs and build outputs are outside the repository):

```sh
mkdir -p /root/tmp/goaster-validation-20261125/home /root/tmp/goaster-validation-20261125/cache /root/tmp/goaster-validation-20261125/build
export HOME=/root/tmp/goaster-validation-20261125/home GOPATH=/root/go GOCACHE=/root/tmp/goaster-validation-20261125/cache GOPROXY=off GOSUMDB=off GOTOOLCHAIN=local
/usr/local/go/bin/go test ./... > /root/tmp/goaster-validation-20261125/test.log 2>&1
/usr/local/go/bin/go vet ./... > /root/tmp/goaster-validation-20261125/vet.log 2>&1
/usr/local/go/bin/go build -o /root/tmp/goaster-validation-20261125/build/ ./... > /root/tmp/goaster-validation-20261125/build.log 2>&1
/usr/local/go/bin/go test -race ./internal/migration/... > /root/tmp/goaster-validation-20261125/race.log 2>&1
```

All four Go commands exited 0. Tests use offline fixtures, including local HTTP test servers; they do not establish live service compatibility.

## Independent verification and rollout boundary

Parent independently reran full `go test ./...`, `go vet ./...`, `go build -o /root/tmp/goaster-fly-parent/build/GoAster/ ./...`, `go test -race ./internal/migration`, and `git diff --check`, with Go 1.25, isolated HOME and GOPROXY/GOSUMDB off: all passed. Read-only Terra final review: SATISFIED, no blocking code findings.

The target Fly app was created, but its Web deployment is blocked on securely configuring the existing dedicated Redis credentials. Keep this origin change on its non-release feature branch until the new Web endpoint is deployed and verified. Existing main and installed agents must continue using the old origin in the meantime. No running agent, cron job, installed skill, handoff or real migration was changed or tested.
