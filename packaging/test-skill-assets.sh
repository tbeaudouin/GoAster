#!/bin/sh
set -eu

script_dir=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
repo_root=$(CDPATH= cd -- "$script_dir/.." && pwd)
. "$script_dir/assets.sh"

expected_starter_skills='background-command-administration bitwarden-secrets-manager github-auth git-repositories goaster-backup goaster-build-test goaster-source-code-evolution goaster-deploy goaster-env goaster-find-contributions goaster-fork goaster-migration goaster-share-contribution hetzner-auth hetzner-auto-backup manage-skills modify-goaster-source-code goaster-feature-inspiration internet-fetch model-administration project-memory task-planning read-write-temp-files ripgrep-search memory-consolidation goaster-telegram-groups-best-practices telegram-commands goaster-export-memory goaster-import-memory goaster-sync-source-changes-after-migration goaster-self-debug'
if [ "$starter_skills" != "$expected_starter_skills" ]; then
	printf 'starter skill allowlist changed unexpectedly\nexpected: %s\nactual: %s\n' \
		"$expected_starter_skills" "$starter_skills" >&2
	exit 1
fi

check_starter_tree "$repo_root/skills"
check_tracked_starters

package_root=$(mktemp -d)
trap 'rm -rf "$package_root"' EXIT HUP INT TERM
mkdir "$package_root/share"
install_starters_to_prefix "$package_root/share"

python3 - "$repo_root" "$expected_starter_skills" "$package_root/share" <<'PY'
import pathlib
import re
import sys
from urllib.parse import unquote, urlsplit

root = pathlib.Path(sys.argv[1]).resolve()
expected = sys.argv[2].split()
installed_root = pathlib.Path(sys.argv[3]).resolve()
actual = sorted(
    path.name
    for path in (root / "skills").iterdir()
    if path.name != "MEMORY.md"
)
if actual != sorted(expected):
    raise SystemExit(f"skill directory inventory differs: {actual!r}")

for name in expected:
    directory = root / "skills" / name
    if not directory.is_dir() or directory.is_symlink():
        raise SystemExit(f"invalid skill directory: {directory}")
    metadata = directory / "META.md"
    lines = metadata.read_text(encoding="utf-8").splitlines()
    if len(lines) != 2 or not lines[0].startswith("name: ") or not lines[1].startswith("description: "):
        raise SystemExit(f"invalid META.md shape: {metadata}")
    if lines[0] != f"name: {name}":
        raise SystemExit(f"META.md name does not match directory: {metadata}")
    for filename in ("META.md", "SKILL.md", "AUTH.md"):
        path = directory / filename
        if not path.is_file() or path.is_symlink():
            raise SystemExit(f"invalid starter file: {path}")
        path.read_text(encoding="utf-8")

workflow = (root / "skills" / "hetzner-auto-backup" / "SKILL.md").read_text(encoding="utf-8")
for phrase in (
    "goaster-backup",
    "POST /servers/{id}/actions/enable_backup",
    "GET /servers/{id}/actions/{action_id}",
    "explicitly approve",
    "recurring backup charge",
    "daily automatic",
    "seven backup slots",
    "application-consistent",
    "Attached Volumes are not included",
    "If backups are already enabled",
    "stop and report",
):
    if phrase not in workflow:
        raise SystemExit(f"hetzner-auto-backup workflow missing required guidance: {phrase!r}")

# Validate the added post-migration source sync starter without executing
# its workflow. Check its metadata, authentication boundary, and key guardrails
# in both the repository source and the installed package.
source_sync_requirements = {
    "META.md": ("source-code customizations", "existing GoAster fork", "migrating artefacts"),
    "AUTH.md": ("paired owner's private conversation", "github-auth", "goaster-fork", "does not authorize source changes"),
    "SKILL.md": (
        "verify GitHub authentication and the user's account",
        "goaster-customization",
        "Do not create a new fork or branch",
        "Preserve local changes",
        "Ask for approval to restore that version",
        "goaster-source-code-evolution",
        "or rebase automatically",
        "separately approved Git change",
        "required backup and deployment approvals",
    ),
}
for label, base in (("source", root), ("installed", installed_root)):
    directory = base / "skills" / "goaster-sync-source-changes-after-migration"
    for filename, phrases in source_sync_requirements.items():
        path = directory / filename
        content = " ".join(path.read_text(encoding="utf-8").split())
        for phrase in phrases:
            if phrase not in content:
                raise SystemExit(f"{label} source sync {filename} missing guidance: {phrase!r}")

