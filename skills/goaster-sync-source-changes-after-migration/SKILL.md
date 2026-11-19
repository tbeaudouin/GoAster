# goaster-sync-source-changes-after-migration

Use when the user wants to restore source-code customizations from their existing GoAster fork.

## Identify and compare

1. Follow `github-auth` to verify GitHub authentication and the user's account.
2. Follow `goaster-fork` to identify and verify the user's existing fork of `tbeaudouin05/GoAster`, its source branch, and the local checkout. The standard customization branch is `goaster-customization`. Do not create a new fork or branch just for this check. If the fork or source branch cannot be established, ask the user rather than guessing. If the user confirms they have no fork or customizations, stop with no changes.
3. Fetch the verified public upstream `main` and the verified fork branch using existing Git capabilities. Record their exact commits. Preserve local changes; do not reset, overwrite, force-push, merge, or rebase automatically. If the checkout is dirty or the target changes unexpectedly, stop and explain.
4. If the customization branch exists, it becomes source of truth for the following process. Compare the customization branch with main branch only for information purpose.

## Restore customizations

6. Compare the main branch vs. the customization branch and summarize broadly the main differences. Show the verified fork, branch and exact source commit. Ask for approval to restore that version. Do not invent new customizations or silently combine the fork with newer upstream changes.
7. Use the existing `goaster-source-code-evolution` workflow, starting with the verified existing fork. Follow `modify-goaster-source-code` only if source changes are needed, then `goaster-build-test`, `goaster-backup`, and `goaster-deploy`. Keep each skill's approval and safety requirements. Reuse the existing implementation; do not add a separate migration updater.
8. If the user also wants to merge current public main into their fork, treat that as a separately approved Git change. Resolve and test it before deployment; never assume migration authorizes a merge or rebase.
9. Build and test the approved source revision for the destination architecture. Obtain the required backup and deployment approvals before replacing binaries or restarting. Preserve imported artefacts; do not reinstall starter skills over them. A build failure, incompatible import, missing required skill, or uncertain backup stops deployment with a clear explanation.
10. Follow `goaster-deploy` for the restart warning and post-restart verification. Explain that the user must send a new message afterward to request verification. Only report the runtime restored after checking service health and matching the running binaries to the tested build.
