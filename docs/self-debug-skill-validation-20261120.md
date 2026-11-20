# GoAster self-debug skill validation — 2026-11-20

Added the concise `goaster-self-debug` starter. It loads through the normal default starter installation and uses the documented `goaster-agent.service` unit with a bounded, read-only journal query. No runtime code or service configuration changed.

Offline checks from the repository root:

- PASS — `GOTOOLCHAIN=local GOPROXY=off GOSUMDB=off /root/go/pkg/mod/golang.org/toolchain@v0.0.1-go1.25.13.linux-amd64/bin/go test ./internal/artefacts -run '^TestGoasterSelfDebugDefaultSkill$' -count=1 -timeout 120s`
- PASS — `GOTOOLCHAIN=local GOPROXY=off GOSUMDB=off /root/go/pkg/mod/golang.org/toolchain@v0.0.1-go1.25.13.linux-amd64/bin/go vet ./internal/artefacts`
- PASS — `sh -n packaging/assets.sh packaging/test-skill-assets.sh`
- PASS — `check_starter_tree` and `install_starters_to_prefix` from `packaging/assets.sh`, using a temporary Git index and prefix; all three new files copied byte-for-byte.
- PASS — `git diff --check`

`sh packaging/test-skill-assets.sh` reaches its existing unrelated `hetzner-auto-backup` wording assertion (`GET /servers/{id}/actions/{action_id}`) when run with the temporary index; the direct uncommitted-tree run stops earlier at the script's tracked-asset guard. No change was made for either condition.

These checks cover source starter installation, discovery, loading, packaging inventory, and command/unit consistency only. They do not query a live journal, change an installed agent, or validate a running service. The unrelated three main full-suite starter-content expectation failures remain out of scope.
