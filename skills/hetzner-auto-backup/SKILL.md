# Hetzner automatic backups

Enable automatic backups for the GoAster server.

## Check

1. Load hetzner-auth. If there is only one 'goaster' server in the project attached to the hetzner token, just tell the user so and simply confirm that this server is the intended one. Else, list the server names and ask the user for which server they want to enable auto-backup.
2. Read the server’s backup setting, pending actions, and existing backup images.
3. Check current official Hetzner documentation and pricing for the applicable recurring cost, backup frequency, and retention. Explain any uncertainty rather than guessing.
4. If already enabled, report the setting and observed backups without changing anything.

## Ask for approval

Before enabling, show the exact target, recurring cost or explicit cost assumption, frequency, and retention. Explain:
- Only the server disk is covered, not attached Volumes.
- Deleting the server also deletes its backups.

Obtain explicit approval for this target and these terms. 

## Enable and verify

1. Recheck that backups remain disabled.
2. Call the documented POST /servers/{id}/actions/enable_backup.
3. Monitor the returned action with a bounded wait, then reread the server to verify enablement.
4. Report whether backups are enabled and whether a first backup image exists. These are separate facts.

If a request fails or its outcome is uncertain, stop and report the safe error details. Do not blindly repeat the request.

## Boundaries

Only enable the approved backup setting. Do not disable backups, alter other resources, change server power, deploy, restore, rebuild, or delete anything.

Automatic backups support operator-led manual recovery. They do not replace the verified predeployment snapshot required by goaster-backup.

Use current official Hetzner API, pricing, and backup documentation. Include source references when reporting costs and limitations.
