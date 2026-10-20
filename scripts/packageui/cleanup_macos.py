"""Record ownership-aware recovery when an installed macOS GUI assertion fails."""

import hashlib
import json
import os
from pathlib import Path
import re
import subprocess
import sys
import time


CLI = "/usr/local/bin/harbor"
INSTALLER = "/Library/Application Support/GoForj/Harbor/current/bin/harbor-installer"


def fixture_root(project):
    """fixture_root refuses to stop a registration outside the disposable GUI scenario."""
    root = Path(project["path"]).resolve()
    if not re.fullmatch(r"/private/tmp/harbor-package-ui-[0-9A-F-]{36}/project", str(root)):
        raise ValueError("refusing recovery outside the disposable GUI fixture")
    return root


def wait_for_daemon_exit(run):
    """wait_for_daemon_exit lets SQLite checkpoint before retained files are hashed."""
    service = f"gui/{os.getuid()}/com.goforj.harbor.daemon"
    deadline = time.monotonic() + 30
    while time.monotonic() < deadline:
        state = run(["launchctl", "print", service]).stdout
        if not re.search(r"^\s*pid = [0-9]+$", state, re.M):
            return
        time.sleep(0.1)
    raise RuntimeError("installed daemon did not exit during failure recovery")


def verify_service_absence():
    """verify_service_absence distinguishes a missing exact job from an unavailable bootstrap domain."""
    domain = f"gui/{os.getuid()}"
    reachable = subprocess.run(["launchctl", "print", domain], capture_output=True, text=True, timeout=30)
    reachable.check_returncode()
    service = "com.goforj.harbor.daemon"
    absent = subprocess.run(["launchctl", "print", f"{domain}/{service}"], capture_output=True, text=True, timeout=30)
    expected = f'Bad request.\nCould not find service "{service}" in domain for user gui: {os.getuid()}\n'
    if absent.returncode != 113 or absent.stdout or absent.stderr != expected:
        raise RuntimeError("installed user service absence could not be verified: " + absent.stderr)
    return {"domain_exit_code": reachable.returncode, "service_exit_code": absent.returncode, "stderr": absent.stderr}


def unregister_fixture(project, run):
    """unregister_fixture retains one CLI intent and approves only its exact disposable removal."""
    root = fixture_root(project)
    identifier = project["id"]
    intent = "intent-macos-ui-remove-" + identifier
    deadline = time.monotonic() + 120
    while True:
        result = json.loads(run([CLI, "remove", identifier, "--intent", intent, "--json"]).stdout)
        operation = result["operation"]
        if operation["state"] == "succeeded":
            return
        if operation["state"] in ("failed", "cancelled"):
            raise RuntimeError("fixture unregister ended " + operation["state"])
        if time.monotonic() >= deadline:
            raise RuntimeError("fixture unregister exceeded its cleanup deadline")
        if operation["state"] == "requires_approval":
            run([os.environ["HARBOR_PACKAGE_UI_APPROVAL_TOOL"],
                 "--project-id", identifier, "--operation-id", operation["id"],
                 "--intent-id", intent, "--revision", str(result["revision"]),
                 "--project-directory", str(root)])
        time.sleep(0.25)


def recover(directory):
    """recover uses production commands and retains partial failure evidence instead of forcing host changes."""
    directory.mkdir(parents=True, exist_ok=True)
    receipt = {"completed": False, "commands": [], "retained": {}}

    def run(arguments):
        result = subprocess.run(arguments, text=True, capture_output=True, timeout=180)
        receipt["commands"].append({"arguments": arguments, "exit_code": result.returncode,
                                    "stdout": result.stdout, "stderr": result.stderr})
        result.check_returncode()
        return result

    try:
        run(["/usr/bin/swift", "desktop/packageui/macos/QuitInstalledHarbor.swift"])
        database = Path.home() / "Library/Application Support/GoForj/Harbor/harbor.db"
        agent = Path.home() / "Library/LaunchAgents/com.goforj.harbor.daemon.plist"
        retained = []
        if database.exists() or agent.exists():
            snapshot = json.loads(run([CLI, "daemon", "snapshot"]).stdout)
            projects = snapshot.get("projects", [])
            roots = [fixture_root(project) for project in projects]
            for project in projects:
                # Failed startup has already retired its session, so Stop rejects it.
                # Unregister retains the ordinary runtime and network admission checks.
                if project.get("state") not in ("stopped", "failed"):
                    run([CLI, "stop", project["id"]])
            deadline = time.monotonic() + 120
            while True:
                snapshot = json.loads(run([CLI, "daemon", "snapshot"]).stdout)
                if all(project["state"] in ("stopped", "failed") for project in snapshot.get("projects", [])):
                    break
                if time.monotonic() >= deadline:
                    raise RuntimeError("fixture projects did not stop during failure recovery")
                time.sleep(0.25)
            for project in projects:
                unregister_fixture(project, run)
            deadline = time.monotonic() + 120
            while projects:
                snapshot = json.loads(run([CLI, "daemon", "snapshot"]).stdout)
                if not snapshot.get("projects", []):
                    break
                if time.monotonic() >= deadline:
                    raise RuntimeError("fixture registrations did not retire before network release")
                time.sleep(0.25)
            receipt["fixture_unregistered"] = [project["id"] for project in projects]
            if snapshot.get("network_setup", {}).get("configured") is False and not snapshot.get("projects", []):
                receipt["network_release"] = "not configured; no registered projects"
            else:
                run([CLI, "daemon", "release"])
            run([CLI, "daemon", "stop"])
            wait_for_daemon_exit(run)
            retained.append(database)
            for root in roots:
                retained.extend(root / name for name in (".goforj.yml", "go.mod"))
        for path in retained:
            receipt["retained"][str(path)] = hashlib.sha256(path.read_bytes()).hexdigest()
        run(["sudo", "-n", INSTALLER, "uninstall", "--user-id", str(os.getuid()), "--group-id", str(os.getgid())])
        if Path("/Applications/Harbor.app").exists() or agent.exists():
            raise RuntimeError("installed product or user service definition survived failure recovery")
        receipt["service_absence"] = verify_service_absence()
        for name, digest in receipt["retained"].items():
            if hashlib.sha256(Path(name).read_bytes()).hexdigest() != digest:
                raise RuntimeError("failure recovery changed a retained user file: " + name)
        receipt["completed"] = True
    except Exception as error:
        receipt["error"] = str(error)
        raise
    finally:
        (directory / "failure-cleanup.json").write_text(json.dumps(receipt, indent=2) + "\n")


if __name__ == "__main__":
    if not sys.argv[1:]:
        recover(Path("harbor-package-ui-evidence"))
    elif sys.argv[1:] == ["absence"]:
        verify_service_absence()
    else:
        raise ValueError("expected absence or no arguments")
