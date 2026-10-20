"""Admit exact native-tested disposable Windows packages before GUI installation."""

import hashlib
import json
import os
from pathlib import Path
import re
import sys

sys.path.insert(0, str(Path(__file__).resolve().parents[1]))
import verify


def verify_assets(directory, run):
    """verify_assets binds one NSIS package to its successful native acceptance workflow."""
    if (run.get("repository", {}).get("full_name") != verify.REPOSITORY
            or run.get("status") != "completed" or run.get("conclusion") != "success"
            or run.get("path", "").split("@", 1)[0] != ".github/workflows/windows-development-package.yml"):
        raise ValueError("package must come from a successful Harbor Windows package workflow")
    packages = list(directory.glob("GoForj-Harbor-windows-amd64-*-unsigned.exe"))
    if len(packages) != 1:
        raise ValueError("exactly one Windows package is required")
    package = packages[0]
    checksum = directory / (package.name + ".sha256")
    metadata = directory / (package.name + ".provenance.json")
    for asset in (package, checksum, metadata):
        if asset.is_symlink() or not asset.is_file() or asset.stat().st_size == 0:
            raise ValueError("package and sidecars must be nonempty regular files")
    provenance = json.loads(metadata.read_text())
    revision = provenance.get("source_commit", "")
    if not re.fullmatch(r"[a-f0-9]{40}", revision) or revision != run.get("head_sha"):
        raise ValueError("package source differs from the native-tested run")
    version = provenance.get("version", "")
    if not re.fullmatch(r"[0-9]+\.[0-9]+\.[0-9]+-(dev|rc)\.[0-9]+", version):
        raise ValueError("Windows GUI acceptance requires a development or candidate package")
    if package.name != f"GoForj-Harbor-windows-amd64-{version}-unsigned.exe":
        raise ValueError("package filename differs from provenance")
    expected_run = f"https://github.com/{verify.REPOSITORY}/actions/runs/{run['id']}"
    if provenance.get("schema_version") != 1 or provenance.get("workflow_run") != expected_run:
        raise ValueError("package provenance does not identify the selected workflow run")
    with package.open("rb") as source:
        digest = hashlib.file_digest(source, "sha256").hexdigest()
    if provenance.get("packages") != {package.name: digest} or checksum.read_text() != f"{digest}  {package.name}\n":
        raise ValueError("package digest differs from tested provenance")
    expected = {package.name, checksum.name, metadata.name}
    if {entry.name for entry in directory.iterdir()} != expected:
        raise ValueError("unexpected Windows package artifact entries")
    return revision, package.name


def main():
    """main emits only admitted package identity for the workflow installer step."""
    revision, package = verify_assets(Path("package-assets"), json.loads(Path("package-run.json").read_text()))
    with open(os.environ["GITHUB_OUTPUT"], "a") as output:
        output.write(f"source_commit={revision}\npackage={package}\n")


if __name__ == "__main__":
    main()
