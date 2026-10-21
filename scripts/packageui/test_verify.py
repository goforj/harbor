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
        names = [name.replace("~", ".") for name in names]
        run = {"id": 123, "head_sha": revision, "repository": {"full_name": "goforj/harbor"},
               "path": ".github/workflows/release.yml", "status": "completed", "conclusion": "success"}
        return run, names

    def test_native_package_identity(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            run, names = self.fixture(root)
            self.assertEqual(verify.verify_assets(root, run), ("a" * 40, *names))

    def test_accepts_exact_legacy_sealed_debian_names(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            run, names = self.fixture(root)
            current = names[1]
            legacy = current.replace("0.1.0.beta", "0.1.0~beta")
            (root / current).rename(root / legacy)
            (root / (current + ".sha256")).rename(root / (legacy + ".sha256"))
            for name in (legacy + ".sha256", "SHA256SUMS", "provenance.json"):
                path = root / name
                path.write_text(path.read_text().replace(current, legacy))
            self.assertEqual(verify.verify_assets(root, run), ("a" * 40, names[0], legacy))

    def test_rejects_a_manifest_containing_both_debian_names(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            run, names = self.fixture(root)
            path = root / "provenance.json"
            data = json.loads(path.read_text())
            data["packages"][names[1].replace("0.1.0.beta", "0.1.0~beta")] = data["packages"][names[1]]
            path.write_text(json.dumps(data))
            with self.assertRaisesRegex(ValueError, "unexpected package publication set"):
                verify.verify_assets(root, run)

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
        verify.verify_product(expected, dict(expected, **{"desktop/frontend/e2e/harbor.spec.ts": "new assertions"}))
        verify.verify_product(expected, dict(expected, **{"desktop/project_prerequisites_test.go": "native path fixture"}))
        for name in ("windows-package-ui.yml", "windows-native-capacity.yml", "windows-installation-admission.yml", "ci.yml", "docker-project-diagnostics.yml"):
            verify.verify_product(expected, dict(expected, **{f".github/workflows/{name}": "new"}))
        verify.verify_product(expected, dict(expected, **{"internal/platform/loopback/backend_windows_diagnostic_integration_test.go": "new"}))
        for path in ("internal/reconcile/project_docker_acceptance_test.go", "internal/reconcile/project_docker_diagnostics_test.go",
                     "internal/testkit/goforjproject/project.go", "internal/testkit/goforjproject/project_test.go",
                     "internal/platformproof/project_identity_test.go"):
            verify.verify_product(expected, dict(expected, **{path: "diagnostics"}))
        for current in ({}, {"desktop/main.go": ("file", "changed")},
                        dict(expected, **{"desktop/project_prerequisites.go": "changed"}),
                        dict(expected, **{"desktop/frontend/src/components/harbor/LogViewer.vue": "changed"}),
                        dict(expected, **{"cmd/harbord/main.go": "new"}),
                        dict(expected, **{"internal/platform/loopback/backend_windows.go": "changed"}),
                        dict(expected, **{".github/workflows/macos-development-package.yml": "changed"}),
                        dict(expected, **{".github/workflows/windows-development-package.yml": "changed"}),
                        dict(expected, **{"internal/testkit/goforjproject/snapshot.go": "changed"}),
                        dict(expected, **{"internal/testkit/goforjproject/doc.go": "changed"}),
                        dict(expected, **{"internal/reconcile/project_docker.go": "changed"}),
                        dict(expected, **{"internal/platformproof/project_identity.go": "changed"}),
                        dict(expected, **{"internal/projectprocess/output_broker_server.go": "changed"}),
                        dict(expected, **{"internal/reconcile/project_docker_diagnostics.go": "changed"})):
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
