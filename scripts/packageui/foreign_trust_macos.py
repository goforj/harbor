"""Prove foreign trust preservation in the disposable nonempty administrator-store profile."""

import hashlib
import json
import os
from pathlib import Path
import plistlib
import shutil
import subprocess
import sys
import tempfile


KEYCHAIN = "/Library/Keychains/System.keychain"
PROFILE = "nonempty administrator trust; interactive and last-root consent unproven"


def run(arguments):
    """run bounds native fixture commands without extending the Harbor release budget."""
    return subprocess.run(arguments, check=True, capture_output=True, timeout=30)


def certificate_identity(certificate):
    """certificate_identity binds trust records and cleanup to the generated public certificate."""
    der = run(["openssl", "x509", "-in", str(certificate), "-outform", "DER"]).stdout
    return {"sha1": hashlib.sha1(der).hexdigest().upper(),
            "sha256": hashlib.sha256(der).hexdigest()}


def trust_entry(identity, required=True):
    """trust_entry exports administrator settings but retains only the fixture certificate entry."""
    with tempfile.TemporaryDirectory(prefix="harbor-ui-trust-export-") as temporary:
        destination = Path(temporary) / "settings.plist"
        run(["sudo", "-n", "/usr/bin/security", "trust-settings-export", "-d", str(destination)])
        settings = plistlib.loads(run(["sudo", "-n", "/bin/cat", str(destination)]).stdout)
    entries = settings.get("trustList", {})
    matches = [entry for key, entry in entries.items() if key.upper() == identity["sha1"]]
    if not matches and not required:
        return None
    if len(matches) != 1:
        raise RuntimeError("foreign fixture administrator trust entry is missing or ambiguous")
    return plistlib.dumps(matches[0], sort_keys=True)


def verify(directory, evidence, stage):
    """verify requires the exact foreign certificate and trust attributes to survive Harbor cleanup."""
    baseline = json.loads((directory / "baseline.json").read_text())
    certificate = directory / "foreign-root.pem"
    if certificate_identity(certificate) != baseline["certificate"]:
        raise RuntimeError("foreign fixture certificate changed")
    name = baseline["common_name"]
    found = run(["/usr/bin/security", "find-certificate", "-a", "-c", name, "-p", KEYCHAIN]).stdout
    if found.count(b"-----BEGIN CERTIFICATE-----") != 1:
        raise RuntimeError("foreign fixture keychain certificate is missing or ambiguous")
    with tempfile.TemporaryDirectory(prefix="harbor-ui-trust-cert-") as temporary:
        exported = Path(temporary) / "certificate.pem"
        exported.write_bytes(found)
        if certificate_identity(exported) != baseline["certificate"]:
            raise RuntimeError("foreign fixture keychain certificate differs")
    if trust_entry(baseline["certificate"]) != (directory / "baseline-entry.plist").read_bytes():
        raise RuntimeError("foreign fixture administrator trust attributes changed")
    run(["sudo", "-n", "/usr/bin/security", "verify-cert", "-c", str(certificate), "-p", "ssl"])
    receipt = {"profile": PROFILE, "stage": stage, "certificate": baseline["certificate"],
               "exact_certificate_and_administrator_trust_preserved": True}
    (evidence / ("foreign-trust-" + stage + ".json")).write_text(json.dumps(receipt, indent=2) + "\n")
    return baseline


def seed(directory, evidence):
    """seed creates only this run's explicit foreign baseline before Harbor obtains trust."""
    directory.mkdir(mode=0o700)
    name = "Harbor UI Foreign Trust Baseline " + os.environ["GITHUB_RUN_ID"]
    certificate = directory / "foreign-root.pem"
    key = directory / "foreign-root-key.pem"
    run(["openssl", "req", "-x509", "-newkey", "rsa:2048", "-nodes", "-days", "1",
         "-subj", "/CN=" + name, "-keyout", str(key), "-out", str(certificate)])
    key.chmod(0o600)
    certificate.chmod(0o644)
    identity = certificate_identity(certificate)
    run(["sudo", "-n", "/usr/bin/security", "add-trusted-cert", "-d", "-r", "trustRoot",
         "-k", KEYCHAIN, str(certificate)])
    (directory / "baseline-entry.plist").write_bytes(trust_entry(identity))
    (directory / "baseline.json").write_text(json.dumps({"common_name": name, "certificate": identity}) + "\n")
    shutil.copyfile(certificate, evidence / "foreign-trust-certificate.pem")
    shutil.copyfile(directory / "baseline-entry.plist", evidence / "foreign-trust-baseline-entry.plist")
    verify(directory, evidence, "before-harbor")


def cleanup(directory, evidence):
    """cleanup attempts ordinary removal of only the admitted fixture and reports last-root limits."""
    receipt = {"profile": PROFILE, "removed": False, "full_trust_settings_restoration_proven": False}
    try:
        if not (directory / "baseline.json").is_file():
            receipt["result"] = "no fully admitted fixture; no cleanup mutation attempted"
            return
        baseline = verify(directory, evidence, "before-fixture-cleanup")
        run(["sudo", "-n", "/usr/bin/security", "remove-trusted-cert", "-d", str(directory / "foreign-root.pem")])
        run(["sudo", "-n", "/usr/bin/security", "delete-certificate", "-Z", baseline["certificate"]["sha1"], KEYCHAIN])
        if trust_entry(baseline["certificate"], required=False) is not None:
            raise RuntimeError("fixture administrator trust remains after removal")
        try:
            run(["/usr/bin/security", "find-certificate", "-c", baseline["common_name"], "-p", KEYCHAIN])
        except subprocess.CalledProcessError as error:
            if error.returncode != 44 or error.stdout:
                raise
        else:
            raise RuntimeError("fixture keychain certificate remains after removal")
        receipt["removed"] = True
        receipt["result"] = "ordinary fixture trust and certificate removal commands succeeded"
    except (RuntimeError, subprocess.SubprocessError, OSError, ValueError) as error:
        receipt["result"] = "fixture cleanup not proven; disposable worker retains baseline if removal did not complete"
        receipt["error"] = str(error)
    finally:
        (evidence / "foreign-trust-fixture-cleanup.json").write_text(json.dumps(receipt, indent=2) + "\n")


def main():
    """main keeps fixture paths fixed inside the selected disposable runner's temporary directory."""
    directory = Path(os.environ["RUNNER_TEMP"]) / "harbor-ui-foreign-trust"
    evidence = Path("harbor-package-ui-evidence")
    evidence.mkdir(exist_ok=True)
    mode = sys.argv[1]
    if mode == "seed":
        seed(directory, evidence)
    elif mode == "cleanup":
        cleanup(directory, evidence)
    elif mode in ("after-release", "after-uninstall"):
        verify(directory, evidence, mode)
    else:
        raise ValueError("unknown foreign trust fixture mode")


if __name__ == "__main__":
    main()
