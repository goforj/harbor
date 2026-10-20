"""Bound failed-GUI recovery to the installed test scenario."""

from pathlib import Path
import json
import os
import subprocess
import tempfile
import unittest
from unittest.mock import patch

import cleanup_macos


@unittest.skipIf(os.name == "nt", "macOS recovery uses POSIX paths and user IDs")
class CleanupFixtureTests(unittest.TestCase):
    def test_admits_only_disposable_project_directories(self):
        expected = Path("/private/tmp/harbor-package-ui-8591F1BE-D15A-4E9E-BE3D-71F45B32AFED/project")
        self.assertEqual(cleanup_macos.fixture_root({"path": str(expected)}), expected)
        for name in ("/Users/runner/work/project", str(expected / "../foreign"),
                     str(expected).replace("harbor-package-ui-", "other-"),
                     "/private/tmp/harbor-package-ui-/project"):
            with self.subTest(path=name), self.assertRaises(ValueError):
                cleanup_macos.fixture_root({"path": name})

    def test_foreign_registration_prevents_stop_and_uninstall(self):
        with tempfile.TemporaryDirectory() as directory:
            home = Path(directory)
            database = home / "Library/Application Support/GoForj/Harbor/harbor.db"
            database.parent.mkdir(parents=True)
            database.write_bytes(b"retained state")
            snapshot = {"projects": [{"path": "/Users/runner/work/foreign", "id": "foreign"}]}
            result = subprocess.CompletedProcess([], 0, json.dumps(snapshot), "")
            with patch.object(cleanup_macos.Path, "home", return_value=home), \
                    patch.object(cleanup_macos.subprocess, "run", side_effect=[subprocess.CompletedProcess([], 0, "quit confirmed", ""), result]) as run:
                with self.assertRaisesRegex(ValueError, "outside the disposable GUI fixture"):
                    cleanup_macos.recover(home / "evidence")
            self.assertEqual(run.call_count, 2)
            self.assertEqual(run.call_args.args[0], [cleanup_macos.CLI, "daemon", "snapshot"])
            receipt = json.loads((home / "evidence/failure-cleanup.json").read_text())
            self.assertFalse(receipt["completed"])
            self.assertIn("outside the disposable GUI fixture", receipt["error"])
            self.assertEqual(database.read_bytes(), b"retained state")

    def test_exact_service_absence_requires_reachable_domain(self):
        missing = f'Bad request.\nCould not find service "com.goforj.harbor.daemon" in domain for user gui: {os.getuid()}\n'
        domain = subprocess.CompletedProcess([], 0, "domain", "")
        absent = subprocess.CompletedProcess([], 113, "", missing)
        with patch.object(cleanup_macos.subprocess, "run", side_effect=[domain, absent]):
            self.assertEqual(cleanup_macos.verify_service_absence()["service_exit_code"], 113)
        for result in (subprocess.CompletedProcess([], 0, "pid = 123", ""),
                       subprocess.CompletedProcess([], 1, "", "permission denied"),
                       subprocess.CompletedProcess([], 113, "", "domain unavailable")):
            with self.subTest(result=result), patch.object(cleanup_macos.subprocess, "run", side_effect=[domain, result]):
                with self.assertRaises(RuntimeError):
                    cleanup_macos.verify_service_absence()
        with patch.object(cleanup_macos.subprocess, "run", return_value=subprocess.CompletedProcess([], 113, "", "domain unavailable")):
            with self.assertRaises(subprocess.CalledProcessError):
                cleanup_macos.verify_service_absence()

    def test_completed_recovery_preserves_files_after_verified_absence(self):
        with tempfile.TemporaryDirectory() as directory:
            home = Path(directory)
            database = home / "Library/Application Support/GoForj/Harbor/harbor.db"
            database.parent.mkdir(parents=True)
            database.write_bytes(b"retained state")
            root = home / "project"
            root.mkdir()
            for name in (".goforj.yml", "go.mod"):
                (root / name).write_text("retained project")
            project = {"path": str(root), "id": "fixture", "state": "ready"}
            snapshots = [json.dumps({"projects": [project]}), json.dumps({"projects": [dict(project, state="stopped")]})]

            def result(arguments, **kwargs):
                output = snapshots.pop(0) if arguments == [cleanup_macos.CLI, "daemon", "snapshot"] else ""
                return subprocess.CompletedProcess(arguments, 0, output, "")

            original_exists = Path.exists
            with patch.object(cleanup_macos.Path, "home", return_value=home), \
                    patch.object(cleanup_macos.Path, "exists", lambda path: False if str(path) == "/Applications/Harbor.app" else original_exists(path)), \
                    patch.object(cleanup_macos, "fixture_root", return_value=root), \
                    patch.object(cleanup_macos.subprocess, "run", side_effect=result) as run, \
                    patch.object(cleanup_macos, "verify_service_absence", return_value={"service_exit_code": 113}):
                cleanup_macos.recover(home / "evidence")
            receipt = json.loads((home / "evidence/failure-cleanup.json").read_text())
            self.assertTrue(receipt["completed"])
            self.assertEqual(len(receipt["retained"]), 3)
            commands = [call.args[0] for call in run.call_args_list]
            self.assertEqual(commands[0], ["/usr/bin/swift", "desktop/packageui/macos/QuitInstalledHarbor.swift"])
            self.assertLess(commands.index([cleanup_macos.CLI, "stop", "fixture"]),
                            commands.index([cleanup_macos.CLI, "daemon", "release"]))

    def test_failed_stop_retains_error_without_uninstalling(self):
        with tempfile.TemporaryDirectory() as directory:
            home = Path(directory)
            database = home / "Library/Application Support/GoForj/Harbor/harbor.db"
            database.parent.mkdir(parents=True)
            database.write_bytes(b"retained state")
            snapshot = {"projects": [{"path": "/private/tmp/harbor-package-ui-8591F1BE-D15A-4E9E-BE3D-71F45B32AFED/project",
                                      "id": "fixture"}]}
            results = [subprocess.CompletedProcess([], 0, "quit confirmed", ""),
                       subprocess.CompletedProcess([], 0, json.dumps(snapshot), ""),
                       subprocess.CompletedProcess([cleanup_macos.CLI, "stop", "fixture"], 1, "", "stop refused")]
            with patch.object(cleanup_macos.Path, "home", return_value=home), \
                    patch.object(cleanup_macos.subprocess, "run", side_effect=results) as run:
                with self.assertRaises(subprocess.CalledProcessError):
                    cleanup_macos.recover(home / "evidence")
            self.assertEqual(run.call_count, 3)
            receipt = json.loads((home / "evidence/failure-cleanup.json").read_text())
            self.assertFalse(receipt["completed"])
            self.assertEqual(receipt["commands"][-1]["stderr"], "stop refused")
            self.assertEqual(database.read_bytes(), b"retained state")


if __name__ == "__main__":
    unittest.main()
