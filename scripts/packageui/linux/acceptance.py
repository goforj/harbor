"""Drive the installed GTK/WebKit UI through AT-SPI and native keyboard input."""

import json
import os
from pathlib import Path
import subprocess
import tempfile
import time

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


def state(expected):
    def check():
        row = project()
        if row and row['state'] in ['failed', 'quarantined', 'unavailable']:
            raise RuntimeError('project failed: ' + json.dumps(row))
        return row and row['state'] == expected
    wait('project ' + expected, check, 180)


def https(slug):
    assert command('/usr/bin/curl', '--silent', '--show-error', '--connect-timeout', '5', '--max-time', '15', '--output', '/dev/null', '--write-out', '%{http_code}', 'https://' + slug + '.test') == '200'


def capture(name):
    command('scrot', str(EVIDENCE / (name + '.png')))
    deadline = time.monotonic() + 5
    error = 'installed accessibility application is temporarily unavailable'
    while time.monotonic() < deadline:
        app = application()
        if app is not None:
            try:
                rows = [node.getRoleName() + ': ' + node.name + ' states=' + str(node.getState().getStates()) for node in nodes(app)]
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
    process = subprocess.Popen([APP], stdout=LOG, stderr=subprocess.STDOUT)
    children.append(process)
    def available():
        code = process.poll()
        if code is not None:
            (EVIDENCE / 'launch-exit.json').write_text(json.dumps({'pid': process.pid, 'returncode': code, 'application': APP}) + '\n')
            raise RuntimeError('installed desktop exited before accessibility registration: ' + str(code))
        return application()
    wait('installed UI accessibility', available, 60)
    assert Path('/proc/' + str(process.pid) + '/exe').resolve() == Path(APP)
    assert Path('/proc/' + str(process.pid)).owner() == command('id', '-un')
    wait('window visible', lambda: window(True), 60)


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


def activate():
    # The visible WebKit client can precede XFCE's window-manager registration.
    wait('window manager', lambda: 'window id #' in command('xprop', '-root', '_NET_SUPPORTING_WM_CHECK'))
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
try:
    launch()
    capture('installed-first-launch')
    # Overview can expose New project before its networking setup card finishes rendering.
    wait('first-run UI', lambda: find('Set up secure networking') or find('New project'), 60)
    if snapshot().get('network_setup', {}).get('configured') is not True:
        click('Set up secure networking')
    wait('configured networking', lambda: snapshot().get('network_setup', {}).get('configured'), 90)
    click('New project')
    fill('Project name', 'Harbor Installed Linux UI')
    fill('Go module', 'example.com/harbor-installed-linux-ui')
    fill('Project directory', str(directory))
    click('Continue')
    for label in ['CLI', 'Docker', 'Mail', 'Auth', 'OAuth', 'Web API', 'Web UI', 'Metrics', 'Observability', 'Grafana', 'Database (MySQL)', 'Database (Postgres)', 'Database (SQLite)', 'Scheduler', 'Cache', 'Events', 'File Storage', 'Background Jobs']:
        checkbox(label, label in ['CLI', 'Web API', 'Web UI'])
    select('Starter kit', 'Vue')
    click('Continue')
    select('Atlas agent support', 'Skip')
    click('Continue')
    capture('creation-review')
    click('Create project')
    row = wait('created registration', project, 300)
    assert row['state'] == 'stopped'
    click('Start project')
    state('ready')
    https(row['slug'])
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
    if not find('Stop project'):
        click('Projects')
        app = application()
        link = wait('project link', lambda: next((node for node in nodes(application()) if node.getRoleName() == 'link' and 'Harbor Installed Linux UI' in node.name), None))
        assert link.queryAction().doAction(0)
    click('Stop project')
    state('stopped')
    quit_ui()
    state('stopped')
    (EVIDENCE / 'installed-ui.json').write_text(json.dumps({
        'application': APP, 'project_directory': str(directory), 'project_id': row['id'],
        'fixture': 'Ubuntu 24.04 x86_64, XFCE X11 desktop on Xvfb, AT-SPI, GTK/WebKit',
        'creation': 'production graphical wizard', 'start': 'ready and system-trusted HTTPS 200',
        'close': 'hidden with project ready', 'reopen': 'original process restored',
        'quit': 'native GTK menu, same daemon PID and project ready',
        'quit_method': 'native GTK menu',
        'ctrl_q': 'unsupported by pinned Wails GTK3 menu accelerator implementation',
        'relaunch': 'new installed process, registration retained and HTTPS 200',
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
