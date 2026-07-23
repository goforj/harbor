import { mount } from '@vue/test-utils'
import { nextTick } from 'vue'
import { afterEach, describe, expect, it, vi } from 'vitest'
import InteractiveTerminal from './InteractiveTerminal.vue'
import type { TerminalSession } from '@/lib/terminalSession'

const observers: MockResizeObserver[] = []

const mocks = vi.hoisted(() => {
  const terminals: Array<{
    cols: number
    rows: number
    open: ReturnType<typeof vi.fn>
    loadAddon: ReturnType<typeof vi.fn>
    write: ReturnType<typeof vi.fn>
    dispose: ReturnType<typeof vi.fn>
    onData(listener: (data: string) => void): { dispose: ReturnType<typeof vi.fn> }
    sendInput(data: string): void
  }> = []
  const fits: Array<{ dispose: ReturnType<typeof vi.fn>, fit: ReturnType<typeof vi.fn> }> = []

  class MockTerminal {
    cols = 80
    rows = 24
    readonly open = vi.fn()
    readonly loadAddon = vi.fn()
    readonly write = vi.fn()
    readonly dispose = vi.fn()
    private inputListener: ((data: string) => void) | null = null

    constructor() {
      terminals.push(this)
    }

    onData(listener: (data: string) => void) {
      this.inputListener = listener
      return { dispose: vi.fn(() => { this.inputListener = null }) }
    }

    sendInput(data: string) {
      this.inputListener?.(data)
    }
  }

  class MockFitAddon {
    readonly dispose = vi.fn()
    readonly fit = vi.fn()

    constructor() {
      fits.push(this)
    }
  }

  return { MockFitAddon, MockTerminal, fits, terminals }
})

class MockResizeObserver {
  readonly disconnect = vi.fn()
  readonly observe = vi.fn()
  readonly unobserve = vi.fn()

  constructor(readonly callback: ResizeObserverCallback) {
    observers.push(this)
  }
}

vi.mock('@xterm/xterm', () => ({ Terminal: mocks.MockTerminal }))
vi.mock('@xterm/addon-fit', () => ({ FitAddon: mocks.MockFitAddon }))

describe('InteractiveTerminal', () => {
  afterEach(() => {
    mocks.terminals.length = 0
    mocks.fits.length = 0
    observers.length = 0
  })

  it('connects xterm input, output, initial dimensions, and startup through its session adapter', async () => {
    let outputListener: ((data: string) => void) | undefined
    vi.stubGlobal('ResizeObserver', MockResizeObserver)
    const session = createSession({
      onOutput: vi.fn((listener) => {
        outputListener = listener
        return vi.fn()
      }),
    })
    const wrapper = mount(InteractiveTerminal, { props: { session, ariaLabel: 'Project shell' } })
    await nextTick()

    const terminal = mocks.terminals[0]
    expect(terminal?.open).toHaveBeenCalledWith(wrapper.element)
    expect(mocks.fits[0]?.fit).toHaveBeenCalledOnce()
    expect(session.resize).toHaveBeenCalledWith({ cols: 80, rows: 24 })
    expect(session.start).toHaveBeenCalledOnce()
    expect(wrapper.attributes('aria-label')).toBe('Project shell')

    outputListener?.('ready\\r\\n')
    terminal?.sendInput('ls\\r')
    expect(terminal?.write).toHaveBeenCalledWith('ready\\r\\n')
    expect(session.write).toHaveBeenCalledWith('ls\\r')
  })

  it('does not repeat the same PTY resize and releases terminal resources on unmount', async () => {
    const removeOutputListener = vi.fn()
    vi.stubGlobal('ResizeObserver', MockResizeObserver)
    const session = createSession({ onOutput: vi.fn(() => removeOutputListener) })
    const wrapper = mount(InteractiveTerminal, { props: { session } })
    await nextTick()

    const observer = observers[0]
    observer?.callback([], observer)
    expect(session.resize).toHaveBeenCalledOnce()

    wrapper.unmount()
    expect(removeOutputListener).toHaveBeenCalledOnce()
    expect(session.close).toHaveBeenCalledOnce()
    expect(mocks.terminals[0]?.dispose).toHaveBeenCalledOnce()
    expect(mocks.fits[0]?.dispose).toHaveBeenCalledOnce()
  })

  it('reports rejected session operations instead of leaking an asynchronous error', async () => {
    const error = new Error('PTY unavailable')
    vi.stubGlobal('ResizeObserver', MockResizeObserver)
    const session = createSession({ start: vi.fn().mockRejectedValue(error) })
    const wrapper = mount(InteractiveTerminal, { props: { session } })
    await nextTick()
    await nextTick()

    expect(wrapper.emitted('error')).toEqual([[error]])
  })
})

// createSession provides a complete adapter while allowing each test to override one transport operation.
function createSession(overrides: Partial<TerminalSession> = {}): TerminalSession {
  return {
    start: vi.fn(),
    write: vi.fn(),
    resize: vi.fn(),
    close: vi.fn(),
    onOutput: vi.fn(() => vi.fn()),
    ...overrides,
  }
}
