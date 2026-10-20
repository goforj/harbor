"""Explicitly unregister disposable projects before the production global release boundary."""

import hashlib
import json
import os
from pathlib import Path
import subprocess
import time


def run(*arguments):
    return subprocess.check_output(arguments, text=True, timeout=120).strip()


def snapshot():
    return json.loads(run('/usr/bin/harbor', 'daemon', 'snapshot'))


def remove_projects(projects, receipt):
    roots = []
    for project in projects:
        root = Path(project['path']).resolve()
        if root.name != 'project' or root.parent.parent != Path('/tmp').resolve() or not root.parent.name.startswith('harbor-package-ui-') or root.parent.name == 'harbor-package-ui-':
            raise RuntimeError('refusing removal of non-fixture project')
        if project['state'] != 'stopped':
            raise RuntimeError('fixture must be stopped before explicit unregister')
        roots.append(root)
    report = {'boundary': 'explicit fixture unregister after GUI registration retention; uninstall preservation is post-unregister', 'projects': []}
    try:
        for project, root in zip(projects, roots):
            files = [root / name for name in ['.goforj.yml', 'go.mod'] if (root / name).is_file() and not (root / name).is_symlink()]
            hashes = {str(path): hashlib.sha256(path.read_bytes()).hexdigest() for path in files}
            intent = 'intent-linux-ui-remove-' + project['id']
            operation_id = None
            deadline = time.monotonic() + 120
            entry = {'project_id': project['id'], 'project_directory': str(root), 'intent_id': intent, 'state': 'incomplete'}
            report['projects'].append(entry)
            while True:
                result = json.loads(run('/usr/bin/harbor', 'remove', project['id'], '--intent', intent, '--json'))
                operation = result['operation']
                if (operation.get('kind') != 'project.unregister' or operation.get('project_id') != project['id'] or operation.get('intent_id') != intent
                        or not isinstance(result.get('revision'), int) or result['revision'] <= 0):
                    raise RuntimeError('CLI removal crossed its selected fixture or intent')
                if operation_id is None:
                    operation_id = operation['id']
                if operation['id'] != operation_id:
                    raise RuntimeError('CLI removal changed its operation identity')
                entry['operation_id'] = operation_id
                if operation['state'] in ['failed', 'cancelled']:
                    raise RuntimeError('fixture removal ended ' + operation['state'])
                if operation['state'] == 'requires_approval':
                    tool = Path(os.environ['HARBOR_PACKAGE_UI_APPROVAL_TOOL'])
                    if not tool.is_absolute() or tool.is_symlink() or not tool.is_file() or not os.access(tool, os.X_OK):
                        raise RuntimeError('fixture approval tool is not an admitted executable file')
                    run(str(tool), '--project-id', project['id'], '--operation-id', operation_id, '--intent-id', intent,
                        '--revision', str(result['revision']), '--project-directory', str(root))
                elif operation['state'] == 'succeeded':
                    break
                if time.monotonic() >= deadline:
                    raise RuntimeError('fixture removal did not reach terminal success')
                time.sleep(.2)
            while any(row['id'] == project['id'] for row in snapshot().get('projects', [])):
                if time.monotonic() >= deadline:
                    raise RuntimeError('fixture registration remains after successful removal')
                time.sleep(.2)
            for name, digest in hashes.items():
                if hashlib.sha256(Path(name).read_bytes()).hexdigest() != digest:
                    raise RuntimeError('explicit unregister changed project file ' + name)
            entry.update(state='unregistered', retained_project_files=hashes)
    finally:
        receipt.write_text(json.dumps(report, indent=2) + '\n')


if __name__ == '__main__':
    remove_projects(snapshot().get('projects', []), Path('harbor-package-ui-evidence/fixture-removal.json'))
