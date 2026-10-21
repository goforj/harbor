"""Drive the installed GTK/WebKit UI through AT-SPI and native keyboard input."""

import json
import os
from pathlib import Path
import re
import shutil
import subprocess
import tempfile
import time
from urllib.parse import urlsplit

import pyatspi
from gi.repository import GLib


APP = '/usr/lib/goforj-harbor/harbor-desktop'
EVIDENCE = Path('harbor-package-ui-evidence')
EVIDENCE.mkdir(exist_ok=True)
LOG = (EVIDENCE / 'desktop.log').open('w')
process = None
children = []


def command(*arguments):
    return subprocess.check_output(arguments, text=True, timeout=30).strip()


def wait(description, condition, timeout=30):
    deadline = time.monotonic() + timeout
    while time.monotonic() < deadline:
        value = condition()
        if value:
            return value
        time.sleep(.25)
    raise RuntimeError('timed out: ' + description)


def nodes(root):
    yield root
    for child in root:
        if child is not None:
            yield from nodes(child)


def application():
    try:
        for app in pyatspi.Registry.getDesktop(0):
            if app.get_process_id() == process.pid:
                return app
    except GLib.GError:
        # A WebKit process can retire while AT-SPI enumerates its application.
        # Callers retain their bounded wait and obtain a fresh tree next time.
        return None
    return None


def find(label, roles=None):
    app = application()
    if app is None:
        return None
    try:
        for node in nodes(app):
            if node.name == label and (roles is None or node.getRoleName() in roles):
                return node
    except GLib.GError:
        return None
    return None


def element(label, roles=None):
    return wait('accessible element ' + label, lambda: find(label, roles))


def key(*keys):
    command('xdotool', 'key', '--clearmodifiers', *keys)


def click(label, roles=None):
    node = element(label, roles)
    wait('enabled ' + label, lambda: node.getState().contains(pyatspi.STATE_ENABLED))
    action = node.queryAction()
    if not action.doAction(0):
        raise RuntimeError('accessible action rejected: ' + label)
    time.sleep(.3)


def focus(node):
    activate()
    if not node.queryComponent().grabFocus():
        raise RuntimeError('could not focus ' + node.name)
    wait('focused ' + node.name, lambda: node.getState().contains(pyatspi.STATE_FOCUSED))


def fill(label, value):
    node = element(label, ['entry', 'text', 'password text'])
    focus(node)
    key('ctrl+a')
    command('xdotool', 'type', '--clearmodifiers', '--delay', '1', value)
    wait('field value ' + label, lambda: node.queryText().getText(0, -1) == value)


def native_folder_dialog():
    """native_folder_dialog selects only the installed process's titled native chooser."""
    return find('Choose a location for the new project', ['file chooser', 'dialog', 'frame'])


def dialog_button(dialog, label):
    """dialog_button keeps native dialog actions out of the underlying web form."""
    matches = [node for node in nodes(dialog) if node.getRoleName() == 'push button'
               and node.name.replace('_', '') == label]
    if len(matches) != 1 or not matches[0].getState().contains(pyatspi.STATE_ENABLED):
        raise RuntimeError('native folder dialog requires exactly one enabled ' + label)
    button = matches[0]
    if not button.queryAction().doAction(0):
        raise RuntimeError('native folder dialog rejected ' + label)


def choose_project_location(parent, expected):
    """choose_project_location verifies cancellation and selection through the actual GTK chooser."""
    previous = str(parent / 'unchanged-before-picker')
    fill('Project directory', previous)
    click('Choose project location')
    dialog = wait('native folder chooser', native_folder_dialog)
    dialog_button(dialog, 'Cancel')
    wait('native picker cancellation preserves the directory', lambda: native_folder_dialog() is None
         and element('Project directory', ['entry']).queryText().getText(0, -1) == previous)
    click('Choose project location')
    dialog = wait('native folder chooser', native_folder_dialog)
    if command('xdotool', 'getwindowpid', command('xdotool', 'getactivewindow')) != str(process.pid):
        raise RuntimeError('native chooser is not the foreground installed process')
    key('ctrl+l')
    wait('native folder location field', lambda: next((node for node in nodes(dialog)
        if node.getRoleName() == 'entry' and node.getState().contains(pyatspi.STATE_FOCUSED)), None))
    key('ctrl+a')
    command('xdotool', 'type', '--clearmodifiers', '--delay', '1', str(parent) + '/')
    key('Return')
    # GTK can accept the location directly or navigate there before its Open response.
    try:
        wait('native chooser accepted the typed directory', lambda: native_folder_dialog() is None, 3)
    except RuntimeError:
        dialog_button(wait('native folder chooser', native_folder_dialog), 'Open')
    wait('selected project directory returned', lambda: native_folder_dialog() is None
         and element('Project directory', ['entry']).queryText().getText(0, -1) == str(expected))
    capture('native-directory-picker-selected')


