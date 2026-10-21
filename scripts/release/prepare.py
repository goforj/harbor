"""Validate prerelease identity and seal packages already tested by native workers."""

import hashlib
import json
import os
from pathlib import Path
import re
import subprocess
import sys

VERSION = re.compile(r"(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)-(beta|rc)\.(0|[1-9][0-9]*)")


def validate_version(value):
    """validate_version excludes stable, malformed, and ambiguous numeric identities."""
    if not VERSION.fullmatch(value):
        raise ValueError("only MAJOR.MINOR.PATCH-(beta|rc).NUMBER prereleases are publishable")
    return value


def command(*arguments):
    """command executes fixed argument vectors without interpreting shell text."""
    return subprocess.check_output(arguments, text=True).strip()


def identity():
    """identity requires tested default-branch ancestry before a tag can publish."""
    event = os.environ["GITHUB_EVENT_NAME"]
    version = validate_version(os.environ["GITHUB_REF_NAME"][1:] if event == "push" else os.environ["REQUESTED_VERSION"])
    if event == "push":
        if os.environ["GITHUB_REF"] != "refs/tags/v" + version:
            raise ValueError("publication requires an exact version tag")
        revision = command("git", "rev-parse", "HEAD")
        subprocess.run(["git", "merge-base", "--is-ancestor", revision, "origin/main"], check=True)
        runs = json.loads(command("gh", "api", "--method", "GET",
                                  f"repos/{os.environ['GITHUB_REPOSITORY']}/actions/workflows/ci.yml/runs",
                                  "-f", f"head_sha={revision}", "-f", "event=push", "-f", "per_page=100"))
        if not any(run["head_sha"] == revision and run["head_branch"] == "main" and
                   run["status"] == "completed" and run["conclusion"] == "success"
                   for run in runs["workflow_runs"]):
            raise ValueError("tagged commit requires successful push CI on main before release")
    elif event != "workflow_dispatch":
        raise ValueError("unsupported release event")
    with Path(os.environ["GITHUB_OUTPUT"]).open("a") as output:
        output.write(f"version={version}\n")


def seal_assets(directory, version, revision, run_url):
    """seal_assets verifies tested packages and seals GitHub-safe download filenames."""
    validate_version(version)
    base, channel = version.split("-", 1)
    names = [f"GoForj-Harbor-macos-arm64-{version}.pkg", f"goforj-harbor_{base}~{channel}-1_amd64.deb"]
    expected = set(names + [name + ".sha256" for name in names])
    if {path.name for path in directory.iterdir()} != expected:
        raise ValueError("publication set contains missing or unexpected assets")
    digests = {}
    for name in names:
        package = directory / name
        checksum = directory / (name + ".sha256")
        if package.is_symlink() or checksum.is_symlink() or not package.is_file() or not checksum.is_file():
            raise ValueError("release assets must be regular files")
        if package.stat().st_size == 0:
            raise ValueError("empty release package")
        with package.open("rb") as source:
            digest = hashlib.file_digest(source, "sha256").hexdigest()
        if checksum.read_text().strip() != f"{digest}  {name}":
            raise ValueError(f"package checksum mismatch: {name}")
        digests[name] = digest
    # GitHub rewrites tildes in download names, but Debian keeps them in package versions.
    public_digests = {}
    for name, digest in digests.items():
        public_name = name.replace("~", ".")
        if public_name != name:
            (directory / name).rename(directory / public_name)
            (directory / (name + ".sha256")).rename(directory / (public_name + ".sha256"))
            (directory / (public_name + ".sha256")).write_text(f"{digest}  {public_name}\n")
        public_digests[public_name] = digest
    digests = public_digests
    (directory / "SHA256SUMS").write_text("".join(f"{digests[name]}  {name}\n" for name in sorted(digests)))
    (directory / "provenance.json").write_text(json.dumps({
        "schema_version": 1, "version": version, "source_commit": revision,
        "workflow_run": run_url, "signing": "unsigned-development-preview",
        "packages": digests,
    }, indent=2) + "\n")
    (directory / "RELEASE-NOTES.md").write_text(f"""Harbor {version} is an unsigned development preview.

These are the exact packages that passed their native install, three-project trusted-HTTPS, and ownership-aware uninstall workflows. They have not been rebuilt for publication.

- macOS: Apple silicon PKG, tested on macOS 26. Not Developer ID signed or notarized.
- Linux: amd64 DEB, tested on Ubuntu 24.04. No signed APT repository is configured.
- Windows: public installers are gated until production code signing replaces the disposable CI certificate. Windows CI installers are not public release assets.

This is not a stable release. Hosted acceptance does not prove genuine interactive consent, reboot recovery, native Docker Desktop integration, or signed updates. Review the repository's installation and testing documents before installing.

Verify downloads with `sha256sum --check SHA256SUMS` (or `shasum -a 256 -c SHA256SUMS` on macOS).

Source: `{revision}`. [Native package workflow]({run_url}).
""")


def main():
    """main keeps publishing authority out of the package verification process."""
    if sys.argv[1:] == ["identity"]:
        identity()
    elif sys.argv[1:] == ["assets"]:
        seal_assets(Path("release-assets"), os.environ["RELEASE_VERSION"], os.environ["GITHUB_SHA"],
                    f"{os.environ['GITHUB_SERVER_URL']}/{os.environ['GITHUB_REPOSITORY']}/actions/runs/{os.environ['GITHUB_RUN_ID']}")
    else:
        raise ValueError("expected identity or assets")


if __name__ == "__main__":
    main()
