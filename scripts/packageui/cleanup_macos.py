"""Record ownership-aware recovery when an installed macOS GUI assertion fails."""

from contextlib import closing
import ctypes
import hashlib
import json
import os
from pathlib import Path
import re
import sqlite3
import stat
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


def bounded_text(value):
    """bounded_text retains partial subprocess bytes without unbounded diagnostic artifacts."""
    if isinstance(value, bytes):
        value = value.decode("utf-8", errors="replace")
    return (value or "")[:65536]


def daemon_process_facts(pid):
    """daemon_process_facts reads native birth, owner, and executable without inspecting process arguments."""
    # PROC_PIDTBSDINFO follows sys/proc_info.h; retain microseconds to detect same-second PID reuse.
    class BSDInfo(ctypes.Structure):
        _fields_ = [("words", ctypes.c_uint32 * 12), ("comm", ctypes.c_char * 16),
                    ("name", ctypes.c_char * 32), ("status", ctypes.c_uint32 * 6),
                    ("seconds", ctypes.c_uint64), ("microseconds", ctypes.c_uint64)]

    library = ctypes.CDLL("/usr/lib/libproc.dylib", use_errno=True)
    library.proc_pidinfo.argtypes = [ctypes.c_int, ctypes.c_int, ctypes.c_uint64, ctypes.c_void_p, ctypes.c_int]
    library.proc_pidinfo.restype = ctypes.c_int
    library.proc_pidpath.argtypes = [ctypes.c_int, ctypes.c_void_p, ctypes.c_uint32]
    library.proc_pidpath.restype = ctypes.c_int
    info = BSDInfo()
    if library.proc_pidinfo(pid, 3, 0, ctypes.byref(info), ctypes.sizeof(info)) != ctypes.sizeof(info):
        raise RuntimeError("native daemon birth unavailable")
    executable = ctypes.create_string_buffer(4096)
    if library.proc_pidpath(pid, executable, len(executable)) <= 0:
        raise RuntimeError("native daemon executable unavailable")
    if info.words[3] != pid or info.seconds == 0:
        raise RuntimeError("native daemon identity unavailable")
    return {"pid": pid, "uid": info.words[5], "birth_seconds": info.seconds,
            "birth_microseconds": info.microseconds, "executable": os.fsdecode(executable.value)}