def assert_project_url_clipboard():
    """assert_project_url_clipboard checks the system clipboard without recording unexpected text."""
    selected = project()
    urls = [row['url'] for row in selected.get('resources', []) if row['kind'] == 'application']
    if len(urls) != 1 or selected['state'] != 'ready':
        raise RuntimeError('copy requires one ready published application URL')
    subprocess.run(['xclip', '-selection', 'clipboard', '-in'], input='Harbor clipboard probe cleared',
                   text=True, check=True, timeout=5)
    click('Copy project URL')
    wait('native clipboard receives the published URL', lambda:
         command('xclip', '-selection', 'clipboard', '-out', '-target', 'UTF8_STRING') == urls[0])
    element('Project URL copied', ['push button'])
    (EVIDENCE / 'project-url-clipboard.json').write_text(json.dumps({
        'project_id': selected['id'], 'expected_url': urls[0],
        'native_clipboard_exact_match': True, 'copied_feedback_visible': True,
    }, indent=2) + '\n')


def checkbox(label, selected):
    node = element(label, ['check box'])
    if node.getState().contains(pyatspi.STATE_CHECKED) != selected:
        click(label, ['check box'])
    wait('checkbox ' + label, lambda: node.getState().contains(pyatspi.STATE_CHECKED) == selected)


def select(label, option):
    node = element(label, ['combo box'])
    focus(node)
    key('alt+Down')
    key('Home')
    command('xdotool', 'type', '--clearmodifiers', option)
    key('Return')
    # WebKit exposes option state through its menu children; the combo itself
    # intentionally has no AT-SPI Selection interface.
    wait('selected option ' + option, lambda: any(
        child.name == option and child.getState().contains(pyatspi.STATE_SELECTED)
        for child in nodes(node)))


def snapshot():
    return json.loads(command('/usr/bin/harbor', 'daemon', 'snapshot'))


def project():
    return next((row for row in snapshot().get('projects', []) if Path(row['path']).resolve() == directory), None)


def created_project():
    row = project()
    if row is None and (find('Review choices') or find('Retry registration')):
        raise RuntimeError('graphical project creation failed; see retained creation output')
    return row


def state(expected):
    def check():
        row = project()
        if row and row['state'] in ['failed', 'quarantined', 'unavailable']:
            raise RuntimeError('project failed: ' + json.dumps(row))
        return row and row['state'] == expected
    wait('project ' + expected, check, 360 if expected == 'ready' else 180)


def https(slug):
    assert command('/usr/bin/curl', '--silent', '--show-error', '--connect-timeout', '5', '--max-time', '15', '--output', '/dev/null', '--write-out', '%{http_code}', 'https://' + slug + '.test') == '200'


def capture(name):
    (EVIDENCE / (name + '-children.json')).write_text(json.dumps([
        {'pid': child.pid, 'returncode': child.poll()} for child in children
    ], indent=2) + '\n')
    command('scrot', str(EVIDENCE / (name + '.png')))
    deadline = time.monotonic() + 5
    error = 'installed accessibility application is temporarily unavailable'
    while time.monotonic() < deadline:
        app = application()
        if app is not None:
            try:
                rows = []
                for node in nodes(app):
                    rows.append(node.getRoleName() + ': ' + node.name + ' states=' + str(node.getState().getStates()))
                    text = accessible_text(node)
                    if text:
                        rows.append('  text: ' + repr(text[:4096]))
                    if node.name == 'Creation output':
                        output = accessible_text(node)
                        if not output:
                            # WebKit can expose the preformatted text on child
                            # nodes instead of the labelled section itself.
                            output = '\n'.join(accessible_text(child) for child in nodes(node))
                        (EVIDENCE / (name + '-creation-output.txt')).write_text(output[-262144:])
                (EVIDENCE / (name + '-accessibility.txt')).write_text('\n'.join(rows))
                return
            except GLib.GError as caught:
                error = str(caught)
        time.sleep(.1)
    # Tree capture is diagnostic. Actual controls still must satisfy their own
    # native accessibility checks and hard deadlines after a document reload.
    (EVIDENCE / (name + '-accessibility-error.txt')).write_text(error + '\n')


