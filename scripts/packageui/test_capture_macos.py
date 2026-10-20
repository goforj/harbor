"""Keep failed-start evidence without following project paths outside the fixture."""

from pathlib import Path
import os
import plistlib
import re
import subprocess
import sys
import tempfile
import textwrap
import unittest
from unittest.mock import patch
from types import SimpleNamespace

import capture_macos


@unittest.skipUnless(sys.platform == "darwin", "native Swift path behavior requires macOS")
class RecoveryFixturePathTests(unittest.TestCase):
    def test_native_caller_matches_registered_canonical_path_and_rejects_drift(self):
        source = (Path(__file__).resolve().parents[2] / "desktop/packageui/macos/HarborPackageUITests.swift").read_text()
        method = re.search(r"    private func canonicalRecoveryProjectDirectory\([^\n]+[\s\S]*?\n    }", source)
        self.assertIsNotNone(method)
        function = textwrap.dedent(method.group()).replace("private func", "func", 1)
        program = "import Foundation\nimport Darwin\n" + function + r'''
let project = URL(fileURLWithPath: CommandLine.arguments[1], isDirectory: true)
let registered = CommandLine.arguments[2]
let canonical = try canonicalRecoveryProjectDirectory(project, registeredPath: registered)
precondition(canonical == registered)
precondition(canonical.hasPrefix("/private/tmp/"))
do {
    _ = try canonicalRecoveryProjectDirectory(project, registeredPath: project.path)
    fatalError("caller alias accepted as registered canonical path")
} catch {}
do {
    _ = try canonicalRecoveryProjectDirectory(project, registeredPath: registered + "-foreign")
    fatalError("different registered project accepted")
} catch {}
do {
    _ = try canonicalRecoveryProjectDirectory(project.appendingPathComponent("missing"), registeredPath: registered)
    fatalError("missing project accepted")
} catch {}
print(canonical)
'''
        with tempfile.TemporaryDirectory(prefix="harbor-package-ui-", dir="/tmp") as root:
            project = Path(root) / "project"
            project.mkdir()
            registered = str(project.resolve())
            result = subprocess.run(["/usr/bin/swift", "-", str(project), registered], input=program,
                                    text=True, capture_output=True, timeout=60, check=True)
            self.assertEqual(result.stdout.strip(), registered)


@unittest.skipIf(os.name == "nt", "macOS trace fixtures use POSIX paths and links")
class CaptureTraceTests(unittest.TestCase):
    def test_process_arguments_only_include_validated_fixture_descendants(self):
        with tempfile.TemporaryDirectory() as directory:
            temporary = Path(directory)
            project = temporary / "harbor-package-ui-71BE2309-89EF-4120-B1EE-3EAD3C1106EE/project"
            project.mkdir(parents=True)
            evidence = temporary / "evidence"
            processes = "PID PPID STAT ELAPSED TIME %CPU NI COMM\n100 1 S 00:01 00:01 1.0 0 forj\n101 100 R 00:01 00:01 1.0 0 node\n200 1 S 00:01 00:01 1.0 0 forj\n201 200 R 00:01 00:01 1.0 0 node\n"
            def observe(arguments, **kwargs):
                if arguments[1] == "-axo":
                    return SimpleNamespace(stdout=processes, stderr="")
                if arguments[0] == "/usr/sbin/lsof":
                    cwd = str(project) if arguments[3] == "100" else str(temporary / "foreign")
                    return SimpleNamespace(stdout="p" + arguments[3] + "\nn" + cwd + "\n", stderr="")
                self.assertEqual(arguments[2], "100,101")
                return SimpleNamespace(stdout="node vue-tsc --noEmit\n", stderr="")
            with patch.object(capture_macos.subprocess, "run", side_effect=observe):
                capture_macos.retain_process_stages(temporary, evidence)
                capture_macos.retain_process_stages(temporary, evidence)
            content = (evidence / "fixture-process-stages.txt").read_text()
            self.assertEqual(content.count("node vue-tsc --noEmit"), 2)

    def test_foreign_process_cwd_does_not_collect_arguments(self):
        with tempfile.TemporaryDirectory() as directory:
            temporary = Path(directory)
            responses = [SimpleNamespace(stdout="header\n100 1 S 00:01 00:01 1.0 0 forj\n", stderr=""), SimpleNamespace(stdout="p100\nn/other/project\n", stderr="")]
            with patch.object(capture_macos.subprocess, "run", side_effect=responses) as run:
                capture_macos.retain_process_stages(temporary, temporary / "evidence")
            self.assertEqual(run.call_count, 2)

    def test_linked_fixture_directory_does_not_collect_arguments(self):
        for shape in ("root", "project"):
            with self.subTest(shape=shape), tempfile.TemporaryDirectory() as directory:
                temporary = Path(directory)
                root = temporary / "harbor-package-ui-71BE2309-89EF-4120-B1EE-3EAD3C1106EE"
                foreign = temporary / "foreign"
                foreign.mkdir()
                if shape == "root":
                    (foreign / "project").mkdir()
                    root.symlink_to(foreign, target_is_directory=True)
                else:
                    root.mkdir()
                    (root / "project").symlink_to(foreign, target_is_directory=True)
                responses = [SimpleNamespace(stdout="header\n100 1 S 00:01 00:01 1.0 0 forj\n", stderr=""), SimpleNamespace(stdout="p100\nn" + str(root / "project") + "\n", stderr="")]
                with patch.object(capture_macos.subprocess, "run", side_effect=responses) as run:
                    capture_macos.retain_process_stages(temporary, temporary / "evidence")
                self.assertEqual(run.call_count, 2)

    def test_retains_last_trace_after_runtime_removes_it(self):
        with tempfile.TemporaryDirectory() as directory:
            temporary = Path(directory)
            root = temporary / "harbor-package-ui-71BE2309-89EF-4120-B1EE-3EAD3C1106EE"
            trace = root / "project/_data/harbor/forj-dev.log"
            trace.parent.mkdir(parents=True)
            trace.write_text("frontend dependencies still starting\n")
            package = root / "project/cmd/app/frontend/package.json"
            package.parent.mkdir(parents=True)
            package.write_text('{"scripts":{"build":"vite build"}}')
            evidence = temporary / "evidence"
            capture_macos.retain_traces(temporary, evidence)
            trace.unlink()
            capture_macos.retain_traces(temporary, evidence)
            self.assertEqual((evidence / (root.name + "-live-launch.log")).read_text(), "frontend dependencies still starting\n")
            self.assertEqual((evidence / (root.name + "-cmd-app-frontend-package.json")).read_text(), package.read_text())

    def test_only_copies_npm_logs_naming_the_fixture(self):
        with tempfile.TemporaryDirectory() as directory:
            temporary = Path(directory)
            root = temporary / "harbor-package-ui-71BE2309-89EF-4120-B1EE-3EAD3C1106EE"
            trace = root / "project/_data/harbor/forj-dev.log"
            trace.parent.mkdir(parents=True)
            trace.write_text("starting npm")
            logs = temporary / "logs"
            logs.mkdir()
            (logs / "fixture-debug-0.log").write_text("cwd " + str(root / "project/cmd/app/frontend"))
            (logs / "foreign-debug-0.log").write_text("another project")
            evidence = temporary / "evidence"
            capture_macos.retain_traces(temporary, evidence, logs)
            self.assertEqual(len(list(evidence.glob("*-npm-*"))), 1)
            self.assertTrue((evidence / (root.name + "-npm-fixture-debug-0.log")).is_file())

    def test_rejects_linked_or_foreign_traces(self):
        with tempfile.TemporaryDirectory() as directory:
            temporary = Path(directory)
            foreign = temporary / "foreign"
            foreign.write_text("outside fixture")
            trace = temporary / "harbor-package-ui-71BE2309-89EF-4120-B1EE-3EAD3C1106EE/project/_data/harbor/forj-dev.log"
            trace.parent.mkdir(parents=True)
            trace.symlink_to(foreign)
            evidence = temporary / "evidence"
            capture_macos.retain_traces(temporary, evidence)
            self.assertFalse(evidence.exists())