def collect_release_diagnostics(directory):
    """collect_release_diagnostics reads only the fixed release checkpoint and admitted installed daemon."""
    diagnostics = {"commands": []}

    def observe(arguments, timeout=10, retain_stdout=True):
        try:
            result = subprocess.run(arguments, capture_output=True, text=True, timeout=timeout)
            diagnostics["commands"].append({"arguments": arguments, "exit_code": result.returncode,
                                            "stdout": bounded_text(result.stdout) if retain_stdout else "omitted: native service output may contain environment values",
                                            "stderr": bounded_text(result.stderr)})
            result.check_returncode()
            return result.stdout
        except subprocess.TimeoutExpired as error:
            diagnostics["commands"].append({"arguments": arguments, "timed_out": True,
                                            "stdout": bounded_text(error.output) if retain_stdout else "omitted: native service output may contain environment values",
                                            "stderr": bounded_text(error.stderr)})
            raise

    try:
        database = Path.home() / "Library/Application Support/GoForj/Harbor/harbor.db"
        # Never copy authority payloads, helper tickets, or arbitrary database rows into evidence.
        with closing(sqlite3.connect(database.as_uri() + "?mode=ro", uri=True, timeout=2)) as connection:
            connection.execute("PRAGMA query_only=ON")
            connection.row_factory = sqlite3.Row
            diagnostics["operation"] = [dict(row) for row in connection.execute(
                "SELECT id, intent_id, kind, state, phase, revision FROM operations WHERE intent_id = ?",
                ("intent-network-release",))]
            diagnostics["checkpoint"] = [dict(row) for row in connection.execute(
                "SELECT operation_id, phase, checkpoint_revision, network_revision FROM network_global_release_plans WHERE id = 1")]
    except Exception as error:
        diagnostics["checkpoint_error"] = str(error)
    try:
        observe([CLI, "daemon", "snapshot"])
    except Exception as error:
        diagnostics["snapshot_error"] = str(error)
    try:
        service = f"gui/{os.getuid()}/com.goforj.harbor.daemon"
        definition = observe(["/bin/launchctl", "print", service], retain_stdout=False)
        if not re.search(r"^\s*program = /Library/Application Support/GoForj/Harbor/daemon-launcher$", definition, re.M):
            raise RuntimeError("preserve daemon diagnostic privacy: service program differs")
        matches = re.findall(r"^\s*pid = ([0-9]+)$", definition, re.M)
        if len(matches) != 1:
            raise RuntimeError("daemon service has no unique running process")
        pid = int(matches[0])
        expected = Path("/Library/Application Support/GoForj/Harbor/current/bin/harbord").resolve(strict=True)
        metadata = expected.stat()
        if not stat.S_ISREG(metadata.st_mode) or metadata.st_uid != 0 or metadata.st_mode & 0o022:
            raise RuntimeError("installed daemon executable lacks package ownership")
        before = daemon_process_facts(pid)
        if before["uid"] != os.getuid() or before["executable"] != str(expected):
            raise RuntimeError("preserve daemon diagnostic privacy: native process does not match installed daemon")
        diagnostics["daemon"] = before
        # Installed LaunchAgent has no stderr file. Record that absence rather than reading guessed files.
        diagnostics["daemon_stderr"] = "installed LaunchAgent does not configure StandardErrorPath"
        if daemon_process_facts(pid) != before:
            raise RuntimeError("daemon changed before native sampling")
        sample = directory / "release-daemon.sample.txt"
        try:
            observe(["/usr/bin/sample", str(pid), "1", "1", "-file", str(sample)], timeout=5)
            if daemon_process_facts(pid) != before:
                raise RuntimeError("daemon changed during native sampling; sample discarded")
        except Exception:
            sample.unlink(missing_ok=True)
            raise
        diagnostics["sample"] = sample.name
    except Exception as error:
        diagnostics["daemon_error"] = str(error)
    (directory / "release-timeout-diagnostics.json").write_text(json.dumps(diagnostics, indent=2) + "\n")


def run_recorded(arguments, directory, receipt):
    """run_recorded preserves bounded command evidence before propagating a release timeout."""
    try:
        result = subprocess.run(arguments, text=True, capture_output=True, timeout=180)
    except subprocess.TimeoutExpired as error:
        receipt["commands"].append({"arguments": arguments, "timed_out": True, "timeout_seconds": 180,
                                    "stdout": bounded_text(error.output), "stderr": bounded_text(error.stderr)})
        if arguments == [CLI, "daemon", "release"]:
            try:
                collect_release_diagnostics(directory)
            except Exception as diagnostic_error:
                receipt["release_diagnostics_error"] = str(diagnostic_error)
        raise
    receipt["commands"].append({"arguments": arguments, "exit_code": result.returncode,
                                "stdout": result.stdout, "stderr": result.stderr})
    result.check_returncode()
    return result


def release_network(directory):
    """release_network applies the recovery command bound and diagnostics to normal GUI completion too."""
    directory.mkdir(parents=True, exist_ok=True)
    receipt = {"completed": False, "commands": []}
    try:
        run_recorded([CLI, "daemon", "release"], directory, receipt)
        receipt["completed"] = True
    except Exception as error:
        receipt["error"] = str(error)
        raise
    finally:
        (directory / "network-release-command.json").write_text(json.dumps(receipt, indent=2) + "\n")


def recover(directory):
    """recover uses production commands and retains partial failure evidence instead of forcing host changes."""
    directory.mkdir(parents=True, exist_ok=True)
    receipt = {"completed": False, "commands": [], "retained": {}}

    def run(arguments):
        return run_recorded(arguments, directory, receipt)

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
    elif sys.argv[1:] == ["release"]:
        release_network(Path("harbor-package-ui-evidence"))
    elif sys.argv[1:] == ["absence"]:
        verify_service_absence()
    else:
        raise ValueError("expected release, absence, or no arguments")