def launch():
    global process
    wait('window manager before product launch', window_manager_ready)
    process = subprocess.Popen([APP], stdout=LOG, stderr=subprocess.STDOUT)
    children.append(process)
    def require_running(stage):
        code = process.poll()
        if code is not None:
            (EVIDENCE / 'launch-exit.json').write_text(json.dumps({'pid': process.pid, 'returncode': code, 'application': APP, 'stage': stage}) + '\n')
            raise RuntimeError('installed desktop exited during ' + stage + ': ' + str(code))
    def available():
        require_running('accessibility registration')
        return application()
    wait('installed UI accessibility', available, 60)
    assert Path('/proc/' + str(process.pid) + '/exe').resolve() == Path(APP)
    assert Path('/proc/' + str(process.pid)).owner() == command('id', '-un')
    def visible():
        require_running('initial window presentation')
        return window(True)
    wait('window visible', visible, 60)


def accessible_text(node, depth=0):
    """accessible_text expands WebKit embedded-object characters using their native hypertext anchors."""
    try:
        text = node.queryText().getText(0, -1)
    except (NotImplementedError, GLib.GError):
        return ''
    if '\ufffc' not in text or depth >= 4:
        return text
    try:
        hypertext = node.queryHypertext()
        parts = []
        for offset, character in enumerate(text):
            if character == '\ufffc':
                index = hypertext.getLinkIndex(offset)
                if index >= 0:
                    link = hypertext.getLink(index)
                    if link is not None:
                        child = link.get_object(0)
                        if child is not None:
                            character = accessible_text(child, depth + 1) or child.name
            parts.append(character)
        return ''.join(parts)
    except (NotImplementedError, GLib.GError):
        return text


def visible_text(label):
    """visible_text handles WebKit static text exposed through Text instead of an accessible name."""
    app = application()
    if app is None:
        return False
    try:
        expected = ''.join(label.split())
        return any(node.name == label or expected in ''.join(accessible_text(node).split()) for node in nodes(app))
    except GLib.GError:
        return False


def selected_document_text(label):
    """selected_document_text observes rendered WebKit text that GTK omits from the accessibility tree."""
    activate()
    # Retire the browser's clipboard value before copying the actual Harbor
    # document. An unchanged or unavailable selection cannot satisfy this check.
    subprocess.run(['xclip', '-selection', 'clipboard', '-in'], input='Harbor UI probe cleared',
                   text=True, check=True, timeout=5)
    key('ctrl+a')
    key('ctrl+c')
    time.sleep(.1)
    try:
        text = command('xclip', '-selection', 'clipboard', '-out', '-target', 'UTF8_STRING')
    except subprocess.CalledProcessError:
        return False
    return ''.join(label.split()) in ''.join(text.split())


def configure_browser_preference(home):
    """configure_browser_preference gives the disposable XFCE desktop a real browser and retains its prior setting."""
    path = home / '.config/xfce4/helpers.rc'
    if path.is_symlink() or (path.exists() and not path.is_file()):
        raise RuntimeError('browser preference must be a direct regular file')
    original = path.read_bytes() if path.exists() else None
    text = original.decode() if original is not None else ''
    updated, count = re.subn(r'(?m)^[ \t]*WebBrowser=.*$', 'WebBrowser=epiphany', text)
    if not count:
        updated = text.rstrip('\n') + ('\n' if text else '') + 'WebBrowser=epiphany\n'
    expected = updated.encode()
    path.parent.mkdir(parents=True, exist_ok=True)
    path.write_bytes(expected)
    return path, original, expected