# Validate each active file both before and after packaging. Checking the files
# separately catches stale metadata or authorization guidance alongside SKILL.
requirements = {
    "META.md": ("one exact migration server", "private", "paired owner", "30 minutes"),
    "AUTH.md": (
        "paired private Telegram conversation", "never request a pasted provider token",
        "one private spending approval", "server_name", "server_type", "locations",
        "immediate first create", "capacity-only retries every 30 minutes", "now versus waiting", "four-step",
        "verify private delivery is available",
        "revalidated paired owner", "separate private messages", "id_ed25519",
        "protected environment is the intended token storage", "do not automatically resend",
    ),
    "SKILL.md": (
        "Use this workflow only in the paired owner’s private Telegram conversation.",
        "Check the protected <real user home>/.goaster/env first, then process HCLOUD_TOKEN.",
        "Never display the token or ask the user to paste it into chat.",
        "Keep tokens out of conversation text, model input, tool output, logs and extra local copies.",
        "If the check fails, report availability as unknown rather than unavailable.",
        "Pricing is informational only. Do not send a target price, impose a price ceiling, or choose locations based on price.",
        "The user may leave the location open.",
        "Before requesting approval, check Hetzner’s latest official public pricing",
        "Obtain one explicit spending approval in the paired private conversation",
        "Always try immediately first.",
        "A changed choice requires fresh approval.",
        'action: "create"', "The create action generates its SSH key pair internally",
        "sends only the public key to GoAsterWeb.",
        "Do not place private keys in the conversation or cron instructions.",
        "The model receives creation and delivery status, not the private key or secret-bearing link.",
        "Do not claim that setup or migration is complete merely because the server was created.",
        'creation_state: "not_created"', 'code: "no_capacity"',
        "create one ordinary recurring cron job, once, with a 30-minute interval.",
        "Do not create another job for the same request.",
        "Never create or schedule another cron job.",
        "Do nothing. Send no notification and make no cron changes.",
        "For every other error, uncertain creation outcome, or failed, partial or unknown delivery:",
        "Disable this job.",
        "Do not run overlapping attempts.",
        "Do not automatically retry creation after an uncertain outcome.",
        "Ask for one explicit confirmation before a fresh attempt.",
    ),
}
expected_migration_sections = (
    "1. Check the Hetzner token",
    "2. Choose the server",
    "3. Get approval for one exact choice",
    "4. Create or arrange a retry",
)
for label, base in (("source", root), ("installed", installed_root)):
    for filename, phrases in requirements.items():
        path = base / "skills" / "goaster-migration" / filename
        raw = path.read_text(encoding="utf-8")
        content = " ".join(raw.split())
        for phrase in phrases:
            if phrase not in content:
                raise SystemExit(f"{label} migration {filename} missing guidance: {phrase!r}")
        if filename == "SKILL.md":
            section_pattern = r"^[1-4]\. (?:Check the Hetzner token|Choose the server|Get approval for one exact choice|Create or arrange a retry)$"
            sections = tuple(line for line in raw.splitlines() if re.match(section_pattern, line))
            if sections != expected_migration_sections:
                raise SystemExit(f"{label} migration SKILL.md must keep the four numbered sections: {sections!r}")
        for forbidden in ("scheduler resumption are enabled", "preserve it for manual owner cleanup", "30-minute availability monitor", "/migration/v1/machines/", "operation_capability", "GoAsterWeb/", "../../docs/", "../docs/"):
            if forbidden in content:
                raise SystemExit(f"{label} migration {filename} retained retired guidance: {forbidden!r}")

availability_requirements = (
    "GET /v1/server_types?name=<url-encoded-exact-type>",
    "server_types[]",
    "locations[].available",
    "removed 2026-10-01",
    "capacity guarantee",
)
for label, base in (("source", root), ("installed", installed_root)):
    for filename, skill_name, phrases in (
        ("SKILL.md", "goaster-migration", (
            "GET /v1/server_types?name=<requested-server-type>",
            "server_types[].locations[].available",
            "Do not use the retired datacenters endpoint.",
            "An availability check does not reserve capacity.",
        )),
        ("AUTH.md", "hetzner-auth", availability_requirements),
    ):
        path = base / "skills" / skill_name / filename
        content = " ".join(path.read_text(encoding="utf-8").split())
        for phrase in phrases:
            if phrase not in content:
                raise SystemExit(f"{label} {path.parent.name} {filename} missing current availability guidance: {phrase!r}")
        for forbidden in ("Use GET /v1/datacenters", "call GET /v1/datacenters", "Use the migration availability endpoint", "Use the migration availability tool"):
            if forbidden in content:
                raise SystemExit(f"{label} {path.parent.name} {filename} retained active retired availability guidance: {forbidden!r}")

notes = " ".join((root / "docs" / "goaster-migration.md").read_text(encoding="utf-8").split())
for phrase in ("ordinary generic", "one-shot", "server_name", "target_exists", "local persistence"):
    if phrase not in notes:
        raise SystemExit(f"migration notes missing guidance: {phrase!r}")

# Check local Markdown links so renamed skills and stale skill paths fail in
# the same offline test as the package allowlist.
link_pattern = re.compile(r"\]\((?:<([^>]+)>|([^\s)]+))")
for markdown in root.rglob("*.md"):
    if ".git" in markdown.parts or "dist" in markdown.parts:
        continue
    text = markdown.read_text(encoding="utf-8")
    for match in link_pattern.finditer(text):
        target = unquote(match.group(1) or match.group(2))
        parsed = urlsplit(target)
        if parsed.scheme or parsed.netloc or not parsed.path or parsed.path == "URL":
            continue
        candidate = (markdown.parent / parsed.path).resolve()
        if not candidate.exists():
            raise SystemExit(f"stale local Markdown link in {markdown}: {target}")

for markdown in installed_root.rglob("*.md"):
    text = markdown.read_text(encoding="utf-8")
    for match in link_pattern.finditer(text):
        target = unquote(match.group(1) or match.group(2))
        parsed = urlsplit(target)
        if parsed.scheme or parsed.netloc or not parsed.path or parsed.path == "URL":
            continue
        candidate = (markdown.parent / parsed.path).resolve()
        if not candidate.exists():
            raise SystemExit(f"stale installed Markdown link in {markdown}: {target}")

print("Skill allowlist, installed package, and local Markdown reference checks passed offline.")
PY
