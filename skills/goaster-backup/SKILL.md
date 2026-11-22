# GoAster backup and recovery

Before every deployment, create and verify a Hetzner snapshot of the exact
target server. A successful, available snapshot is a required deployment gate.

Before creating it, identify the target server and explain the expected cost and retention. Include the snapshot in the coordinator's single approval; standalone backup requires approval. Explain that it can contain secrets and that a running snapshot may not capture application data consistently. Identify essential data outside the server disk, including attached volumes, for recovery planning.

Wait for snapshot creation to complete,
then verify that the snapshot is available and belongs to the target server.
Record its ID for recovery.

If any step fails or is unclear, stop. Do not deploy.

Recovery is manual: if deployment fails, an operator decides whether to restore
the snapshot. Do not set up automatic rollback.
