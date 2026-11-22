# GoAster backup and recovery

Use `goaster-env` and `hetzner-auth` to identify the target and keep credentials private. The coordinator's single approval covers snapshot cost and retention; a standalone snapshot requires one scoped approval covering these. Use a current official public snapshot estimate when obtainable; otherwise explicitly say the estimate is unavailable, never invent a price, and proceed under the existing approval with that uncertainty, respecting any explicit user limit. No pricing API call or separate approval is required. Accept the user’s stated data-location scope without asking again; do not inventory external storage or assert that a server snapshot covers external data. Explain that snapshots can contain secrets and that a running snapshot may not capture application data consistently.

Create a snapshot of the target server, wait for completion, verify that the snapshot is available and belongs to the target server, and record its ID for recovery. Report any actual snapshot failure and do not deploy without the verified snapshot. Do not ask for a project name or ID, recovery plan, or operator recovery-access questionnaire or prompt for approval again.

Recovery is manual. Do not set up automatic rollback.
