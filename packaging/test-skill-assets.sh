#!/bin/sh
set -eu

script_dir=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
repo_root=$(CDPATH= cd -- "$script_dir/.." && pwd)
. "$script_dir/assets.sh"

expected_starter_skills='background-command-administration bitwarden-secrets-manager github-auth goaster-backup goaster-build-test goaster-source-code-evolution goaster-deploy goaster-env goaster-find-contributions goaster-fork goaster-migration goaster-share-contribution hetzner-auth hetzner-auto-backup manage-skills modify-goaster-source-code internet-fetch model-administration project-memory read-write-temp-files ripgrep-search memory-consolidation goaster-telegram-groups-best-practices telegram-commands'
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

# Validate each active file both before and after packaging. Checking the files
# separately catches stale metadata or authorization guidance alongside SKILL.
requirements = {
    "META.md": ("one exact migration server", "private", "paired owner"),
    "AUTH.md": ("public admission and rate bookkeeping first", "read-only exact-name lookup before capacity", "paired private Telegram", "explicit conversational approval", "request-scoped", "never to the model", "local persistence", "duplicate charges", "fresh conversational approval", "server_name", "target_exists", "no_capacity", "1800", "one create call"),
    "SKILL.md": ("public admission and rate bookkeeping first", "read-only exact-name lookup before capacity", "paired private Telegram", "explicit conversational approval", "eventual exact request", "protected one-shot", "server_name", "target_exists", "1800 seconds", "no_capacity", "do not replay", "Never expose the URL", "separate", "SSH private key", "one scheduled create call"),
}
for label, base in (("source", root), ("installed", installed_root)):
    for filename, phrases in requirements.items():
        path = base / "skills" / "goaster-migration" / filename
        content = " ".join(path.read_text(encoding="utf-8").split())
        for phrase in phrases:
            if phrase not in content:
                raise SystemExit(f"{label} migration {filename} missing guidance: {phrase!r}")
        for forbidden in ("scheduler resumption are enabled", "preserve it for manual owner cleanup", "30-minute availability monitor", "/migration/v1/machines/", "operation_capability", "GoAsterWeb/", "../../docs/", "../docs/"):
            if forbidden in content:
                raise SystemExit(f"{label} migration {filename} retained retired guidance: {forbidden!r}")

notes = " ".join((root / "docs" / "goaster-migration.md").read_text(encoding="utf-8").split())
for phrase in ("retired", "protected one-shot", "server_name", "target_exists", "no secret local persistence"):
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
