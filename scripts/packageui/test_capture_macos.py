"""Keep failed-start evidence without following project paths outside the fixture."""

from pathlib import Path
import os
import tempfile
import unittest
from unittest.mock import patch
from types import SimpleNamespace

import capture_macos


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


if __name__ == "__main__":
    unittest.main()
