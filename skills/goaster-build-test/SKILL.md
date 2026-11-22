# GoAster build and test

Build and test the requested changes in the user's fork on `goaster-customization`, preserving unrelated work and keeping secrets out of logs and artifacts. Use `read-write-temp-files` for private temporary files. Format changed Go files as needed and run appropriate checks, including `go build ./...` and `go test ./...` when applicable. Fix failures within the approved task and report any remaining failures or incomplete checks.

Discover the target OS and architecture from available target evidence, then build `goaster-agent` and `goaster-telegram` for that OS and architecture, retain the tested binaries with their source revision and checksums, and verify the artifacts before installation. A new build is needed if the source or binaries change; ordinary path, SHA or artifact changes do not require another approval. Ignored files alone do not prevent testing. On Linux, protected socket tests may require root and a short secure `TMPDIR`.

A real-provider prompt check is optional, not a universal deployment gate; run it only when chosen and authorized, and report its result without exposing credentials or prompt output. Standalone build/test does not authorize deployment. Coordinated work continues under the existing approval through backup and deployment.
