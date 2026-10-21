"""Check creation-failure diagnostics without launching a native installation."""

import ast
from pathlib import Path
import re
import subprocess
import tempfile
from types import SimpleNamespace
import unittest
from unittest.mock import Mock, call
from urllib.parse import urlsplit


def load_function(name, **namespace):
    source = ast.parse(Path(__file__).with_name('acceptance.py').read_text())
    function = next(node for node in source.body if isinstance(node, ast.FunctionDef) and node.name == name)
    exec(compile(ast.Module(body=[function], type_ignores=[]), 'acceptance.py', 'exec'), namespace)
    return namespace[name]


def browser_reader(*, executable='/usr/bin/epiphany', address='https://project.test/', uid=1001, geometry='WIDTH=1024\nHEIGHT=768', active_window='7'):
    """browser_reader supplies native observations while retaining the real window and address admission logic."""
    paths = {'epiphany': '/usr/bin/epiphany', '/proc/123/exe': executable}
    responses = {
        ('pgrep', '-x', 'epiphany'): '123',
        ('xprop', '-root', '_NET_CLIENT_LIST'): '_NET_CLIENT_LIST(WINDOW): window id # 0x7',
        ('xdotool', 'getwindowpid', '0x7'): '123',
        ('xdotool', 'getwindowgeometry', '--shell', '0x7'): geometry,
        ('xdotool', 'windowactivate', '--sync', '0x7'): '',
        ('xdotool', 'getactivewindow'): active_window,
        ('xclip', '-selection', 'clipboard', '-out', '-target', 'UTF8_STRING'): address,
        ('xclip', '-selection', 'clipboard', '-out', '-target', 'text/plain;charset=utf-8'): address,
    }
    command = Mock(side_effect=lambda *args: responses[args])
    key = Mock()
    opened = load_function('opened_browser',
        Path=lambda path: SimpleNamespace(resolve=lambda: paths[path]),
        shutil=SimpleNamespace(which=lambda _: 'epiphany'), urlsplit=urlsplit,
        os=SimpleNamespace(stat=lambda _: SimpleNamespace(st_uid=uid), getuid=lambda: 1001),
        subprocess=subprocess, re=re, time=SimpleNamespace(sleep=lambda _: None), command=command, key=key,
        record_browser_probe=Mock())
    return opened, key, command


