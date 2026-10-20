"""Verify retained package provenance before an unattended installed-UI run."""

import hashlib
import json
import os
from pathlib import Path
import re
import subprocess
import sys

sys.path.insert(0, str(Path(__file__).resolve().parents[1] / "release"))
import prepare


REPOSITORY = "goforj/harbor"


def verify_assets(directory, run):
    """verify_assets admits the exact native-tested package set from the selected repository run."""
    if (run.get("repository", {}).get("full_name") != REPOSITORY
            or run.get("status") != "completed" or run.get("conclusion") != "success"
            or run.get("path", "").split("@", 1)[0] != ".github/workflows/release.yml"):
        raise ValueError("packages must come from a successful Harbor prerelease workflow")
    provenance = json.loads((directory / "provenance.json").read_text())
    revision = provenance.get("source_commit", "")
    if not re.fullmatch(r"[a-f0-9]{40}", revision) or revision != run.get("head_sha"):
        raise ValueError("package source does not match the selected workflow run")
    expected_run = f"https://github.com/{REPOSITORY}/actions/runs/{run['id']}"
    if provenance.get("schema_version") != 1 or provenance.get("workflow_run") != expected_run:
        raise ValueError("package provenance does not identify the selected workflow run")
    version = prepare.validate_version(provenance["version"])
    base, channel = version.split("-", 1)
    package = f"GoForj-Harbor-macos-arm64-{version}.pkg"
    names = [package, f"goforj-harbor_{base}~{channel}-1_amd64.deb"]
    if set(provenance.get("packages", {})) != set(names):
        raise ValueError("unexpected package publication set")
    sums = ""
    for name in sorted(names):
        asset = directory / name
        checksum = directory / (name + ".sha256")
        if asset.is_symlink() or checksum.is_symlink() or not asset.is_file() or not checksum.is_file():
            raise ValueError("packages and checksum sidecars must be regular files")
        with asset.open("rb") as source:
            digest = hashlib.file_digest(source, "sha256").hexdigest()
        expected = f"{digest}  {name}\n"
        if asset.stat().st_size == 0 or provenance["packages"][name] != digest or checksum.read_text() != expected:
            raise ValueError(f"package digest mismatch: {name}")
        sums += expected
    if (directory / "SHA256SUMS").read_text() != sums:
        raise ValueError("publication checksum manifest differs from package provenance")
    return revision, package


def test_only_path(name):
    """test_only_path excludes only the verification harness and documentation from product-source matching."""
    return (name.startswith(("desktop/packageui/", "scripts/packageui/", "docs/"))
            or name in ("README.md", ".github/workflows/macos-package-ui.yml"))


def verify_product(expected, current):
    """verify_product prevents a current UI contract from claiming evidence for different packaged product code."""
    names = set(expected) | set(current)
    changes = sorted(name for name in names if not test_only_path(name) and expected.get(name) != current.get(name))
    if changes:
        raise ValueError("package and verification product sources differ: " + ", ".join(changes[:12]))


def tracked_files(root):
    """tracked_files compares Git-owned bytes and link identities without traversing untracked caches or link targets."""
    names = subprocess.check_output(["git", "-C", str(root), "ls-files", "-z"]).decode().split("\0")
    files = {}
    for name in filter(None, names):
        if test_only_path(name):
            continue
        path = root / name
        if path.is_symlink():
            files[name] = ("link", os.readlink(path))
        elif path.is_file():
            files[name] = ("file", hashlib.sha256(path.read_bytes()).hexdigest())
        else:
            raise ValueError("tracked product file is absent: " + name)
    return files


def main():
    """main emits only verified workflow outputs and never installs an unverified package."""
    if sys.argv[1:] == ["assets"]:
        revision, package = verify_assets(Path("package-assets"), json.loads(Path("package-run.json").read_text()))
        with open(os.environ["GITHUB_OUTPUT"], "a") as output:
            output.write(f"source_commit={revision}\npackage={package}\n")
    elif sys.argv[1:] == ["source"]:
        verify_product(tracked_files(Path(".ci/package-source")), tracked_files(Path(".")))
    else:
        raise ValueError("expected assets or source")


if __name__ == "__main__":
    main()
