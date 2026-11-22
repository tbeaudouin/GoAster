# GoAster source code evolution

Use this skill to evolve GoAster's own source code and safely update its active
runtime. Group the question “Could a simple skill meet your need instead of changing GoAster's source code?” and the optional research choice (shared contributions, other agents' implementations, both, or neither) into the single approval prompt below. Research is optional and never blocks approved implementation.

## Workflow

```text
goaster-fork -> github-auth -> modify-goaster-source-code -> goaster-build-test -> goaster-backup -> goaster-deploy
```

## Coordinator boundaries

First load `goaster-env` and check `HCLOUD_TOKEN` in protected settings and then the process fallback as it directs. If missing, request setup through the private `/env` flow; never ask for the token in chat. If present, paginate `GET /v1/servers` and identify GoAster candidates from returned names and labels and available local runtime identity. If authentication fails or no candidates are found, report the actual problem and stop; do not ask for project or server IDs.

Present the discovered target and the whole process in one approval prompt: optional skill-versus-source and research choices, edits, fork and branch setup if needed, commit and push, builds and tests, snapshot with brief cost and retention information, deployment and restart. If there are multiple candidates, have the user choose among those discovered in this same prompt; do not guess. After approval, complete the process without stage-by-stage approval or reconfirming normal commits, SHA changes, backup, restart, or downstream stages. The delegated skills reuse this approval. Keep upstream read-only, work in the user's fork, and never create a pull request.

Secrets remain in their approved local or server stores. Do not expose them in
handoffs, logs, structured results, or backup manifests.
