"""Exercise Windows package provenance and digest admission failures."""

import hashlib
import json
from pathlib import Path
import tempfile
import unittest

import verify_windows


class WindowsAdmissionTests(unittest.TestCase):
    """WindowsAdmissionTests preserve fail-closed cross-workflow package admission."""

    def setUp(self):
        """setUp creates one fake package and exact matching native-run evidence."""
        self.temporary = tempfile.TemporaryDirectory()
        self.addCleanup(self.temporary.cleanup)
        self.directory = Path(self.temporary.name)
        self.name = "GoForj-Harbor-windows-amd64-0.1.0-dev.10-unsigned.exe"
        (self.directory / self.name).write_bytes(b"fake installer")
        self.digest = hashlib.sha256(b"fake installer").hexdigest()
        (self.directory / (self.name + ".sha256")).write_text(f"{self.digest}  {self.name}\n")
        self.run = {"id": 10, "repository": {"full_name": "goforj/harbor"}, "status": "completed",
                    "conclusion": "success", "head_sha": "a" * 40,
                    "path": ".github/workflows/windows-development-package.yml"}
        self.provenance = {"schema_version": 1, "source_commit": "a" * 40,
                           "workflow_run": "https://github.com/goforj/harbor/actions/runs/10",
                           "version": "0.1.0-dev.10", "packages": {self.name: self.digest}}
        self.write_provenance()

    def write_provenance(self):
        """write_provenance serializes the current fixture without executable content."""
        (self.directory / (self.name + ".provenance.json")).write_text(json.dumps(self.provenance))

    def test_exact_native_package(self):
        """test_exact_native_package admits only the retained installer and source pair."""
        self.assertEqual(verify_windows.verify_assets(self.directory, self.run), ("a" * 40, self.name))

    def test_untrusted_run(self):
        """test_untrusted_run rejects a foreign repository, wrong workflow, and unfinished or failed run."""
        for field, value in (("repository", {"full_name": "foreign/harbor"}), ("path", ".github/workflows/other.yml"),
                             ("status", "in_progress"), ("conclusion", "failure"), ("head_sha", "b" * 40)):
            with self.subTest(field=field), self.assertRaises(ValueError):
                verify_windows.verify_assets(self.directory, dict(self.run, **{field: value}))

    def test_provenance_drift(self):
        """test_provenance_drift rejects every authorization-bearing metadata mismatch."""
        original = dict(self.provenance)
        for field, value in (("source_commit", "../invalid"), ("schema_version", 2), ("workflow_run", "https://foreign/10"),
                             ("version", "0.1.0"), ("version", "0.1.1-dev.10"), ("packages", {})):
            with self.subTest(field=field, value=value):
                self.provenance = dict(original, **{field: value})
                self.write_provenance()
                with self.assertRaises(ValueError):
                    verify_windows.verify_assets(self.directory, self.run)

    def test_digest_drift(self):
        """test_digest_drift refuses altered package bytes and sidecars."""
        for filename in (self.name, self.name + ".sha256"):
            with self.subTest(filename=filename):
                target = self.directory / filename
                original = target.read_bytes()
                target.write_bytes(b"changed")
                with self.assertRaises(ValueError):
                    verify_windows.verify_assets(self.directory, self.run)
                target.write_bytes(original)

    def test_package_selection(self):
        """test_package_selection rejects absent or ambiguous installer selection."""
        package = self.directory / self.name
        original = package.read_bytes()
        package.unlink()
        with self.assertRaises(ValueError):
            verify_windows.verify_assets(self.directory, self.run)
        package.write_bytes(original)
        (self.directory / self.name.replace("dev.10", "dev.11")).write_bytes(original)
        with self.assertRaises(ValueError):
            verify_windows.verify_assets(self.directory, self.run)

    def test_nonregular_or_empty_assets(self):
        """test_nonregular_or_empty_assets refuses substituted package and provenance files."""
        for filename in (self.name, self.name + ".provenance.json"):
            target = self.directory / filename
            original = target.read_bytes()
            for mode in ("empty", "symlink"):
                with self.subTest(filename=filename, mode=mode):
                    target.unlink()
                    if mode == "empty":
                        target.write_bytes(b"")
                    else:
                        target.symlink_to(self.directory / (self.name + ".sha256"))
                    with self.assertRaises(ValueError):
                        verify_windows.verify_assets(self.directory, self.run)
                    target.unlink()
                    target.write_bytes(original)

    def test_artifact_shape(self):
        """test_artifact_shape rejects extra content, symlinks, and missing or empty sidecars."""
        extra = self.directory / "unexpected.ps1"
        extra.write_text("untrusted")
        with self.assertRaises(ValueError):
            verify_windows.verify_assets(self.directory, self.run)
        extra.unlink()
        target = self.directory / (self.name + ".sha256")
        original = target.read_text()
        for mode in ("missing", "empty", "symlink"):
            with self.subTest(mode=mode):
                target.unlink(missing_ok=True)
                if mode == "empty":
                    target.write_text("")
                elif mode == "symlink":
                    target.symlink_to(self.directory / self.name)
                with self.assertRaises(ValueError):
                    verify_windows.verify_assets(self.directory, self.run)
                target.unlink(missing_ok=True)
                target.write_text(original)


if __name__ == "__main__":
    unittest.main()