def restore_browser_preference(receipt):
    """restore_browser_preference preserves another writer's changes instead of overwriting an unexpected setting."""
    path, original, expected = receipt
    if path.is_symlink() or path.read_bytes() != expected:
        raise RuntimeError('browser preference changed outside the fixture')
    if original is None:
        path.unlink()
    else:
        path.write_bytes(original)


def record_browser_probe(observation):
    """record_browser_probe retains bounded progress metadata without collecting unexpected clipboard text."""
    (EVIDENCE / 'browser-probe.json').write_text(json.dumps(observation, indent=2) + '\n')


def opened_browser(url):
    """opened_browser reads the real address through X11 input because GTK4 may omit it from AT-SPI."""
    expected_executable = Path(shutil.which('epiphany')).resolve()
    host = urlsplit(url).netloc
    observation = {'expected_executable': str(expected_executable), 'expected_address': url, 'stage': 'finding_process'}
    try:
        for pid in command('pgrep', '-x', 'epiphany').split():
            try:
                observation.update(pid=pid, stage='checking_executable')
                if not pid.isdecimal() or Path(f'/proc/{pid}/exe').resolve() != expected_executable:
                    continue
                if os.stat(f'/proc/{pid}').st_uid != os.getuid():
                    continue
                observation['stage'] = 'finding_window'
                clients = re.findall(r'0x[0-9a-fA-F]+', command('xprop', '-root', '_NET_CLIENT_LIST'))
                for identifier in clients:
                    try:
                        if command('xdotool', 'getwindowpid', identifier).strip() != pid:
                            continue
                    except subprocess.SubprocessError:
                        continue
                    geometry = dict(line.split('=', 1) for line in command('xdotool', 'getwindowgeometry', '--shell', identifier).splitlines() if '=' in line)
                    if int(geometry.get('WIDTH', '0')) < 420 or int(geometry.get('HEIGHT', '0')) < 520:
                        continue
                    observation.update(window_id=identifier, stage='copying_address')
                    command('xdotool', 'windowactivate', '--sync', identifier)
                    active = command('xdotool', 'getactivewindow').strip()
                    observation['active_window'] = active
                    if int(active, 0) != int(identifier, 0):
                        continue
                    key('ctrl+l')
                    time.sleep(.1)
                    key('ctrl+c')
                    observation['clipboard_target'] = 'UTF8_STRING'
                    try:
                        address = command('xclip', '-selection', 'clipboard', '-out', '-target', 'UTF8_STRING')
                    except subprocess.CalledProcessError:
                        observation['clipboard_target'] = 'text/plain;charset=utf-8'
                        address = command('xclip', '-selection', 'clipboard', '-out', '-target', 'text/plain;charset=utf-8')
                    address = address.strip().rstrip('/')
                    observation['stage'] = 'checking_address'
                    if address in (url.rstrip('/'), host):
                        key('Escape')
                        observation['stage'] = 'verified'
                        return {'pid': int(pid), 'executable': str(expected_executable), 'window_id': identifier, 'address': address}
            except (OSError, subprocess.SubprocessError, ValueError) as error:
                observation['error_type'] = type(error).__name__
                continue
    except (OSError, subprocess.SubprocessError) as error:
        observation['error_type'] = type(error).__name__
        return None
    finally:
        record_browser_probe(observation)
    return None


def window(visible):
    result = subprocess.run(['xdotool', 'search', '--onlyvisible', '--pid', str(process.pid), '--name', 'GoForj Harbor'], capture_output=True, text=True)
    clients = []
    for identifier in result.stdout.split():
        geometry = dict(line.split('=', 1) for line in command('xdotool', 'getwindowgeometry', '--shell', identifier).splitlines())
        # The tray's XEmbed window shares Harbor's title and PID, but cannot be
        # mistaken for the product's minimum 420 by 520 client window.
        if int(geometry['WIDTH']) >= 420 and int(geometry['HEIGHT']) >= 520:
            clients.append(identifier)
    return '\n'.join(clients) if visible else not clients


