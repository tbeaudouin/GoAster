# No system roles

New instruction messages use the developer role. Conversation role validation accepts developer and rejects system using the existing unsupported-role handling. The Codex serialization boundary also rejects system roles before a network request. Context budgeting, trimming, and compaction retain their existing behavior; no history conversion is performed.

Offline regression coverage checks source construction, developer-role persistence, chat and summary requests, retries and tool continuation, and rejection before network access. The source check detects explicit role literals and assignments, not dynamically constructed values.

Validation used a fresh private HOME under `/root/tmp` for each invocation, with the existing Go build cache and GOPATH retained:

- `go test ./...`: passed.
- `go vet ./...`: passed.
- `go build ./...`: passed.
- Focused role, summary, persistence, retry and source-guard tests: passed after the final summary assertion was added.
- `go test -race ./internal/chat ./cmd/goaster-agent`: passed.

The parent independently reran full tests, vet, build, focused package race tests, and `git diff --check` on the final code; all passed. A read-only Terra final review was satisfied.

Source-only change: no running agent, deployment, stored conversation, or live service was changed.
