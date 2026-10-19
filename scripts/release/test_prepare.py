"""Exercise the public-release admission boundary without building installers."""

import hashlib
import json
import os
from pathlib import Path
import tempfile
import unittest
from unittest.mock import patch

import prepare


class ReleaseAdmissionTests(unittest.TestCase):
    def test_prerelease_identity(self):
        for version in ("0.1.0-beta.1", "1.2.3-rc.0"):
            self.assertEqual(prepare.validate_version(version), version)
        for version in ("1.0.0", "v1.0.0-beta.1", "1.0.0-alpha.1", "01.0.0-rc.1", "1.0.0-rc.01", "1.0.0-rc.1\n"):
            with self.subTest(version=version), self.assertRaises(ValueError):
                prepare.validate_version(version)

    def test_publication_requires_exact_main_push_success(self):
        with tempfile.TemporaryDirectory() as root:
            env = {"GITHUB_EVENT_NAME": "push", "GITHUB_REF_NAME": "v0.1.0-beta.1",
                   "GITHUB_REF": "refs/tags/v0.1.0-beta.1", "GITHUB_REPOSITORY": "goforj/harbor",
                   "GITHUB_OUTPUT": str(Path(root) / "output")}
            valid = {"head_sha": "abc", "head_branch": "main", "status": "completed", "conclusion": "success"}
            for field, value in (("head_sha", "other"), ("head_branch", "feature"),
                                 ("status", "in_progress"), ("conclusion", "failure")):
                run = dict(valid, **{field: value})
                with self.subTest(field=field), patch.dict(os.environ, env), \
                     patch.object(prepare, "command", side_effect=["abc", json.dumps({"workflow_runs": [run]})]), \
                     patch.object(prepare.subprocess, "run"), self.assertRaises(ValueError):
                    prepare.identity()
            with patch.dict(os.environ, env), \
                 patch.object(prepare, "command", side_effect=["abc", json.dumps({"workflow_runs": [valid]})]), \
                 patch.object(prepare.subprocess, "run") as ancestry:
                prepare.identity()
                ancestry.assert_called_once_with(["git", "merge-base", "--is-ancestor", "abc", "origin/main"], check=True)
            self.assertEqual(Path(env["GITHUB_OUTPUT"]).read_text(), "version=0.1.0-beta.1\n")

    def create_packages(self, root):
        version = "0.1.0-beta.1"
        names = [f"GoForj-Harbor-macos-arm64-{version}.pkg", "goforj-harbor_0.1.0~beta.1-1_amd64.deb"]
        for name in names:
            (root / name).write_bytes(b"tested installer")
            digest = hashlib.sha256(b"tested installer").hexdigest()
            (root / (name + ".sha256")).write_text(f"{digest}  {name}\n")
        return names

    def test_seals_exact_tested_bytes(self):
        with tempfile.TemporaryDirectory() as root:
            directory = Path(root)
            names = self.create_packages(directory)
            prepare.seal_assets(directory, "0.1.0-beta.1", "abc", "https://github.com/example/run")
            provenance = json.loads((directory / "provenance.json").read_text())
            self.assertEqual(set(provenance["packages"]), set(names))
            self.assertEqual(provenance["source_commit"], "abc")
            self.assertEqual(len((directory / "SHA256SUMS").read_text().splitlines()), 2)

    def test_rejects_missing_extra_changed_empty_and_linked_packages(self):
        for failure in ("missing", "extra", "changed", "empty", "linked", "bad-checksum"):
            with self.subTest(failure=failure), tempfile.TemporaryDirectory() as root:
                directory = Path(root)
                name = self.create_packages(directory)[0]
                package = directory / name
                if failure == "missing":
                    package.unlink()
                elif failure == "extra":
                    (directory / "ci-only-windows.exe").write_bytes(b"ci certificate")
                elif failure in ("changed", "empty"):
                    package.write_bytes(b"changed" if failure == "changed" else b"")
                elif failure == "linked":
                    package.unlink()
                    package.symlink_to(directory / "goforj-harbor_0.1.0~beta.1-1_amd64.deb")
                else:
                    (directory / (name + ".sha256")).write_text("invalid hash\n")
                with self.assertRaises(ValueError):
                    prepare.seal_assets(directory, "0.1.0-beta.1", "abc", "https://github.com/example/run")


if __name__ == "__main__":
    unittest.main()
