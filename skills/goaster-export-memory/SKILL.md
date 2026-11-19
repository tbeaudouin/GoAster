# goaster-export-memory

Export this GoAster agent's memories, skills, knowledge, and other files stored in its artefacts folder so the user can import them into another GoAster agent.

## Workflow

1. Resolve the real user's home and verify the source is the real directory `<real user home>/.goaster/artefacts`. Do not archive the whole `.goaster` directory or follow symbolic links outside the artefacts folder.
2. Explain: "I will export your artefacts folder as a ZIP. It may contain private memories and files. Environment variables are exported separately with `/get-env`. This does not export the running program, GitHub fork, or files outside the artefacts folder."
3. Use the existing `read-write-temp-files` skill for temporary storage and existing shell capabilities for archiving.
4. Create a ZIP named `goaster-memory-YYYYMMDD-HHMMSS.zip` containing one top-level `artefacts/` folder and all its contents, including hidden files and empty directories. Keep the archive private.
5. Send the ZIP to the user in the same conversation.
6. Tell the user: "On your old GoAster agent, send `/get-env` separately and save the `goaster.env` file securely. On your new agent, use `goaster-import-memory` with this ZIP, then attach `goaster.env` with `/env`."
