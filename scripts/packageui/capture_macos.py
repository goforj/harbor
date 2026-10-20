"""Retain a live launch transcript before a failed startup retires its temporary diagnostics."""

from pathlib import Path
import subprocess
import sys
import time


def retain_traces(temporary, evidence):
    """retain_traces copies only direct diagnostic files inside the disposable GUI scenario."""
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
        (evidence / (root.name + "-live-launch.log")).write_bytes(data)
        for name in (".goforj.yml", "go.mod", "app/frontend/package.json"):
            source = root / "project" / name
            if source.is_file() and not source.is_symlink() and source.resolve().is_relative_to(root.resolve()):
                (evidence / (root.name + "-" + name.replace("/", "-").lstrip("."))).write_bytes(source.read_bytes())


def main():
    """main preserves the native driver's exit status while observing its temporary launch output."""
    temporary = Path("/private/tmp")
    evidence = Path("harbor-package-ui-evidence")
    with subprocess.Popen(sys.argv[1:]) as process:
        while process.poll() is None:
            retain_traces(temporary, evidence)
            time.sleep(0.2)
        retain_traces(temporary, evidence)
        return process.wait()


if __name__ == "__main__":
    sys.exit(main())