def window_manager_ready():
    # The visible WebKit client can precede XFCE's window-manager registration.
    return 'window id #' in command('xprop', '-root', '_NET_SUPPORTING_WM_CHECK')


def activate():
    wait('window manager', window_manager_ready)
    identifier = wait('visible window', lambda: window(True))
    command('xdotool', 'windowactivate', '--sync', identifier.splitlines()[0])
    wait('active installed window', lambda: command('xdotool', 'getactivewindow') == identifier.splitlines()[0])


def quit_ui():
    activate()
    click('Harbor', ['menu'])
    click('Quit Harbor UI', ['menu item'])
    wait('native UI quit', lambda: process.poll() is not None)
    assert process.returncode == 0


root = Path(tempfile.mkdtemp(prefix='harbor-package-ui-', dir='/tmp'))
directory = root / 'project'
browser_preference = None
try:
    assert shutil.which('epiphany'), 'the disposable desktop requires its installed browser'
    browser_preference = configure_browser_preference(Path.home())
    launch()
    element('Dismiss getting started guide', ['push button'])
    capture('installed-first-launch')
    # Overview can expose New project before its networking setup card finishes rendering.
    wait('first-run UI', lambda: find('Set up secure networking') or find('New project'), 60)
    if snapshot().get('network_setup', {}).get('configured') is not True:
        click('Set up secure networking')
    wait('configured networking', lambda: snapshot().get('network_setup', {}).get('configured'), 90)
    click('New project')
    fill('Project name', 'Project')
    choose_project_location(root, directory)
    fill('Project name', 'Harbor Installed Linux UI')
    fill('Go module', 'example.com/harbor-installed-linux-ui')
    click('Continue')
    for label in ['CLI', 'Docker', 'Mail', 'Auth', 'OAuth', 'Web API', 'Web UI', 'Metrics', 'Observability', 'Grafana', 'Database (MySQL)', 'Database (Postgres)', 'Database (SQLite)', 'Scheduler', 'Cache', 'Events', 'File Storage', 'Background Jobs']:
        checkbox(label, label in ['CLI', 'Web API', 'Web UI'])
    select('Starter kit', 'Vue')
    click('Continue')
    select('Atlas agent support', 'Skip')
    click('Continue')
    capture('creation-review')
    click('Create project')
    row = wait('created registration', created_project, 300)
    assert row['state'] == 'stopped'
    element('Dismiss getting started guide', ['push button'])
    click('Start project')
    state('ready')
    https(row['slug'])
    click('Recheck')
    wait('native prerequisites are ready', lambda: visible_text('Required tools are ready'))
    click('Open app')
    browser = wait('real browser opened the project address', lambda: opened_browser('https://' + row['slug'] + '.test'), 60)
    (EVIDENCE / 'browser-open.json').write_text(json.dumps(browser, indent=2) + '\n')
    activate()
    wait('guide App opening completed', lambda: selected_document_text('4 of 4 steps complete'))
    capture('guide-complete')
    click('Finish setup')
    wait('guide dismissed after completion', lambda: not find('Dismiss getting started guide'))
    capture('project-ready')
    daemon_pid = command('systemctl', '--user', 'show', 'harbord.service', '--property=MainPID', '--value')
    assert int(daemon_pid) > 1
    original_pid = process.pid
    activate()
    key('alt+F4')
    wait('window hidden', lambda: window(False))
    assert process.poll() is None
    state('ready')
    second = subprocess.Popen([APP], stdout=LOG, stderr=subprocess.STDOUT)
    children.append(second)
    second.wait(timeout=30)
    assert second.returncode == 0
    wait('original window reopened', lambda: window(True))
    assert process.pid == original_pid and process.poll() is None
    state('ready')
    quit_ui()
    state('ready')
    assert command('systemctl', '--user', 'show', 'harbord.service', '--property=MainPID', '--value') == daemon_pid
    launch()
    assert process.pid != original_pid
    state('ready')
    https(row['slug'])
    capture('reopened-running-project')
    # The driver uses authenticated control requests and native service identity.
    # Keep this original graphical process alive across both daemon crashes.
    recovery_tool = Path(os.environ['HARBOR_PACKAGE_UI_CRASH_RECOVERY_TOOL'])
    assert recovery_tool.is_absolute() and recovery_tool.is_file()
    subprocess.run([
        str(recovery_tool), '--project-id', row['id'],
        '--project-directory', str(directory.resolve()),
        '--evidence', str((EVIDENCE / 'installed-crash-recovery.json').resolve()),
    ], check=True, timeout=900, stdout=LOG, stderr=subprocess.STDOUT)
    assert process.poll() is None
    state('ready')
    https(row['slug'])
    if not find('Stop project'):
        click('Projects')
        app = application()
        link = wait('project link', lambda: next((node for node in nodes(application()) if node.getRoleName() == 'link' and 'Harbor Installed Linux UI' in node.name), None))
        assert link.queryAction().doAction(0)
    # This product control requires a connected daemon and a fresh snapshot.
    wait('graphical client reconnected with live project controls', lambda:
         find('Stop project') and find('Stop project').getState().contains(pyatspi.STATE_ENABLED), 60)
    capture('daemon-crash-recovered-live-ui')
    assert_project_url_clipboard()
    click('Stop project')
    state('stopped')
    quit_ui()
    state('stopped')
    (EVIDENCE / 'installed-ui.json').write_text(json.dumps({
        'application': APP, 'project_directory': str(directory), 'project_id': row['id'],
        'fixture': 'Ubuntu 24.04 x86_64, XFCE X11 desktop on Xvfb, AT-SPI, GTK/WebKit',
        'creation': 'production graphical wizard', 'start': 'ready and system-trusted HTTPS 200',
        'directory_picker': 'native GTK cancellation preserves choices; selected parent produces the registered destination',
        'clipboard': 'system clipboard exactly matches the daemon-published application URL',
        'onboarding': 'installed first-project guide, native tool recheck, and published App opening completed',
        'browser': browser,
        'close': 'hidden with project ready', 'reopen': 'original process restored',
        'quit': 'native GTK menu, same daemon PID and project ready',
        'quit_method': 'native GTK menu',
        'ctrl_q': 'unsupported by pinned Wails GTK3 menu accelerator implementation',
        'relaunch': 'new installed process, registration retained and HTTPS 200',
        'daemon_crash_recovery': 'two main-process crashes, live GUI reconnect, HTTPS 200, and enabled Stop',
        'stop': 'stopped registration retained',
    }, indent=2) + '\n')
