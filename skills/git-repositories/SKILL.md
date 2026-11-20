# Git repositories

Load this skill before selecting a path for a new clone or checkout. It sets
local path and inspection rules only; it does not authenticate, authorize Git
operations, create a GitHub repository or fork, or change the source workflow's
approval requirements.

## Choose a path

- Use `/srv/git/<owner>/<repo>` for a new persistent clone unless the user
  provides an explicit path for the task.
- Keep existing approved repositories where they are. Do not migrate them to
  the default path.
- Treat owner and repository names as single path components. Reject empty,
  `.` or `..` components, separators, or any value that could escape
  `/srv/git`.
- Inspect each existing path component as a trusted real directory with
  appropriate ownership and access controls. Reject symlinks, unexpected
  owners, non-directories, or paths writable by untrusted identities. Do not
  broadly change permissions or ownership.
- Clone only to a nonexistent final destination. After cloning, verify that
  the destination is the exact Git root and that `origin` identifies the
  intended repository before using it.
- If the approved identity cannot use the required path, stop and report the
  exact path that needs scoped provisioning. Do not fall back elsewhere, use
  root, or silently use `sudo`.

## Inspect an existing destination

Before using an existing destination, verify it is the exact Git root, verify
that `origin` identifies the intended host, owner, and repository, and inspect
`git status --short --ignored`. Do not print an origin containing credentials;
if a URL contains a token or other credentials, stop without echoing it. If the
destination is dirty, is a nested clone, is not the exact Git root, or has an
unexpected origin, stop and preserve it.

Never overwrite an existing tree or run `pull`, `reset`, or `checkout`
automatically. Follow the calling workflow's explicit approval boundary before
any operation that changes a worktree or branch.

Use `github-auth` for GitHub access. Do not embed tokens in URLs, expand
authentication into a remote URL, or bypass its handoff. The separate
`goaster-fork` workflow retains its fork verification and approval steps.
