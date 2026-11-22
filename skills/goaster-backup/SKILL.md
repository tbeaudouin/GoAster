# GoAster backup and recovery

Use `goaster-env` and `hetzner-auth` to identify the target and keep credentials private. The coordinator's single approval covers snapshot cost and retention; a standalone snapshot requires one scoped approval covering these. Explain that snapshots can contain secrets and that a running snapshot may not capture application data consistently.

Create a snapshot of the target server, wait for completion, verify that the snapshot is available and belongs to the target server, and record its ID for recovery. Report any actual snapshot failure and do not deploy without the verified snapshot. Do not ask an additional recovery questionnaire or prompt for approval again.

Recovery is manual. Do not set up automatic rollback.
