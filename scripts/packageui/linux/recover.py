"""Recover only the disposable installed fixture after retaining GUI failure diagnostics."""

import hashlib
import json
from pathlib import Path
import subprocess
import time

import remove_fixtures


def run(*arguments):
    return subprocess.check_output(arguments, text=True, timeout=90).strip()


def snapshot():
    return json.loads(run('/usr/bin/harbor', 'daemon', 'snapshot'))


def main():
    evidence = Path('harbor-package-ui-evidence')
    report = {'original_gui_result': 'failed', 'cleanup': 'incomplete'}
    try:
        deadline = time.monotonic() + 30
        while True:
            try:
                data = snapshot()
                break
            except subprocess.SubprocessError:
                if time.monotonic() >= deadline:
                    raise
                time.sleep(.2)
        files = [Path.home() / '.local/share/goforj/harbor/harbor.db']
        for project in data.get('projects', []):
            root = Path(project['path']).resolve()
            if not root.is_relative_to(Path('/tmp').resolve()) or not root.parent.name.startswith('harbor-package-ui-'):
                raise RuntimeError('refusing cleanup of non-fixture project')
            for name in ['.goforj.yml', 'go.mod']:
                path = root / name
                if path.is_file() and not path.is_symlink():
                    files.append(path)
            if project['state'] != 'stopped':
                run('/usr/bin/harbor', 'stop', project['id'])
                deadline = time.monotonic() + 120
                while True:
                    current = next(row for row in snapshot().get('projects', []) if row['id'] == project['id'])
                    if current['state'] == 'stopped':
                        break
                    if time.monotonic() >= deadline:
                        raise RuntimeError('fixture project did not stop before release: ' + project['id'])
                    time.sleep(.25)
        remove_fixtures.remove_projects(snapshot().get('projects', []), evidence / 'failure-fixture-removal.json')
        if data.get('network_setup', {}).get('configured'):
            (evidence / 'failure-before-release-snapshot.json').write_text(json.dumps(snapshot(), indent=2) + '\n')
            try:
                run('/usr/bin/harbor', 'daemon', 'release')
            except subprocess.SubprocessError:
                (evidence / 'failure-after-release-snapshot.json').write_text(json.dumps(snapshot(), indent=2) + '\n')
                raise
            report['network_release'] = 'completed'
        else:
            # The package prerm independently refuses removal while any owned
            # host integration remains, including a partially completed setup.
            report['network_release'] = 'not configured; native package removal must admit absence'
        run('/usr/bin/harbor', 'daemon', 'stop')
        deadline = time.monotonic() + 30
        while run('systemctl', '--user', 'show', 'harbord.service', '--property=MainPID', '--value') != '0':
            if time.monotonic() >= deadline:
                raise RuntimeError('daemon did not stop')
            time.sleep(.2)
        hashes = {str(path): hashlib.sha256(path.read_bytes()).hexdigest() for path in files}
        run('sudo', '-n', 'dpkg', '--purge', 'goforj-harbor')
        for path in ['/usr/lib/goforj-harbor/harbor-desktop', '/usr/lib/systemd/user/harbord.service', '/usr/libexec/harbor-helper', '/var/lib/goforj/harbor', '/etc/systemd/resolved.conf.d/90-goforj-harbor.conf', '/usr/local/share/ca-certificates/goforj-harbor.crt']:
            if Path(path).exists() or Path(path).is_symlink():
                raise RuntimeError('retained installed state: ' + path)
        run('systemctl', '--user', 'daemon-reload')
        assert run('systemctl', '--user', 'show', 'harbord.service', '--property=LoadState', '--value') == 'not-found'
        assert run('systemctl', '--user', 'show', 'harbord.service', '--property=MainPID', '--value') == '0'
        assert run('systemctl', '--user', 'show', 'harbord.service', '--property=ActiveState', '--value') == 'inactive'
        for name, digest in hashes.items():
            assert hashlib.sha256(Path(name).read_bytes()).hexdigest() == digest, name
        report.update(cleanup='released and uninstalled', preservation_boundary='post-unregister database and project files', retained_files=hashes)
    finally:
        (evidence / 'failure-cleanup.json').write_text(json.dumps(report, indent=2) + '\n')


if __name__ == '__main__':
    main()
