"""Check creation-failure diagnostics without launching a native installation."""

import ast
from pathlib import Path
from types import SimpleNamespace
import unittest
from unittest.mock import Mock


def load_function(name, **namespace):
    source = ast.parse(Path(__file__).with_name('acceptance.py').read_text())
    function = next(node for node in source.body if isinstance(node, ast.FunctionDef) and node.name == name)
    exec(compile(ast.Module(body=[function], type_ignores=[]), 'acceptance.py', 'exec'), namespace)
    return namespace[name]


class CreationDiagnosticsTests(unittest.TestCase):
    def test_webkit_text_can_be_unnamed_or_exposed_as_an_accessible_name(self):
        for name, text, expected in (('', 'Required tools are ready', True),
                                     ('Required tools are ready', '', True),
                                     ('', 'Checking local tools', False)):
            with self.subTest(name=name, text=text):
                node = SimpleNamespace(name=name)
                visible = load_function('visible_text', application=lambda: object(), nodes=lambda _: [node],
                                        accessible_text=lambda _: text, GLib=SimpleNamespace(GError=RuntimeError))
                self.assertEqual(visible('Required tools are ready'), expected)

    def test_webkit_text_waits_when_the_application_or_tree_is_unavailable(self):
        visible = load_function('visible_text', application=lambda: None)
        self.assertFalse(visible('4 of 4 steps complete'))
        def retired_tree(_):
            raise RuntimeError('retired accessibility tree')
        visible = load_function('visible_text', application=lambda: object(), nodes=retired_tree,
                                GLib=SimpleNamespace(GError=RuntimeError))
        self.assertFalse(visible('4 of 4 steps complete'))

    def test_reported_creation_failure_does_not_wait_for_registration_timeout(self):
        for label in ('Review choices', 'Retry registration'):
            with self.subTest(label=label):
                created = load_function('created_project', project=lambda: None,
                                        find=lambda name: name == label)
                with self.assertRaisesRegex(RuntimeError, 'graphical project creation failed'):
                    created()

    def test_registered_project_wins_over_stale_failure_controls(self):
        row = {'id': 'created'}
        find = Mock(side_effect=AssertionError('should not inspect stale controls'))
        self.assertIs(load_function('created_project', project=lambda: row, find=find)(), row)

    def test_pending_creation_remains_pending(self):
        self.assertIsNone(load_function('created_project', project=lambda: None, find=lambda _: None)())

    def test_text_interface_retains_failure_tail_and_tolerates_unavailable_nodes(self):
        class AccessibilityError(Exception):
            pass
        text = load_function('accessible_text', GLib=SimpleNamespace(GError=AccessibilityError))
        node = Mock()
        node.queryText.return_value.getText.return_value = 'go mod tidy failed: concrete error'
        self.assertEqual(text(node), 'go mod tidy failed: concrete error')
        for error in (NotImplementedError(), AccessibilityError()):
            node.queryText.side_effect = error
            self.assertEqual(text(node), '')


if __name__ == '__main__':
    unittest.main()
