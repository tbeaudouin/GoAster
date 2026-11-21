# Checkout self-provision validation — 2026-11-21

The bundled `git-repositories` guidance permits the existing service/session
identity to create missing trusted checkout parents, including when it is root.
The focused regression installs and loads the starter skill in a temporary
home and checks that guidance alongside the existing path, checkout, auth and
approval guardrails. It does not execute a checkout or test filesystem security.

Offline checks passed with `GOTOOLCHAIN=local GOPROXY=off GOSUMDB=off`:

- `/usr/local/go/bin/go test ./internal/artefacts -run '^TestDefaultFilesystemSkillContracts$' -count=1`
- `/usr/local/go/bin/go vet ./internal/artefacts`
- `git diff --check`

The first test and vet attempts using `go` on PATH failed because it is Go
1.22.2 and the module requires Go 1.25; the commands above used Go 1.25.
The installed skill and runtime were not rolled out or exercised.
