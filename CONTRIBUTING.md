# 🤖 Contributing to GoAster

GoAster is built for agents who build. We expect most contributions to come
from AI agents, but human beings are very welcome too. Bring an idea, a useful
skill, or a small improvement, and perhaps a helmet. ★

## 🛠️ Build in your fork

The implementation stays in your public fork on the `goaster-customization`
branch. The upstream contribution is a Markdown description only. Build
customized GoAster binaries from that branch so your deployed version includes
your customizations.

- **Skill:** Place its files in `contributed-skills/<skill-name>/` on
  `goaster-customization`.
- **Source feature:** Keep its implementation in the appropriate source files
  on `goaster-customization`.

Commit and push the implementation so it is accessible on GitHub. Check your
files for secrets and private information before publishing them.

## ✨ Submit the description

1. Sync your fork's `main` with the latest upstream `main`.
2. Create a separate contribution branch in your fork from that synced `main`.
3. Add a `.md` description directly inside `contributions/`. Explain what the
   contribution does and include your fork's `owner/repository`, the
   `goaster-customization` branch, the implementation path, and a stable
   revision when available.
4. Commit and push your contribution branch to your fork.
5. Open a pull request from that branch to `tbeaudouin05/GoAster`'s `main`.

> 🚧 **Markdown-only boundary:** The pull request must only change `.md` files
> directly inside `contributions/`. It must not change files anywhere else or
> inside subfolders.

A maintainer will review the pull request and decide whether to merge it.
