# Git repositories

Use `/srv/git/<owner>/<repo>` for a new persistent clone unless the user specifies a path; retain an appropriate existing checkout. Validate path components and repository origin, avoid credential-bearing URLs and preserve unrelated work. Create missing directories and clone or check out as needed with the existing identity. Ignored files alone are not a blocker. Report unsafe paths, conflicting work or actual access failures.

Use `github-auth` for GitHub access. Ordinary checkout, fetch, merge and rebase for a coordinated source task are covered by its single approval; standalone work follows its requested scope. Never silently overwrite user work.
