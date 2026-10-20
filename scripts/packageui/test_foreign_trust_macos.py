"""Validate exact foreign trust preservation and bounded fixture-only cleanup."""

import json
import os
from pathlib import Path
import plistlib
import subprocess
import tempfile
import unittest
from unittest.mock import patch

import foreign_trust_macos as fixture


@unittest.skipIf(os.name == "nt", "macOS fixture uses POSIX certificate file permissions")
class ForeignTrustFixture(unittest.TestCase):
    def setUp(self):
        self.temporary = tempfile.TemporaryDirectory()
        self.addCleanup(self.temporary.cleanup)
        self.directory = Path(self.temporary.name) / "fixture"
        self.directory.mkdir()
        self.evidence = Path(self.temporary.name) / "evidence"
        self.evidence.mkdir()
        self.identity = {"sha1": "ABC", "sha256": "def"}
        self.baseline = {"common_name": "unique fixture", "certificate": self.identity}
        (self.directory / "baseline.json").write_text(json.dumps(self.baseline))
        (self.directory / "baseline-entry.plist").write_bytes(b"exact trust")
        self.calls = []

    def native_run(self, arguments):
        self.calls.append(arguments)
        if "find-certificate" in arguments:
            return subprocess.CompletedProcess(arguments, 0, b"-----BEGIN CERTIFICATE-----\nfixture\n", b"")
        return subprocess.CompletedProcess(arguments, 0, b"", b"")

    def test_native_commands_are_bounded(self):
        with patch.object(fixture.subprocess, "run") as run:
            fixture.run(["security", "test"])
        self.assertEqual(run.call_args.kwargs["timeout"], 30)
        self.assertTrue(run.call_args.kwargs["check"])

    def test_verify_requires_exact_certificate_and_trust(self):
        with patch.object(fixture, "run", side_effect=self.native_run), patch.object(fixture, "certificate_identity", return_value=self.identity), patch.object(fixture, "trust_entry", return_value=b"exact trust"):
            fixture.verify(self.directory, self.evidence, "after-release")
        receipt = json.loads((self.evidence / "foreign-trust-after-release.json").read_text())
        self.assertTrue(receipt["exact_certificate_and_administrator_trust_preserved"])
        self.assertIn("last-root consent unproven", receipt["profile"])

    def test_verify_rejects_generated_certificate_drift(self):
        with patch.object(fixture, "certificate_identity", return_value={}):
            with self.assertRaisesRegex(RuntimeError, "certificate changed"):
                fixture.verify(self.directory, self.evidence, "after-release")

    def test_verify_rejects_ambiguous_keychain_certificate(self):
        with patch.object(fixture, "certificate_identity", return_value=self.identity), patch.object(fixture, "run", return_value=subprocess.CompletedProcess([], 0, b"", b"")):
            with self.assertRaisesRegex(RuntimeError, "missing or ambiguous"):
                fixture.verify(self.directory, self.evidence, "after-release")

    def test_verify_rejects_keychain_certificate_drift(self):
        with patch.object(fixture, "run", side_effect=self.native_run), patch.object(fixture, "certificate_identity", side_effect=[self.identity, {}]):
            with self.assertRaisesRegex(RuntimeError, "certificate differs"):
                fixture.verify(self.directory, self.evidence, "after-release")

    def test_verify_rejects_trust_attribute_drift(self):
        with patch.object(fixture, "run", side_effect=self.native_run), patch.object(fixture, "certificate_identity", return_value=self.identity), patch.object(fixture, "trust_entry", return_value=b"drift"):
            with self.assertRaisesRegex(RuntimeError, "attributes changed"):
                fixture.verify(self.directory, self.evidence, "after-release")

    def test_export_retains_only_exact_fixture_entry(self):
        for entries, required, expected in [({"abc": {"value": 1}, "OTHER": {"private": 2}}, True, {"value": 1}), ({}, False, None)]:
            with self.subTest(entries=entries), patch.object(fixture, "run", side_effect=[subprocess.CompletedProcess([], 0, b"", b""), subprocess.CompletedProcess([], 0, plistlib.dumps({"trustList": entries}), b"")]):
                entry = fixture.trust_entry(self.identity, required)
                self.assertEqual(plistlib.loads(entry) if entry else None, expected)
        for entries in [{}, {"abc": {}, "ABC": {}}]:
            with patch.object(fixture, "run", side_effect=[subprocess.CompletedProcess([], 0, b"", b""), subprocess.CompletedProcess([], 0, plistlib.dumps({"trustList": entries}), b"")]):
                with self.assertRaisesRegex(RuntimeError, "missing or ambiguous"):
                    fixture.trust_entry(self.identity)

    def test_seed_records_only_public_fixture_baseline(self):
        directory = Path(self.temporary.name) / "new-fixture"
        def generate(arguments):
            if "req" in arguments:
                Path(arguments[arguments.index("-keyout") + 1]).write_text("private key")
                Path(arguments[arguments.index("-out") + 1]).write_text("public certificate")
            return subprocess.CompletedProcess(arguments, 0, b"", b"")
        with patch.dict(fixture.os.environ, {"GITHUB_RUN_ID": "123"}), patch.object(fixture, "run", side_effect=generate), patch.object(fixture, "certificate_identity", return_value=self.identity), patch.object(fixture, "trust_entry", return_value=b"exact trust"), patch.object(fixture, "verify") as verify:
            fixture.seed(directory, self.evidence)
        self.assertEqual((directory / "foreign-root-key.pem").stat().st_mode & 0o777, 0o600)
        self.assertEqual(json.loads((directory / "baseline.json").read_text())["certificate"], self.identity)
        self.assertFalse(any("key" in path.name for path in self.evidence.iterdir()))
        verify.assert_called_once_with(directory, self.evidence, "before-harbor")

    def test_cleanup_without_admitted_baseline_does_not_mutate(self):
        (self.directory / "baseline.json").unlink()
        with patch.object(fixture, "run") as run:
            fixture.cleanup(self.directory, self.evidence)
        run.assert_not_called()

    def test_cleanup_preserves_drifted_foreign_fixture(self):
        with patch.object(fixture, "verify", side_effect=RuntimeError("drift")), patch.object(fixture, "run") as run:
            fixture.cleanup(self.directory, self.evidence)
        run.assert_not_called()
        self.assertFalse(self.cleanup_receipt()["removed"])

    def cleanup_receipt(self):
        return json.loads((self.evidence / "foreign-trust-fixture-cleanup.json").read_text())

    def test_last_root_timeout_stops_before_certificate_deletion(self):
        with patch.object(fixture, "verify", return_value=self.baseline), patch.object(fixture, "run", side_effect=subprocess.TimeoutExpired("security", 30)) as run:
            fixture.cleanup(self.directory, self.evidence)
        self.assertEqual(run.call_count, 1)
        self.assertIn("remove-trusted-cert", run.call_args.args[0])
        self.assertFalse(self.cleanup_receipt()["removed"])
        self.assertFalse(self.cleanup_receipt()["full_trust_settings_restoration_proven"])

    def test_cleanup_requires_exact_absence_after_normal_commands(self):
        absent = subprocess.CalledProcessError(44, "security", output=b"", stderr=b"not found")
        with patch.object(fixture, "verify", return_value=self.baseline), patch.object(fixture, "trust_entry", return_value=None), patch.object(fixture, "run", side_effect=[None, None, absent]) as run:
            fixture.cleanup(self.directory, self.evidence)
        self.assertTrue(self.cleanup_receipt()["removed"])
        self.assertEqual(run.call_args_list[1].args[0][-2], self.identity["sha1"])

    def test_cleanup_rejects_remaining_trust_or_certificate_or_wrong_absence(self):
        for entry, result in [(b"retained", None), (None, None), (None, subprocess.CalledProcessError(1, "security")), (None, subprocess.CalledProcessError(44, "security", output=b"unexpected"))]:
            with self.subTest(entry=entry, result=result), patch.object(fixture, "verify", return_value=self.baseline), patch.object(fixture, "trust_entry", return_value=entry), patch.object(fixture, "run", side_effect=[None, None, result]):
                fixture.cleanup(self.directory, self.evidence)
                self.assertFalse(self.cleanup_receipt()["removed"])


if __name__ == "__main__":
    unittest.main()
