# Automatic skill guidance refresh validation - 2026-11-20

This change keeps the existing conversation, summary, and authorization context when a selected user-owned skill changes. Before a turn, the current validated AUTH.md, META.md, and SKILL.md content is loaded and its durable selection digest is updated. During a tool batch, a changed skill causes the not-yet-executed portion of that batch to be discarded; completed call/output pairs are passed to the model with the refreshed guidance. Refresh does not itself rerun completed tools, and repeated discarded call IDs are rejected within the same request. Repeated mid-turn changes are bounded. Missing, malformed, unreadable, over-budget, or unsaveable guidance remains fail-closed.

Offline regression coverage includes:

- refresh between turns, persisted restart, and conversation compaction;
- preservation of scoped authorization context and current AUTH.md guidance;
- one completed mid-batch tool with the stale remainder discarded;
- bounded repeated refresh churn with no tool execution or manual `/clear` request;
- malformed and deleted selected-skill files;
- selection-digest checkpoint/save failure before a model request;
- existing selection, context-budget, integrity, and uncertainty-safety cases.

Validation performed for this scope:

- `GOTOOLCHAIN=local GOPROXY=off GOSUMDB=off /usr/local/go/bin/go test ./internal/chat -count=1 -timeout 300s`
- `GOTOOLCHAIN=local GOPROXY=off GOSUMDB=off /usr/local/go/bin/go test ./... -count=1 -timeout 300s`
- `GOTOOLCHAIN=local GOPROXY=off GOSUMDB=off /usr/local/go/bin/go vet ./...`
- focused `GOTOOLCHAIN=local GOPROXY=off GOSUMDB=off /usr/local/go/bin/go test -race ./internal/chat -run 'Skill|CodexProviderRegistersAndExecutesSkillTool' -count=1 -timeout 300s`
- `git diff --check`

All checks are source-only and offline; provider-facing tests use only local fake HTTP servers. No live provider request, runtime deployment, cloud resource, secret, GitHub issue, commit, or push was performed. Individual tool outputs are not durably checkpointed across crashes or turns interrupted by the churn limit. The saved churn warning tells the user to check potentially completed actions before repeating them. These checks do not establish exactly-once execution across crashes or later turns. Live provider behavior and rollout are also outside this validation. See the final review for subsequent checks and the reported initial full-suite failure followed by a passing rerun.

## Parent final acceptance, 2026-11-20

After the final review fix, the parent independently inspected the selected-guidance replacement and session checkpoint diff. Final full offline `GOPROXY=off GOSUMDB=off /usr/local/go/bin/go test -count=1 ./...`, `go vet ./...`, full race tests for `./internal/chat ./internal/artefacts`, and `git diff --check` passed. The earlier unexplained full-suite exit remains recorded in the final review; the repeat full-suite run and final-source full-suite run both passed. No live dialogue or running-agent rollout was tested. Next acceptance requires a separately authorized running-agent upgrade and a controlled live conversation refresh; do not clear existing conversations or repeat uncertain actions as part of rollout.

Recovery regression scope: validate named pending selections and checkpoint their current complete guidance before a model request; exercise more than ten selections, anonymous legacy markers, malformed requests, save failures, byte/context budgets, authorization guidance and action replay. See [skill selection recovery validation](skill-selection-recovery-validation-20261120.md) for results.
