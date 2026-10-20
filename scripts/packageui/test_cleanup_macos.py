"""Bound failed-GUI recovery to the installed test scenario."""

from contextlib import closing
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
            snapshots = [json.dumps({"projects": [project]}), json.dumps({"projects": [dict(project, state="stopped")]}), json.dumps({"projects": []})]

            def result(arguments, **kwargs):
                output = snapshots.pop(0) if arguments == [cleanup_macos.CLI, "daemon", "snapshot"] else ""
                if arguments[:2] == [cleanup_macos.CLI, "remove"]:
                    output = json.dumps({"operation": {"state": "succeeded"}})
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
            removal = [cleanup_macos.CLI, "remove", "fixture", "--intent", "intent-macos-ui-remove-fixture", "--json"]
            self.assertLess(commands.index([cleanup_macos.CLI, "stop", "fixture"]), commands.index(removal))
            self.assertLess(commands.index(removal),
                            commands.index([cleanup_macos.CLI, "daemon", "release"]))
            self.assertEqual(receipt["fixture_unregistered"], ["fixture"])

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

    def test_failed_start_unregisters_without_rejected_stop(self):
        with tempfile.TemporaryDirectory() as directory:
            home = Path(directory)
            database = home / "Library/Application Support/GoForj/Harbor/harbor.db"
            database.parent.mkdir(parents=True)
            database.write_bytes(b"retained state")
            root = home / "project"
            root.mkdir()
            for name in (".goforj.yml", "go.mod"):
                (root / name).write_text("retained project")
            project = {"path": str(root), "id": "fixture", "state": "failed"}
            snapshots = [{"projects": [project]}, {"projects": [project]}, {"projects": []}]

            def result(arguments, **kwargs):
                output = json.dumps(snapshots.pop(0)) if arguments == [cleanup_macos.CLI, "daemon", "snapshot"] else ""
                if arguments[:2] == [cleanup_macos.CLI, "remove"]:
                    output = json.dumps({"operation": {"state": "succeeded"}})
                return subprocess.CompletedProcess(arguments, 0, output, "")

            original_exists = Path.exists
            with patch.object(cleanup_macos.Path, "home", return_value=home), \
                    patch.object(cleanup_macos.Path, "exists", lambda path: False if str(path) == "/Applications/Harbor.app" else original_exists(path)), \
                    patch.object(cleanup_macos, "fixture_root", return_value=root), \
                    patch.object(cleanup_macos.subprocess, "run", side_effect=result) as run, \
                    patch.object(cleanup_macos, "verify_service_absence", return_value={"service_exit_code": 113}):
                cleanup_macos.recover(home / "evidence")
            commands = [call.args[0] for call in run.call_args_list]
            self.assertNotIn([cleanup_macos.CLI, "stop", "fixture"], commands)
            self.assertTrue(any(command[:3] == [cleanup_macos.CLI, "remove", "fixture"] for command in commands))
            self.assertIn([cleanup_macos.CLI, "daemon", "release"], commands)
            receipt = json.loads((home / "evidence/failure-cleanup.json").read_text())
            self.assertTrue(receipt["completed"])
            self.assertEqual(len(receipt["retained"]), 3)

    def test_unconfigured_empty_profile_has_no_network_release(self):
        with tempfile.TemporaryDirectory() as directory:
            home = Path(directory)
            database = home / "Library/Application Support/GoForj/Harbor/harbor.db"
            database.parent.mkdir(parents=True)
            database.write_bytes(b"retained empty profile")
            snapshot = {"network_setup": {"configured": False}, "projects": []}

            def result(arguments, **kwargs):
                output = json.dumps(snapshot) if arguments == [cleanup_macos.CLI, "daemon", "snapshot"] else ""
                return subprocess.CompletedProcess(arguments, 0, output, "")

            original_exists = Path.exists
            with patch.object(cleanup_macos.Path, "home", return_value=home), \
                    patch.object(cleanup_macos.Path, "exists", lambda path: False if str(path) == "/Applications/Harbor.app" else original_exists(path)), \
                    patch.object(cleanup_macos.subprocess, "run", side_effect=result) as run, \
                    patch.object(cleanup_macos, "verify_service_absence", return_value={"service_exit_code": 113}):
                cleanup_macos.recover(home / "evidence")
            receipt = json.loads((home / "evidence/failure-cleanup.json").read_text())
            self.assertTrue(receipt["completed"])
            self.assertEqual(receipt["network_release"], "not configured; no registered projects")
            self.assertEqual(len(receipt["retained"]), 1)
            self.assertNotIn([cleanup_macos.CLI, "daemon", "release"], [call.args[0] for call in run.call_args_list])

    def test_failed_unregister_prevents_release_and_uninstall(self):
        with tempfile.TemporaryDirectory() as directory:
            home = Path(directory)
            database = home / "Library/Application Support/GoForj/Harbor/harbor.db"
            database.parent.mkdir(parents=True)
            database.write_bytes(b"retained registration")
            snapshot = {"projects": [{"path": "/private/tmp/harbor-package-ui-8591F1BE-D15A-4E9E-BE3D-71F45B32AFED/project",
                                      "id": "fixture", "state": "stopped"}]}

            def result(arguments, **kwargs):
                if arguments[:3] == [cleanup_macos.CLI, "remove", "fixture"]:
                    return subprocess.CompletedProcess(arguments, 1, "", "remove refused")
                output = json.dumps(snapshot) if arguments == [cleanup_macos.CLI, "daemon", "snapshot"] else ""
                return subprocess.CompletedProcess(arguments, 0, output, "")

            with patch.object(cleanup_macos.Path, "home", return_value=home), \
                    patch.object(cleanup_macos.subprocess, "run", side_effect=result) as run:
                with self.assertRaises(subprocess.CalledProcessError):
                    cleanup_macos.recover(home / "evidence")
            commands = [call.args[0] for call in run.call_args_list]
            self.assertNotIn([cleanup_macos.CLI, "daemon", "release"], commands)
            self.assertFalse(any(command[:3] == ["sudo", "-n", cleanup_macos.INSTALLER] for command in commands))
            receipt = json.loads((home / "evidence/failure-cleanup.json").read_text())
            self.assertFalse(receipt["completed"])
            self.assertEqual(receipt["commands"][-1]["stderr"], "remove refused")
            self.assertEqual(database.read_bytes(), b"retained registration")

    def test_unregister_approves_exact_revision_and_replays_same_intent(self):
        project = {"id": "fixture", "path": "/private/tmp/harbor-package-ui-8591F1BE-D15A-4E9E-BE3D-71F45B32AFED/project"}
        pending = {"operation": {"id": "operation-fixture", "state": "requires_approval"}, "revision": 42}
        complete = {"operation": {"id": "operation-fixture", "state": "succeeded"}, "revision": 43}
        results = [subprocess.CompletedProcess([], 0, json.dumps(pending), ""),
                   subprocess.CompletedProcess([], 0, "approved", ""),
                   subprocess.CompletedProcess([], 0, json.dumps(complete), "")]
        with patch.dict(os.environ, {"HARBOR_PACKAGE_UI_APPROVAL_TOOL": "/fixture/approval"}), \
                patch.object(cleanup_macos.time, "sleep"), \
                patch.object(cleanup_macos.subprocess, "run", side_effect=results) as run:
            cleanup_macos.unregister_fixture(project, run)
        calls = [call.args[0] for call in run.call_args_list]
        self.assertEqual(calls[0], calls[2])
        self.assertEqual(calls[1], ["/fixture/approval", "--project-id", "fixture", "--operation-id", "operation-fixture",
                                   "--intent-id", "intent-macos-ui-remove-fixture", "--revision", "42", "--project-directory", project["path"]])

    def test_failed_approval_does_not_replay_or_release(self):
        project = {"id": "fixture", "path": "/private/tmp/harbor-package-ui-8591F1BE-D15A-4E9E-BE3D-71F45B32AFED/project"}
        pending = {"operation": {"id": "operation-fixture", "state": "requires_approval"}, "revision": 42}
        calls = []

        def run(arguments):
            calls.append(arguments)
            if arguments[0] == "/fixture/approval":
                raise subprocess.CalledProcessError(1, arguments)
            return subprocess.CompletedProcess(arguments, 0, json.dumps(pending), "")

        with patch.dict(os.environ, {"HARBOR_PACKAGE_UI_APPROVAL_TOOL": "/fixture/approval"}):
            with self.assertRaises(subprocess.CalledProcessError):
                cleanup_macos.unregister_fixture(project, run)
        self.assertEqual(len(calls), 2)


