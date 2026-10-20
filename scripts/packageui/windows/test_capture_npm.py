"""Verify that npm diagnostics cannot retain unrelated logs or secret-bearing fields."""
from datetime import datetime, timedelta, timezone
import importlib.util
import json
import os
from pathlib import Path
import tempfile
import unittest

spec = importlib.util.spec_from_file_location("capture_npm", Path(__file__).with_name("capture_npm.py"))
capture_npm = importlib.util.module_from_spec(spec)
spec.loader.exec_module(capture_npm)


class CaptureNpmTests(unittest.TestCase):
    """CaptureNpmTests exercises fixture attribution, size bounds and redaction."""

    def setUp(self):
        self.temporary = tempfile.TemporaryDirectory()
        self.addCleanup(self.temporary.cleanup)
        self.root = Path(self.temporary.name).resolve()
        self.fixture = self.root / "project"
        self.frontend = self.fixture / "cmd/app/frontend"
        self.frontend.mkdir(parents=True)
        self.logs = self.root / "npm-cache/_logs"
        self.logs.mkdir(parents=True)
        self.evidence = self.root / "evidence"
        self.after = datetime.now(timezone.utc) - timedelta(seconds=10)

    def log(self, seconds=1, cwd=None, extra=""):
        """log creates one npm-shaped file with an explicit launch-time and cwd binding."""
        stamp = self.after + timedelta(seconds=seconds)
        name = stamp.strftime("%Y-%m-%dT%H_%M_%S_") + f"{stamp.microsecond // 1000:03d}Z-debug-0.log"
        path = self.logs / name
        path.write_text(f"0 info using npm@10.9.9\n1 verbose cwd {cwd or self.frontend}\n" + extra)
        os.utime(path, (stamp.timestamp(), stamp.timestamp()))
        return path

    def test_matching_log_retains_only_public_timing(self):
        """test_matching_log_retains_only_public_timing excludes argv, config and URL secrets."""
        logfile = self.log(extra="2 verbose argv --password secret\n3 silly config token=secret\n"
                           "4 timing command:install Completed in 71000ms\n"
                           "5 http fetch GET 200 https://user:secret@registry.npmjs.org/pkg?token=secret 700ms (cache miss)\n")
        timing = self.logs / (logfile.name.split("-debug-")[0] + "-timing.json")
        timing.write_text(json.dumps({"metadata": {"command": ["secret"]}, "timers": {"command:install": 71000}}))
        self.assertEqual(capture_npm.capture(self.fixture, self.logs, self.evidence, self.after), 1)
        output = next(self.evidence.glob("*.json")).read_text()
        self.assertNotIn("secret", output)
        self.assertIn("71000", output)
        self.assertIn("registry.npmjs.org 700ms", output)
        self.assertLessEqual(len(output.encode()), capture_npm.LIMIT)

    def test_foreign_missing_cwd_and_old_logs_are_omitted(self):
        """test_foreign_missing_cwd_and_old_logs_are_omitted requires both exact attribution and launch time."""
        self.log(seconds=-1)
        self.log(seconds=1, cwd=self.root / "foreign")
        self.log(seconds=2).write_text("0 verbose argv --prefix secret\n")
        self.assertEqual(capture_npm.capture(self.fixture, self.logs, self.evidence, self.after), 0)
        self.assertFalse(list(self.evidence.glob("*.json")))

    def test_links_are_omitted(self):
        """test_links_are_omitted rejects linked source files and linked npm directories."""
        target = self.log()
        linked = self.logs / target.name.replace("debug-0", "debug-1")
        linked.symlink_to(target)
        target.unlink()
        self.assertEqual(capture_npm.capture(self.fixture, self.logs, self.evidence, self.after), 0)
        linked.unlink()
        self.logs.rmdir()
        real = self.root / "real-logs"
        real.mkdir()
        self.logs.symlink_to(real, target_is_directory=True)
        self.assertEqual(capture_npm.capture(self.fixture, self.logs, self.evidence, self.after), 0)

    def test_maximum_three_logs_and_bounded_tail(self):
        """test_maximum_three_logs_and_bounded_tail keeps repeated samples within fixed file and byte limits."""
        for index in range(1, 6):
            self.log(seconds=index, extra="2 timing command:install Completed in 70000ms\n" * 6000)
        self.assertEqual(capture_npm.capture(self.fixture, self.logs, self.evidence, self.after), 3)
        self.log(seconds=7)
        capture_npm.capture(self.fixture, self.logs, self.evidence, self.after)
        outputs = list(self.evidence.glob("*.json"))
        self.assertEqual(len(outputs), 3)
        self.assertTrue(all(p.stat().st_size <= capture_npm.LIMIT for p in outputs))


if __name__ == "__main__":
    unittest.main()