class NativeRunnerEnvironmentTests(unittest.TestCase):
    def environment(self):
        return {"HARBOR_PACKAGE_UI_CRASH_RECOVERY_TOOL": "/worker/harbor-crash-recovery", "PATH": "/worker/tools:/usr/bin:/bin",
                "GITHUB_ACTIONS": "true", "GITHUB_REPOSITORY": "goforj/harbor",
                "RUNNER_ENVIRONMENT": "github-hosted", "RUNNER_TEMP": "/worker/temp"}

    def configuration(self):
        return {"HarborPackageUITests": {"BlueprintName": "HarborPackageUITests", "TestBundlePath": "__TESTROOT__/test.xctest",
                                        "EnvironmentVariables": {"existing": "preserved", "PATH": "$(UNRESOLVED)"}},
                "__xctestrun_metadata__": {"FormatVersion": 1}}

    def test_forwards_actual_worker_values_and_preserves_native_test_selection(self):
        with tempfile.TemporaryDirectory() as root:
            products = Path(root)
            path = products / "HarborPackageUITests.xctestrun"
            path.write_bytes(plistlib.dumps(self.configuration()))
            self.assertEqual(capture_macos.configure_test_runner(products, self.environment()), path)
            configured = plistlib.loads(path.read_bytes())
            target = configured["HarborPackageUITests"]
            self.assertEqual(target["TestBundlePath"], "__TESTROOT__/test.xctest")
            self.assertEqual(target["EnvironmentVariables"], dict(self.environment(), existing="preserved"))

    def test_missing_worker_value_preserves_configuration(self):
        for key in self.environment():
            with self.subTest(key=key), tempfile.TemporaryDirectory() as root:
                products = Path(root)
                path = products / "HarborPackageUITests.xctestrun"
                original = plistlib.dumps(self.configuration())
                path.write_bytes(original)
                environment = self.environment()
                del environment[key]
                with self.assertRaises(ValueError):
                    capture_macos.configure_test_runner(products, environment)
                self.assertEqual(path.read_bytes(), original)

    def test_rejects_ambiguous_or_substituted_test_target(self):
        for change in (lambda data: data.update(ForeignTarget={}),
                       lambda data: data["HarborPackageUITests"].update(BlueprintName="ForeignTarget"),
                       lambda data: data["HarborPackageUITests"].update(EnvironmentVariables=[])):
            with self.subTest(change=change), tempfile.TemporaryDirectory() as root:
                products = Path(root)
                path = products / "HarborPackageUITests.xctestrun"
                data = self.configuration()
                change(data)
                original = plistlib.dumps(data)
                path.write_bytes(original)
                with self.assertRaises(ValueError):
                    capture_macos.configure_test_runner(products, self.environment())
                self.assertEqual(path.read_bytes(), original)
        with tempfile.TemporaryDirectory() as root:
            products = Path(root)
            for name in ("first", "second"):
                (products / (name + ".xctestrun")).write_bytes(plistlib.dumps(self.configuration()))
            with self.assertRaises(ValueError):
                capture_macos.configure_test_runner(products, self.environment())


if __name__ == "__main__":
    unittest.main()
