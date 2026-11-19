# goaster-export-memory

Export this GoAster agent's memories, skills, knowledge, and other files stored in its artefacts folder so the user can import them into another GoAster agent.

## Workflow

1. Resolve the real user's home and verify the source is the real directory `<real user home>/.goaster/artefacts`. Do not archive the whole `.goaster` directory or follow symbolic links outside the artefacts folder.
2. Explain: "I will export your artefacts folder as a ZIP. It may contain private memories and files. Environment variables are exported separately with `/get-env`. This does not export the running program, GitHub fork, or files outside the artefacts folder."
3. Use the existing `read-write-temp-files` skill for temporary storage and existing shell capabilities for archiving.
4. Create a ZIP named `goaster-memory-YYYYMMDD-HHMMSS.zip` containing one top-level `artefacts/` folder and all its contents, including hidden files and empty directories. Keep the archive private.
5. Send the ZIP to the user in the same conversation.
6. Tell the user: "On your old GoAster agent, send `/get-env` separately and save the `goaster.env` file securely. On your new agent, use `goaster-import-memory` with this ZIP, then attach `goaster.env` with `/env`."

## Safety and delivery

Use this workflow only in the paired owner's private conversation. Export only the real user's `<real user home>/.goaster/artefacts`; do not access outside it or follow/traverse symlinks. If any symlink or prohibited secret, credential, environment, or authentication material is encountered, stop and explain that a complete export cannot be made; never silently omit material or claim completeness. Environment variables are handled separately through `/get-env` and are outside this export.

Use the existing `read-write-temp-files` skill to create the archive in an existing private temporary directory outside the source tree. After the archive is complete, copy it as a private, exclusive regular file into a fresh, unique subdirectory of the artefacts delivery workspace. The staged copy must be no larger than `send_file`'s 8 MiB limit, addressed by its path relative to that workspace, and excluded from the export. Send it once with the existing `send_file` capability in this same conversation.

After confirmed delivery, remove only the staging copy, its own staging directory, and this task's temporary archive. If delivery is uncertain or fails, stop, preserve the archive and staged copy for explicit recovery, explain the status, and never retry or use another delivery route. Do not add tools, change runtime behavior, or modify authentication or delivery mechanisms.