class CleanupDiagnosticsTests(unittest.TestCase):
    def test_timeout_records_partial_bytes_before_diagnostics_and_preserves_error(self):
        with tempfile.TemporaryDirectory() as directory:
            home = Path(directory)
            database = home / "Library/Application Support/GoForj/Harbor/harbor.db"
            database.parent.mkdir(parents=True)
            database.write_bytes(b"retained state")
            command = [cleanup_macos.CLI, "daemon", "release"]
            timeout = subprocess.TimeoutExpired(command, 180, output=b"partial output", stderr=b"partial error")
            outputs = [subprocess.CompletedProcess([], 0, "quit confirmed", ""),
                       subprocess.CompletedProcess([], 0, json.dumps({"projects": [], "network_setup": {"configured": True}}), ""),
                       subprocess.CompletedProcess([], 0, json.dumps({"projects": []}), ""), timeout]
            with patch.object(cleanup_macos.Path, "home", return_value=home), \
                    patch.object(cleanup_macos.subprocess, "run", side_effect=outputs), \
                    patch.object(cleanup_macos, "collect_release_diagnostics", side_effect=RuntimeError("diagnostic failed")) as diagnostics:
                with self.assertRaises(subprocess.TimeoutExpired) as raised:
                    cleanup_macos.recover(home / "evidence")
            self.assertIs(raised.exception, timeout)
            diagnostics.assert_called_once_with(home / "evidence")
            receipt = json.loads((home / "evidence/failure-cleanup.json").read_text())
            self.assertFalse(receipt["completed"])
            self.assertEqual(receipt["commands"][-1], {"arguments": command, "timed_out": True,
                "timeout_seconds": 180, "stdout": "partial output", "stderr": "partial error"})
            self.assertEqual(receipt["release_diagnostics_error"], "diagnostic failed")

    def test_diagnostics_allowlist_checkpoint_and_reject_foreign_process(self):
        import sqlite3
        with tempfile.TemporaryDirectory() as directory:
            home = Path(directory)
            database = home / "Library/Application Support/GoForj/Harbor/harbor.db"
            database.parent.mkdir(parents=True)
            with closing(sqlite3.connect(database)) as connection:
                connection.executescript("CREATE TABLE operations (id, intent_id, kind, state, phase, revision, secret);"
                    "INSERT INTO operations VALUES ('release', 'intent-network-release', 'network.release', 'running', 'release', 42, 'private');"
                    "CREATE TABLE network_global_release_plans (id, operation_id, phase, checkpoint_revision, network_revision, authority_payload);"
                    "INSERT INTO network_global_release_plans VALUES (1, 'release', 'low_ports', 42, 41, 'private');")
            evidence = home / "evidence"
            evidence.mkdir()
            def run(arguments, **kwargs):
                return subprocess.CompletedProcess(arguments, 0, "program = /foreign\npid = 123\n" if "launchctl" in arguments[0] else "{}", "")
            with patch.object(cleanup_macos.Path, "home", return_value=home), \
                    patch.object(cleanup_macos.subprocess, "run", side_effect=run) as observed, \
                    patch.object(cleanup_macos, "daemon_process_facts") as native:
                cleanup_macos.collect_release_diagnostics(evidence)
            result = json.loads((evidence / "release-timeout-diagnostics.json").read_text())
            self.assertEqual(result["checkpoint"][0]["phase"], "low_ports")
            self.assertNotIn("private", json.dumps(result))
            self.assertIn("service program differs", result["daemon_error"])
            native.assert_not_called()
            self.assertTrue(all("sample" not in call.args[0][0] for call in observed.call_args_list))

    @unittest.skipUnless(__import__("sys").platform == "darwin", "native libproc layout")
    def test_native_process_facts_match_current_python_process(self):
        facts = cleanup_macos.daemon_process_facts(os.getpid())
        self.assertEqual(facts["pid"], os.getpid())
        self.assertEqual(facts["uid"], os.getuid())
        self.assertGreater(facts["birth_seconds"], 0)
        self.assertGreaterEqual(facts["birth_microseconds"], 0)
        self.assertLess(facts["birth_microseconds"], 1000000)
        self.assertTrue(Path(facts["executable"]).is_absolute())


    def test_native_sampling_preserves_foreign_and_replaced_process_privacy(self):
        from types import SimpleNamespace
        baseline = {"pid": 123, "uid": os.getuid(), "executable": "/admitted/harbord",
                    "birth_seconds": 1234, "birth_microseconds": 567}
        cases = [
            ("wrong uid", [dict(baseline, uid=os.getuid() + 1)], False),
            ("wrong executable", [dict(baseline, executable="/foreign")], False),
            ("replacement before sample", [baseline, dict(baseline, birth_microseconds=568)], False),
            ("replacement during sample", [baseline, baseline, dict(baseline, birth_microseconds=568)], True),
            ("process vanished during sample", [baseline, baseline, RuntimeError("vanished")], True),
            ("matching process", [baseline, baseline, baseline], True),
        ]
        for name, facts, sampled in cases:
            with self.subTest(name=name), tempfile.TemporaryDirectory() as directory:
                evidence = Path(directory)
                sample = evidence / "release-daemon.sample.txt"
                def run(arguments, **kwargs):
                    if arguments[0] == "/usr/bin/sample":
                        sample.write_text("native stack")
                    output = "program = /Library/Application Support/GoForj/Harbor/daemon-launcher\npid = 123\n" if "launchctl" in arguments[0] else "{}"
                    return subprocess.CompletedProcess(arguments, 0, output, "")
                with patch.object(cleanup_macos.Path, "home", return_value=evidence), \
                        patch.object(cleanup_macos.Path, "resolve", return_value=Path("/admitted/harbord")), \
                        patch.object(cleanup_macos.Path, "stat", return_value=SimpleNamespace(st_uid=0, st_mode=0o100755)), \
                        patch.object(cleanup_macos.subprocess, "run", side_effect=run) as commands, \
                        patch.object(cleanup_macos, "daemon_process_facts", side_effect=facts):
                    cleanup_macos.collect_release_diagnostics(evidence)
                self.assertEqual(any(call.args[0][0] == "/usr/bin/sample" for call in commands.call_args_list), sampled)
                self.assertEqual(sample.exists(), name == "matching process")
                result = json.loads((evidence / "release-timeout-diagnostics.json").read_text())
                self.assertEqual("sample" in result, name == "matching process")

    def test_bounded_text_preserves_partial_utf8_and_limits_output(self):
        self.assertEqual(cleanup_macos.bounded_text(None), "")
        self.assertEqual(cleanup_macos.bounded_text(b"partial\xff"), "partial\ufffd")
        self.assertEqual(len(cleanup_macos.bounded_text("x" * 70000)), 65536)


    def test_normal_release_records_success_and_timeout_with_same_bound(self):
        for timed_out in (False, True):
            with self.subTest(timed_out=timed_out), tempfile.TemporaryDirectory() as directory:
                evidence = Path(directory)
                command = [cleanup_macos.CLI, "daemon", "release"]
                result = subprocess.TimeoutExpired(command, 180, output=b"partial", stderr=b"reason") if timed_out else subprocess.CompletedProcess(command, 0, "Network release complete.\n", "")
                with patch.object(cleanup_macos.subprocess, "run", side_effect=[result]) as run, \
                        patch.object(cleanup_macos, "collect_release_diagnostics") as diagnostics:
                    if timed_out:
                        with self.assertRaises(subprocess.TimeoutExpired):
                            cleanup_macos.release_network(evidence)
                        diagnostics.assert_called_once_with(evidence)
                    else:
                        cleanup_macos.release_network(evidence)
                        diagnostics.assert_not_called()
                self.assertEqual(run.call_args.kwargs["timeout"], 180)
                receipt = json.loads((evidence / "network-release-command.json").read_text())
                self.assertEqual(receipt["completed"], not timed_out)
                self.assertEqual(receipt["commands"][0]["arguments"], command)


if __name__ == "__main__":
    unittest.main()
