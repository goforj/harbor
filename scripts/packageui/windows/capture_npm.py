"""Retain only public timing facts from npm logs bound to the exact GUI fixture."""
import argparse
from datetime import datetime, timezone
import json
import os
from pathlib import Path, PureWindowsPath
import re
import stat
from urllib.parse import urlsplit

LIMIT = 65536
LOG_NAME = re.compile(r"^(\d{4}-\d{2}-\d{2}T\d{2}_\d{2}_\d{2}_\d{3}Z)-debug-\d+\.log$")


def plain_path(path):
    """plain_path rejects links and Windows reparse ancestors before diagnostic reads."""
    for item in (path, *path.parents):
        info = item.lstat()
        if stat.S_ISLNK(info.st_mode) or getattr(info, "st_file_attributes", 0) & 0x400:
            return False
    return True


def bounded_read(path):
    """bounded_read checks the opened identity and reads at most a head and tail window."""
    before = path.stat()
    with path.open("rb") as source:
        opened = os.fstat(source.fileno())
        if (before.st_dev, before.st_ino) != (opened.st_dev, opened.st_ino):
            return b""
        data = source.read(LIMIT)
        if opened.st_size > LIMIT:
            source.seek(max(LIMIT, opened.st_size - LIMIT))
            data += b"\n" + source.read(LIMIT)
    return data


def timing_lines(text):
    """timing_lines excludes argv, configuration, credentials and full request URLs."""
    result = []
    for line in text.splitlines():
        if re.fullmatch(r"\d+ timing [A-Za-z0-9:._/@ -]{1,160} Completed in \d+ms", line):
            result.append(line)
        elif re.fullmatch(r"\d+ (?:info using (?:npm|node)@[A-Za-z0-9.+-]+|verbose (?:exit|code) -?\d+|error code [A-Z0-9_]+)", line):
            result.append(line)
        else:
            match = re.fullmatch(r"\d+ http fetch (GET|POST) (\d{3}) (\S+) (\d+)ms(?: \(cache (miss|hit|revalidated)\))?", line)
            if match and urlsplit(match[3]).hostname == "registry.npmjs.org":
                result.append(f"http fetch {match[1]} {match[2]} registry.npmjs.org {match[4]}ms cache={match[5] or 'unspecified'}")
    return result


def capture(fixture, logs, evidence, after):
    """capture admits at most three recent exact-cwd logs and writes bounded sanitized facts."""
    frontend = fixture / "cmd/app/frontend"
    if not frontend.is_dir() or not logs.is_dir() or not plain_path(frontend) or not plain_path(logs):
        return 0
    expected = PureWindowsPath(str(frontend)).as_posix().casefold()
    evidence.mkdir(parents=True, exist_ok=True)
    retained = {p.name for p in evidence.glob("npm-debug-*.json")}
    count = 0
    candidates = sorted(logs.glob("*-debug-*.log"), key=lambda p: p.name, reverse=True)[:12]
    for logfile in candidates:
        match = LOG_NAME.fullmatch(logfile.name)
        if not match or not logfile.is_file() or not plain_path(logfile):
            continue
        started = datetime.strptime(match[1], "%Y-%m-%dT%H_%M_%S_%fZ").replace(tzinfo=timezone.utc)
        if started < after or logfile.stat().st_mtime < after.timestamp():
            continue
        text = bounded_read(logfile).decode("utf-8", errors="replace")
        cwd = re.findall(r"^\d+ verbose cwd (.+)$", text, re.MULTILINE)
        if len(cwd) != 1 or PureWindowsPath(cwd[0].strip()).as_posix().casefold() != expected:
            continue
        name = "npm-debug-" + logfile.stem + ".json"
        if name not in retained and len(retained) >= 3:
            continue
        lines = timing_lines(text)
        result = {"source_filename": logfile.name, "exact_frontend_cwd_verified": True,
                  "after_start": after.isoformat(), "public_timing_lines": lines}
        timing = logs / (match[1] + "-timing.json")
        if timing.is_file() and plain_path(timing) and timing.stat().st_size <= LIMIT and timing.stat().st_mtime >= after.timestamp():
            try:
                document = json.loads(bounded_read(timing))
                result["timers_ms"] = {key: value for key, value in document.get("timers", {}).items()
                                       if re.fullmatch(r"[A-Za-z0-9:._/@ -]{1,160}", key)
                                       and type(value) in (int, float) and 0 <= value <= 86400000}
            except (ValueError, TypeError, AttributeError):
                pass
        data = json.dumps(result, indent=2).encode()
        if len(data) > LIMIT:
            result.pop("timers_ms", None)
            result["public_timing_lines"] = lines[-100:]
            data = json.dumps(result, indent=2).encode()
        if len(data) <= LIMIT:
            (evidence / name).write_bytes(data)
            retained.add(name)
            count += 1
    return count


def main():
    """main accepts only read-only fixture and default npm-log locations supplied by the harness."""
    parser = argparse.ArgumentParser()
    parser.add_argument("--fixture", type=Path, required=True)
    parser.add_argument("--logs", type=Path, required=True)
    parser.add_argument("--evidence", type=Path, required=True)
    parser.add_argument("--after", required=True)
    args = parser.parse_args()
    after = datetime.fromisoformat(args.after.replace("Z", "+00:00"))
    if after.tzinfo is None:
        parser.error("startup time must include UTC offset")
    capture(args.fixture, args.logs, args.evidence, after)


if __name__ == "__main__":
    main()
