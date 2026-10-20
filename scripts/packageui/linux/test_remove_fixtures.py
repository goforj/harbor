"""Verify fixture unregister uses exact native approval and completes before global cleanup."""

import json
import os
from pathlib import Path
import subprocess
import tempfile
import unittest
from unittest import mock

import remove_fixtures


class FixtureRemovalTests(unittest.TestCase):
    def exercise(self, states, *, wrong_project=False, approval_failure=False, registration_remains=False, expired=False):
        with tempfile.TemporaryDirectory(prefix='harbor-package-ui-', dir='/tmp') as temporary:
            root = Path(temporary).resolve()
            checkout = root / 'project'
            checkout.mkdir()
            (checkout / 'go.mod').write_text('module example.com/fixture\n')
            tool = root / 'approval-tool'
            tool.write_text('fixture executable stub, intercepted by test')
            tool.chmod(0o700)
            project = {'id': 'project-fixture', 'path': str(checkout), 'state': 'stopped'}
            events = []
            choices = iter(states)

            def run(*arguments):
                events.append(arguments)
                if arguments[0] != '/usr/bin/harbor':
                    if approval_failure:
                        raise subprocess.CalledProcessError(1, arguments)
                    return 'approved'
                operation = {'id': 'operation-fixture', 'project_id': 'project-other' if wrong_project else project['id'],
                             'intent_id': 'intent-linux-ui-remove-project-fixture', 'kind': 'project.unregister',
                             'state': next(choices, states[-1])}
                return json.dumps({'operation': operation, 'revision': 5})

            receipt = root / 'receipt.json'
            error = None
            with mock.patch.object(remove_fixtures, 'run', side_effect=run), \
                    mock.patch.object(remove_fixtures, 'snapshot', return_value={'projects': [project] if registration_remains else []}), \
                    mock.patch.object(remove_fixtures.time, 'sleep'), \
                    mock.patch.dict(os.environ, {'HARBOR_PACKAGE_UI_APPROVAL_TOOL': str(tool)}):
                timing = mock.patch.object(remove_fixtures.time, 'monotonic', side_effect=[0, 121]) if expired else mock.patch.object(remove_fixtures.time, 'monotonic', return_value=0)
                with timing:
                    try:
                        remove_fixtures.remove_projects([project], receipt)
                    except (RuntimeError, subprocess.SubprocessError) as caught:
                        error = caught
            report = json.loads(receipt.read_text())
            return events, report, error

    def test_native_approval_replays_same_cli_intent_and_records_explicit_boundary(self):
        events, report, error = self.exercise(['requires_approval', 'succeeded'])
        self.assertIsNone(error)
        self.assertEqual([event[1] for event in events if event[0] == '/usr/bin/harbor'], ['remove', 'remove'])
        self.assertEqual(events[0], events[2])
        approval = events[1]
        self.assertEqual(approval[1:9], ('--project-id', 'project-fixture', '--operation-id', 'operation-fixture', '--intent-id', 'intent-linux-ui-remove-project-fixture', '--revision', '5'))
        self.assertEqual(report['projects'][0]['state'], 'unregistered')
        self.assertIn('post-unregister', report['boundary'])
        self.assertEqual(len(report['projects'][0]['retained_project_files']), 1)

    def test_wrong_project_never_receives_approval(self):
        events, report, error = self.exercise(['requires_approval'], wrong_project=True)
        self.assertIn('crossed', str(error))
        self.assertEqual(len(events), 1)
        self.assertEqual(report['projects'][0]['state'], 'incomplete')

    def test_declined_or_failed_native_approval_never_claims_removal(self):
        events, report, error = self.exercise(['requires_approval'], approval_failure=True)
        self.assertIsInstance(error, subprocess.CalledProcessError)
        self.assertEqual(len(events), 2)
        self.assertEqual(report['projects'][0]['state'], 'incomplete')

    def test_terminal_removal_failure_is_not_success(self):
        events, report, error = self.exercise(['failed'])
        self.assertIn('ended failed', str(error))
        self.assertEqual(len(events), 1)
        self.assertEqual(report['projects'][0]['state'], 'incomplete')

    def test_success_response_does_not_substitute_for_registry_absence(self):
        _, report, error = self.exercise(['succeeded'], registration_remains=True, expired=True)
        self.assertIn('registration remains', str(error))
        self.assertEqual(report['projects'][0]['state'], 'incomplete')

    def test_pending_removal_has_bounded_wait(self):
        _, report, error = self.exercise(['running'], expired=True)
        self.assertIn('terminal success', str(error))
        self.assertEqual(report['projects'][0]['state'], 'incomplete')

    def test_foreign_or_running_project_is_never_removed(self):
        for project in [{'id': 'foreign', 'path': '/foreign/project', 'state': 'stopped'},
                        {'id': 'running', 'path': '/tmp/harbor-package-ui-test/project', 'state': 'ready'}]:
            with self.subTest(project=project), mock.patch.object(remove_fixtures, 'run') as run:
                with self.assertRaises(RuntimeError):
                    remove_fixtures.remove_projects([project], Path('/unused-receipt'))
                run.assert_not_called()


if __name__ == '__main__':
    unittest.main()
