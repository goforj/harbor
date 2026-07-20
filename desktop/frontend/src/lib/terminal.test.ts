import { describe, expect, it } from 'vitest'
import { TerminalModel } from './terminal'

describe('TerminalModel', () => {
  it('updates carriage-return and backspace loaders in place', () => {
    const terminal = new TerminalModel()

    terminal.feed('Building 10%\rBuilding 20%\b\b5%\u001b[K')

    expect(terminal.text()).toBe('Building 25%')
  })

  it('redraws multiline loaders without replacing completed scrollback', () => {
    const terminal = new TerminalModel()
    terminal.feed('Compiled package\nDownload api: 10%\nDownload web: 20%\n')
    terminal.feed('\u001b[2A\r\u001b[2KDownload api: 90%\u001b[1B\r\u001b[2KDownload web: 80%')

    expect(terminal.text()).toBe('Compiled package\nDownload api: 90%\nDownload web: 80%\n')
  })

  it('supports cursor position, column, next-line, previous-line, and display erasure', () => {
    const terminal = new TerminalModel()
    terminal.feed('alpha\nbeta\ngamma')
    terminal.feed('\u001b[2;3H!\u001b[3G?\u001b[1Edelta\u001b[1F\u001b[2Knew')

    expect(terminal.text()).toBe('alpha\nnew\ndelta')

    terminal.feed('\u001b[2Jready')
    expect(terminal.text()).toBe('ready')
  })

  it('retains SGR state across chunks and returns safe text runs', () => {
    const terminal = new TerminalModel()
    terminal.feed('\u001b[31;1m<script')
    terminal.feed('>\u001b[0m ready')

    const [line] = terminal.renderLines()
    expect(line?.runs).toEqual([
      { text: '<script>', style: { color: '#cd3131', fontWeight: '700' } },
      { text: ' ready', style: {} },
    ])
    expect(terminal.text()).toBe('<script> ready')
  })

  it('renders palette colors, emphasis, inverse, and selective resets', () => {
    const terminal = new TerminalModel()
    terminal.feed('\u001b[1;4;44;38;5;214mwarning\u001b[22;24;49m \u001b[38;2;12;34;56;7minverse\u001b[0m')

    expect(terminal.renderLines()[0]?.runs).toEqual([
      {
        text: 'warning',
        style: {
          backgroundColor: '#2472c8',
          color: 'rgb(255 175 0)',
          fontWeight: '700',
          textDecorationLine: 'underline',
        },
      },
      { text: ' ', style: { color: 'rgb(255 175 0)' } },
      {
        text: 'inverse',
        style: { backgroundColor: 'rgb(12 34 56)', color: '#09090b' },
      },
    ])
  })

  it('retains split CSI, OSC, and surrogate pairs across chunks', () => {
    const terminal = new TerminalModel()
    terminal.feed('ready \u001b[38;2;')
    terminal.feed('12;34;56mco')
    terminal.feed('lor\u001b]8;;https://example.com')
    terminal.feed('\u001b\\ link\u001b]8;;\u001b\\ \ud83d')
    terminal.feed('\ude80')

    expect(terminal.text()).toBe('ready color link 🚀')
    expect(terminal.renderLines()[0]?.runs[1]).toEqual({
      text: 'color link 🚀',
      style: { color: 'rgb(12 34 56)' },
    })
  })

  it('strips unsupported control strings and commands from visible output', () => {
    const terminal = new TerminalModel()
    terminal.feed('before\u001b]0;window title\u0007after\u001b[?25l!\u0000\u0007')

    expect(terminal.text()).toBe('beforeafter!')
  })

  it('clamps hostile cursor coordinates and excessive unterminated controls', () => {
    const terminal = new TerminalModel()
    terminal.feed(`start\u001b[999999999;999999999Hend\u001b]0;${'x'.repeat(5000)}`)
    terminal.feed('still hidden\u0007visible')

    const lines = terminal.renderLines()
    expect(lines.length).toBeLessThanOrEqual(4096)
    expect(terminal.text()).toContain('visible')
    expect(terminal.text()).not.toContain('still hidden')
  })

  it('discards an oversized completed CSI command without allocating screen state', () => {
    const terminal = new TerminalModel()
    terminal.feed(`before\u001b[${'9;'.repeat(5000)}999Hafter`)

    expect(terminal.text()).toBe('beforeafter')
    expect(terminal.renderLines()).toHaveLength(1)
  })

  it('releases obsolete styles from repeated in-place redraws', () => {
    const terminal = new TerminalModel()
    for (let index = 0; index < 5000; index += 1) {
      const red = index % 256
      const green = Math.floor(index / 256) % 256
      terminal.feed(`\r\u001b[38;2;${red};${green};1m#`)
    }

    const styles = (terminal as unknown as { styles: Map<string, unknown> }).styles
    expect(styles.size).toBeLessThanOrEqual(4096)
    expect(terminal.text()).toBe('#')
    expect(terminal.renderLines()[0]?.runs).toHaveLength(1)
  })
})
