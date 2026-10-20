"""Verify that one-off GUI checks cannot silently select different package bytes or product code."""

import hashlib
import json
from pathlib import Path
import tempfile
import unittest
from unittest.mock import patch

import verify


class PackageVerificationTests(unittest.TestCase):
    def fixture(self, root):
        version = "0.1.0-beta.1"
        names = [f"GoForj-Harbor-macos-arm64-{version}.pkg", "goforj-harbor_0.1.0~beta.1-1_amd64.deb"]
        for name in names:
            (root / name).write_bytes(b"native tested package")
            digest = hashlib.sha256((root / name).read_bytes()).hexdigest()
            (root / (name + ".sha256")).write_text(f"{digest}  {name}\n")
        revision = "a" * 40
        verify.prepare.seal_assets(root, version, revision, "https://github.com/goforj/harbor/actions/runs/123")
        run = {"id": 123, "head_sha": revision, "repository": {"full_name": "goforj/harbor"},
               "path": ".github/workflows/release.yml", "status": "completed", "conclusion": "success"}
        return run, names

    def test_native_package_identity(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            run, names = self.fixture(root)
            self.assertEqual(verify.verify_assets(root, run), ("a" * 40, *names))

    def test_outputs_only_admitted_platform_packages(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            run, names = self.fixture(root)
            output = root / "outputs"
            with patch.object(verify.sys, "argv", ["verify.py", "assets"]), \
                    patch.object(verify, "verify_assets", return_value=("a" * 40, *names)), \
                    patch.object(verify.Path, "read_text", return_value=json.dumps(run)), \
                    patch.dict(verify.os.environ, {"GITHUB_OUTPUT": str(output)}):
                verify.main()
            self.assertEqual(output.read_text(),
                             f"source_commit={'a' * 40}\npackage={names[0]}\nlinux_package={names[1]}\n")

    def test_rejects_untrusted_or_incomplete_runs(self):
        for key, value in (("repository", {"full_name": "other/repo"}), ("path", "other.yml"),
                           ("status", "in_progress"), ("conclusion", "failure"), ("head_sha", "b" * 40), ("id", 456)):
            with self.subTest(key=key), tempfile.TemporaryDirectory() as directory:
                root = Path(directory)
                run, _ = self.fixture(root)
                run[key] = value
                with self.assertRaises(ValueError):
                    verify.verify_assets(root, run)

    def test_rejects_changed_package_evidence(self):
        for change in ("bytes", "empty", "sidecar", "missing", "link", "sidecar-link", "manifest", "revision", "schema", "package-set"):
            with self.subTest(change=change), tempfile.TemporaryDirectory() as directory:
                root = Path(directory)
                run, names = self.fixture(root)
                package = root / names[0]
                if change == "bytes":
                    package.write_bytes(b"rebuilt")
                elif change == "empty":
                    package.write_bytes(b"")
                elif change == "sidecar":
                    (root / (names[0] + ".sha256")).write_text("wrong")
                elif change == "missing":
                    package.unlink()
                elif change in ("link", "sidecar-link"):
                    target = package if change == "link" else root / (names[0] + ".sha256")
                    original = target.with_suffix(".saved")
                    target.rename(original)
                    target.symlink_to(original)
                elif change == "manifest":
                    (root / "SHA256SUMS").write_text("wrong")
                else:
                    path = root / "provenance.json"
                    data = json.loads(path.read_text())
                    if change == "revision":
                        data["source_commit"] = "invalid"
                    elif change == "schema":
                        data["schema_version"] = 2
                    else:
                        data["packages"]["foreign.pkg"] = "0" * 64
                    path.write_text(json.dumps(data))
                with self.assertRaises(ValueError):
                    verify.verify_assets(root, run)

    def test_requires_matching_product_while_allowing_test_harness_changes(self):
        expected = {"desktop/main.go": ("file", "same"), "docs/current-state.md": "old"}
        verify.verify_product(expected, dict(expected, **{"docs/current-state.md": "new", "desktop/packageui/test.swift": "new"}))
        verify.verify_product(expected, dict(expected, **{".github/workflows/linux-package-ui.yml": "new"}))
        for current in ({}, {"desktop/main.go": ("file", "changed")},
                        dict(expected, **{"cmd/harbord/main.go": "new"}),
                        dict(expected, **{".github/workflows/macos-development-package.yml": "changed"})):
            with self.subTest(current=current), self.assertRaises(ValueError):
                verify.verify_product(expected, current)

    def test_product_tree_preserves_modes_and_links_without_checkout(self):
        entries = (b"100644 blob abc\tdesktop/main.go\0"
                   b"100755 blob def\tscripts/launcher\0"
                   b"120000 blob ghi\tcurrent\0"
                   b"100644 blob ignored\tdocs/current-state.md\0")
        with patch.object(verify.subprocess, "check_output", return_value=entries):
            self.assertEqual(verify.product_tree("a" * 40), {
                "desktop/main.go": ("100644", "abc"),
                "scripts/launcher": ("100755", "def"),
                "current": ("120000", "ghi"),
            })
        for entry in (b"160000 commit abc\tnested\0", b"100600 blob abc\tapp\0"):
            with self.subTest(entry=entry), patch.object(verify.subprocess, "check_output", return_value=entry):
                with self.assertRaises(ValueError):
                    verify.product_tree("a" * 40)


if __name__ == "__main__":
    unittest.main()