class CreationDiagnosticsTests(unittest.TestCase):
    def test_webkit_embedded_objects_preserve_inline_progress_text(self):
        child = SimpleNamespace(name='', queryText=lambda: SimpleNamespace(getText=lambda *_: '4'))
        parent = SimpleNamespace(
            queryText=lambda: SimpleNamespace(getText=lambda *_: '\ufffc of 4 steps complete'),
            queryHypertext=lambda: SimpleNamespace(getLinkIndex=lambda _: 0,
                getLink=lambda _: SimpleNamespace(get_object=lambda _: child)))
        namespace = {'GLib': SimpleNamespace(GError=RuntimeError)}
        read = load_function('accessible_text', **namespace)
        read.__globals__['accessible_text'] = read
        self.assertEqual(read(parent), '4 of 4 steps complete')

    def test_webkit_embedded_objects_remain_bounded_and_tolerate_retired_links(self):
        def unsupported():
            raise NotImplementedError()
        node = SimpleNamespace(queryText=lambda: SimpleNamespace(getText=lambda *_: '\ufffc of 4 steps complete'), queryHypertext=unsupported)
        read = load_function('accessible_text', GLib=SimpleNamespace(GError=RuntimeError))
        self.assertEqual(read(node), '\ufffc of 4 steps complete')
        self.assertEqual(read(node, 4), '\ufffc of 4 steps complete')
        node.queryHypertext = lambda: SimpleNamespace(getLinkIndex=lambda _: 0, getLink=lambda _: None)
        self.assertEqual(read(node), '\ufffc of 4 steps complete')
        node.queryHypertext = lambda: SimpleNamespace(getLinkIndex=lambda _: 0, getLink=lambda _: SimpleNamespace(get_object=lambda _: None))
        self.assertEqual(read(node), '\ufffc of 4 steps complete')

    def test_browser_preference_preserves_other_helpers_and_restores_prior_bytes(self):
        configure = load_function('configure_browser_preference', re=re)
        restore = load_function('restore_browser_preference')
        with tempfile.TemporaryDirectory() as directory:
            home = Path(directory)
            path = home / '.config/xfce4/helpers.rc'
            path.parent.mkdir(parents=True)
            original = b'# owned desktop preferences\nWebBrowser=previous\nMailReader=mail\n'
            path.write_bytes(original)
            receipt = configure(home)
            self.assertEqual(path.read_bytes(), original.replace(b'WebBrowser=previous', b'WebBrowser=epiphany'))
            restore(receipt)
            self.assertEqual(path.read_bytes(), original)

    def test_browser_preference_restores_absence_and_rejects_links_and_other_writers(self):
        configure = load_function('configure_browser_preference', re=re)
        restore = load_function('restore_browser_preference')
        with tempfile.TemporaryDirectory() as directory:
            home = Path(directory)
            receipt = configure(home)
            path = receipt[0]
            self.assertEqual(path.read_text(), 'WebBrowser=epiphany\n')
            restore(receipt)
            self.assertFalse(path.exists())
            target = home / 'unrelated'
            target.write_text('untouched')
            path.symlink_to(target)
            with self.assertRaisesRegex(RuntimeError, 'direct regular file'):
                configure(home)
            self.assertEqual(target.read_text(), 'untouched')
            path.unlink()
            path.mkdir()
            with self.assertRaisesRegex(RuntimeError, 'direct regular file'):
                configure(home)
            path.rmdir()
            receipt = configure(home)
            path.write_text('WebBrowser=other-writer\n')
            with self.assertRaisesRegex(RuntimeError, 'outside the fixture'):
                restore(receipt)
            self.assertEqual(path.read_text(), 'WebBrowser=other-writer\n')

    def test_browser_address_requires_the_selected_executable_and_project(self):
        for executable, address, expected in (('/usr/bin/epiphany', 'https://project.test/', True),
                                               ('/usr/bin/epiphany', 'project.test', True),
                                               ('/usr/bin/epiphany', 'other.test', False),
                                               ('/usr/bin/unrelated', 'project.test', False)):
            with self.subTest(executable=executable, address=address):
                opened, key, command = browser_reader(executable=executable, address=address)
                self.assertEqual(opened('https://project.test') is not None, expected)
                if expected:
                    self.assertEqual(key.call_args_list, [call('ctrl+l'), call('ctrl+c'), call('Escape')])
                    command.assert_any_call('xclip', '-selection', 'clipboard', '-out', '-target', 'UTF8_STRING')
                if executable != '/usr/bin/epiphany':
                    key.assert_not_called()

    def test_browser_address_rejects_foreign_users_and_popup_windows(self):
        for options in ({'uid': 2000}, {'geometry': 'WIDTH=300\nHEIGHT=200'}, {'geometry': 'WIDTH=invalid'}, {'active_window': '8'}, {'active_window': 'unavailable'}):
            with self.subTest(options=options):
                opened, key, _ = browser_reader(**options)
                self.assertIsNone(opened('https://project.test'))
                key.assert_not_called()

    def test_browser_address_accepts_the_standard_gtk_utf8_mime_target(self):
        opened, key, command = browser_reader()
        normal = command.side_effect
        def read(*args):
            if args == ('xclip', '-selection', 'clipboard', '-out', '-target', 'UTF8_STRING'):
                raise subprocess.CalledProcessError(1, args)
            return normal(*args)
        command.side_effect = read
        self.assertIsNotNone(opened('https://project.test'))
        command.assert_any_call('xclip', '-selection', 'clipboard', '-out', '-target', 'text/plain;charset=utf-8')

    def test_browser_address_ignores_unmanaged_or_foreign_client_windows(self):
        for missing in (False, True):
            with self.subTest(missing=missing):
                opened, key, command = browser_reader()
                normal = command.side_effect
                def read(*args):
                    if args == ('xprop', '-root', '_NET_CLIENT_LIST'):
                        return '_NET_CLIENT_LIST(WINDOW): window id # 0x6, 0x7'
                    if args == ('xdotool', 'getwindowpid', '0x6'):
                        if missing:
                            raise subprocess.CalledProcessError(1, args)
                        return '456'
                    return normal(*args)
                command.side_effect = read
                self.assertIsNotNone(opened('https://project.test'))
                self.assertEqual(key.call_args_list, [call('ctrl+l'), call('ctrl+c'), call('Escape')])

    def test_browser_address_waits_for_native_query_turnover(self):
        for failure in (OSError('retired process'), subprocess.CalledProcessError(1, ['pgrep'])):
            with self.subTest(failure=failure):
                opened, key, command = browser_reader()
                command.side_effect = failure
                self.assertIsNone(opened('https://project.test'))
                key.assert_not_called()

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

    def test_webkit_inline_progress_ignores_layout_whitespace_without_changing_the_words(self):
        for text, expected in [('4of 4 steps complete', True), ('4 \n of 4 steps complete', True), ('3 of 4 steps complete', False)]:
            with self.subTest(text=text):
                visible = load_function('visible_text', application=lambda: object(),
                    nodes=lambda _: [SimpleNamespace(name='')], accessible_text=lambda _: text,
                    GLib=SimpleNamespace(GError=RuntimeError))
                self.assertEqual(visible('4 of 4 steps complete'), expected)
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
