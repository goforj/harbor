"""Retain a live launch transcript before a failed startup retires its temporary diagnostics."""

from pathlib import Path
from datetime import datetime, timezone
import subprocess
import sys
import time


def retain_process_stages(temporary, evidence):
    """retain_process_stages observes arguments only for descendants of a validated disposable project."""
    observed = subprocess.run(["/bin/ps", "-axo", "pid,ppid,state,etime,time,%cpu,nice,comm"], text=True, capture_output=True, timeout=10)
    rows = []
    admitted = set()
    for line in observed.stdout.splitlines()[1:]:
        fields = line.split(None, 7)
        if len(fields) != 8 or not fields[0].isdigit() or not fields[1].isdigit():
            continue
        pid, parent = int(fields[0]), int(fields[1])
        rows.append((pid, parent))
        if Path(fields[7]).name != "forj":
            continue
        cwd = subprocess.run(["/usr/sbin/lsof", "-a", "-p", str(pid), "-d", "cwd", "-Fn"], text=True, capture_output=True, timeout=10)
        for name in cwd.stdout.splitlines():
            if not name.startswith("n"):
                continue
            project = Path(name[1:])
            if project.name == "project" and project.parent.parent == temporary and project.parent.match("harbor-package-ui-????????-????-????-????-????????????") and not project.parent.is_symlink() and not project.is_symlink() and project.is_dir() and project.resolve().parent.parent == temporary.resolve():
                admitted.add(pid)
    while True:
        descendants = {pid for pid, parent in rows if parent in admitted}
        if descendants.issubset(admitted):
            break
        admitted.update(descendants)
    evidence.mkdir(parents=True, exist_ok=True)
    with (evidence / "fixture-process-stages.txt").open("a") as output:
        output.write("\nobserved=" + datetime.now(timezone.utc).isoformat() + "\n")
        output.write(observed.stdout + observed.stderr)
        if admitted:
            stages = subprocess.run(["/bin/ps", "-p", ",".join(str(pid) for pid in sorted(admitted)), "-o", "pid,ppid,state,etime,time,%cpu,nice,args"], text=True, capture_output=True, timeout=10)
            output.write("\nvalidated fixture descendants:\n" + stages.stdout + stages.stderr)


def retain_traces(temporary, evidence, npm_logs=None):
    """retain_traces copies only direct diagnostic files inside the disposable GUI scenario."""
    retained = False
    for root in temporary.glob("harbor-package-ui-????????-????-????-????-????????????"):
        if root.is_symlink() or not root.is_dir():
            continue
        trace = root / "project/_data/harbor/forj-dev.log"
        if trace.is_symlink() or not trace.is_file() or not trace.resolve().is_relative_to(root.resolve()):
            continue
        try:
            data = trace.read_bytes()
        except FileNotFoundError:
            continue
        evidence.mkdir(parents=True, exist_ok=True)
        retained = True
        (evidence / (root.name + "-live-launch.log")).write_bytes(data)
        for name in (".goforj.yml", "go.mod", "cmd/app/frontend/package.json"):
            source = root / "project" / name
            if source.is_file() and not source.is_symlink() and source.resolve().is_relative_to(root.resolve()):
                (evidence / (root.name + "-" + name.replace("/", "-").lstrip("."))).write_bytes(source.read_bytes())
        if npm_logs is not None:
            for logfile in npm_logs.glob("*-debug-*.log"):
                if logfile.is_symlink() or not logfile.is_file():
                    continue
                with logfile.open("rb") as source:
                    data = source.read(4 << 20)
                if str(root).encode() in data:
                    (evidence / (root.name + "-npm-" + logfile.name)).write_bytes(data)
    return retained


def main():
    """main preserves the native driver's exit status while observing its temporary launch output."""
    temporary = Path("/private/tmp")
    evidence = Path("harbor-package-ui-evidence")
    npm_logs = Path.home() / ".npm/_logs"
    observed_at = 0.0
    with subprocess.Popen(sys.argv[1:]) as process:
        while process.poll() is None:
            retained = retain_traces(temporary, evidence, npm_logs)
            if retained and time.monotonic() - observed_at >= 15:
                retain_process_stages(temporary, evidence)
                observed_at = time.monotonic()
            time.sleep(0.2)
        retain_traces(temporary, evidence, npm_logs)
        return process.wait()


if __name__ == "__main__":
    sys.exit(main())