except BaseException:
    capture('failure')
    try:
        (EVIDENCE / 'failure-snapshot.json').write_text(json.dumps(snapshot(), indent=2))
        trace = directory / '_data/harbor/forj-dev.log'
        if trace.is_file() and not trace.is_symlink():
            (EVIDENCE / 'forj-dev.log').write_bytes(trace.read_bytes())
    except (subprocess.SubprocessError, OSError):
        pass
    raise
finally:
    # Retire every direct, unreaped UI child before stopping the daemon: even a
    # failed single-instance launch can leave a reconnecting desktop client.
    retired = []
    for child in children:
        if child.poll() is not None:
            continue
        result = {'pid': child.pid, 'method': 'native GTK menu'}
        try:
            if child is not process:
                raise RuntimeError('single-instance child did not finish handoff')
            activate()
            click('Harbor', ['menu'])
            click('Quit Harbor UI', ['menu item'])
            child.wait(timeout=15)
        except (subprocess.SubprocessError, RuntimeError):
            # Popen retains this exact direct child until wait reaps it. Failed
            # native quit remains a failed GUI check when recovery retires it.
            result['method'] = 'fixture direct-child termination after failed native quit'
            child.terminate()
            try:
                child.wait(timeout=15)
            except subprocess.TimeoutExpired:
                child.kill()
                child.wait(timeout=15)
        result['returncode'] = child.returncode
        retired.append(result)
    if retired:
        (EVIDENCE / 'failure-ui-retirement.json').write_text(json.dumps(retired, indent=2) + '\n')
    (EVIDENCE / 'ui-child-settlement.json').write_text(json.dumps([
        {'pid': child.pid, 'returncode': child.poll()} for child in children
    ], indent=2) + '\n')
    if browser_preference is not None:
        restore_browser_preference(browser_preference)
