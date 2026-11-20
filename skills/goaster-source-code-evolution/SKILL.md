# GoAster source code evolution

Use this skill to evolve GoAster's own source code and safely update its active
runtime. Before making source changes, ask the user: "Could a simple skill meet
your need instead of changing GoAster's source code?" After that question is
answered, offer once the optional research choice in
[modify-goaster-source-code](../modify-goaster-source-code/SKILL.md): shared
contributions, other agents' implementations, both, or neither. If that offer
was already made, do not repeat it. Research may be skipped and never blocks
approved implementation.

## Workflow

```text
goaster-fork -> github-auth -> modify-goaster-source-code -> goaster-build-test -> goaster-backup -> goaster-deploy
```

## Coordinator boundaries

This coordinator only sequences the named skills. Each stage keeps its own
authentication, authorization, and approval gate. Upstream GitHub remains
read-only, the user works in their own fork, and this workflow never creates a
pull request.

Bind every approval to the exact GitHub host and fork identity, the
`goaster-customization` branch, source SHA,
local path, both built binary checksums, target host identity, affected
services, release paths, and any helper path verified on that host. A change to
any identity, SHA, path, or host invalidates the affected approval and all
downstream results. Re-run the necessary preflight, build, or backup stage and
obtain fresh approval; never carry an approval across a changed tuple.

Secrets remain in their approved local or server stores. Do not expose them in
handoffs, logs, structured results, or backup manifests.
