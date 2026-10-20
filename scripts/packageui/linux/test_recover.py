"""Exercise failure recovery ordering and refusal boundaries without a local install."""

import contextlib
import json
import os
from pathlib import Path
import tempfile
import unittest
from unittest import mock

import recover


class RecoveryTests(unittest.TestCase):
    def exercise(self, states, *, foreign=False, active=False, expired=False, configured=True):
        with tempfile.TemporaryDirectory(prefix='harbor-package-ui-', dir='/tmp') as temporary:
            root = Path(temporary).resolve()
            project = root / 'project'
            project.mkdir()
            for name in ['.goforj.yml', 'go.mod']:
                (project / name).write_text('retained fixture\n')
            home = root / 'home'
            database = home / '.local/share/goforj/harbor/harbor.db'
            database.parent.mkdir(parents=True)
            database.write_bytes(b'retained database')
            (root / 'harbor-package-ui-evidence').mkdir()
            events = []
            observed = iter(states)

            def snapshot():
                state = next(observed)
                events.append('snapshot:' + state)
                return {'network_setup': {'configured': configured}, 'projects': [{'id': 'fixture', 'path': '/foreign/project' if foreign else str(project), 'state': state}]}

            def run(*args):
                events.append(' '.join(args))
                if '--property=MainPID' in args:
                    return '0'
                if '--property=LoadState' in args:
                    return 'not-found'
                if '--property=ActiveState' in args:
                    return 'active' if active else 'inactive'
                return ''

            previous = Path.cwd()
            os.chdir(root)
            error = None
            try:
                with contextlib.ExitStack() as stack:
                    stack.enter_context(mock.patch.object(recover, 'run', side_effect=run))
                    stack.enter_context(mock.patch.object(recover, 'snapshot', side_effect=snapshot))
                    stack.enter_context(mock.patch.object(Path, 'home', return_value=home))
                    stack.enter_context(mock.patch.object(recover.time, 'sleep'))
                    if expired:
                        stack.enter_context(mock.patch.object(recover.time, 'monotonic', side_effect=[0, 0, 121]))
                    try:
                        recover.main()
                    except (RuntimeError, AssertionError) as caught:
                        error = caught
                report = json.loads((root / 'harbor-package-ui-evidence/failure-cleanup.json').read_text())
            finally:
                os.chdir(previous)
            return events, report, error

    def test_waits_for_asynchronous_stop_before_release(self):
        events, report, error = self.exercise(['starting', 'stopping', 'stopped'])
        self.assertIsNone(error)
        self.assertLess(events.index('snapshot:stopped'), events.index('/usr/bin/harbor daemon release'))
        self.assertEqual(report['cleanup'], 'released and uninstalled')
        self.assertEqual(len(report['retained_files']), 3)

    def test_stop_timeout_prevents_global_release_and_purge(self):
        events, report, error = self.exercise(['starting', 'stopping'], expired=True)
        self.assertIn('did not stop', str(error))
        self.assertNotIn('/usr/bin/harbor daemon release', events)
        self.assertNotIn('sudo -n dpkg --purge goforj-harbor', events)
        self.assertEqual(report['cleanup'], 'incomplete')

    def test_foreign_registration_is_never_stopped(self):
        events, report, error = self.exercise(['ready'], foreign=True)
        self.assertIn('non-fixture', str(error))
        self.assertEqual(events, ['snapshot:ready'])
        self.assertEqual(report['cleanup'], 'incomplete')

    def test_unconfigured_daemon_uses_native_removal_absence_guard(self):
        events, report, error = self.exercise(['stopped'], configured=False)
        self.assertIsNone(error)
        self.assertNotIn('/usr/bin/harbor daemon release', events)
        self.assertIn('sudo -n dpkg --purge goforj-harbor', events)
        self.assertIn('native package removal', report['network_release'])

    def test_deleted_but_active_service_does_not_pass_cleanup(self):
        _, report, error = self.exercise(['stopped'], active=True)
        self.assertIsInstance(error, AssertionError)
        self.assertEqual(report['cleanup'], 'incomplete')


if __name__ == '__main__':
    unittest.main()
