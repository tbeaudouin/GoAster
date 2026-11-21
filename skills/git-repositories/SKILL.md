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
  owners, non-directories, or paths writable by untrusted identities.
- Use the existing service/session identity, including root if already running
  as root; ordinary checkout setup requires no new user or identity switch.
  Create only missing trusted parent directories needed for the approved
  checkout, using that identity. Keep new directories non-world-writable and
  inspect them before continuing; missing directories alone are not a blocker.
  Do not change existing ownership or permissions or silently escalate identity.
- Clone only to a nonexistent final destination. After cloning, verify that
  the destination is the exact Git root and that `origin` identifies the
  intended repository before using it.
- If a component is unsafe or the existing identity cannot safely create or
  access the required path, stop and report the exact path and problem needing
  scoped provisioning. Do not fall back elsewhere or silently use `sudo`.

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
