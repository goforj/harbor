import { describe, expect, it, vi } from 'vitest'
import type { HarborBridge } from '@/bridge/types'
import type { ProjectTerminalEvent, ProjectTerminalStarted } from '@/domain/harbor'
import { ProjectTerminalSession } from './projectTerminalSession'

// deferred exposes a Wails-like start response that can race terminal events.
function deferred<T>() {
  let resolve!: (value: T) => void
  const promise = new Promise<T>((resolvePromise) => {
    resolve = resolvePromise
  })
  return { promise, resolve }
}

// terminalBridge provides only the narrow bridge surface used by the adapter.
function terminalBridge(start: Promise<ProjectTerminalStarted>) {
  let listener: ((event: ProjectTerminalEvent) => void) | undefined
  const bridge = {
    startProjectTerminal: vi.fn(() => start),
    attachProjectTerminal: vi.fn().mockResolvedValue(undefined),
    writeProjectTerminal: vi.fn().mockResolvedValue(undefined),
    resizeProjectTerminal: vi.fn().mockResolvedValue(undefined),
    closeProjectTerminal: vi.fn().mockResolvedValue(undefined),
    subscribeProjectTerminal: vi.fn((next) => {
      listener = next
      return vi.fn()
    }),
  } as unknown as HarborBridge
  return {
    bridge,
    emit(event: ProjectTerminalEvent) {
      listener?.(event)
    },
  }
}

describe('ProjectTerminalSession', () => {
  it('uses fitted dimensions and attaches output only after the opaque start response', async () => {
    const started = deferred<ProjectTerminalStarted>()
    const transport = terminalBridge(started.promise)
    const session = new ProjectTerminalSession(transport.bridge, 'orders-api')
    const output: Array<string | Uint8Array> = []
    session.onOutput((data) => output.push(data))
    await session.resize({ cols: 117, rows: 31 })

    const opening = session.start()
    started.resolve({ session_id: 'terminal-1' })
    await opening
    transport.emit({
      session_id: 'terminal-1',
      kind: 'output',
      data_base64: btoa('ready\r\n'),
    })

    expect(transport.bridge.startProjectTerminal).toHaveBeenCalledWith('orders-api', 117, 31)
    expect(transport.bridge.attachProjectTerminal).toHaveBeenCalledWith('terminal-1')
    expect([...output[0] as Uint8Array]).toEqual([...new TextEncoder().encode('ready\r\n')])
  })

  it('replays a resize that arrives while the native start response is pending', async () => {
    const started = deferred<ProjectTerminalStarted>()
    const transport = terminalBridge(started.promise)
    const session = new ProjectTerminalSession(transport.bridge, 'orders-api')

    const opening = session.start()
    await session.resize({ cols: 132, rows: 42 })
    started.resolve({ session_id: 'terminal-4' })
    await opening

    expect(transport.bridge.startProjectTerminal).toHaveBeenCalledWith('orders-api', 80, 24)
    expect(transport.bridge.attachProjectTerminal).toHaveBeenCalledWith('terminal-4')
    expect(transport.bridge.resizeProjectTerminal).toHaveBeenCalledWith('terminal-4', 132, 42)
  })

  it('waits for a pending start response and closes the unattached shell', async () => {
    const started = deferred<ProjectTerminalStarted>()
    const transport = terminalBridge(started.promise)
    const session = new ProjectTerminalSession(transport.bridge, 'orders-api')

    void session.start()
    const closing = session.close()
    started.resolve({ session_id: 'terminal-5' })
    await closing

    expect(transport.bridge.attachProjectTerminal).not.toHaveBeenCalled()
    expect(transport.bridge.closeProjectTerminal).toHaveBeenCalledWith('terminal-5')
  })

  it('buffers early keyboard input and closes only its opaque native session', async () => {
    const started = deferred<ProjectTerminalStarted>()
    const transport = terminalBridge(started.promise)
    const session = new ProjectTerminalSession(transport.bridge, 'orders-api')

    const opening = session.start()
    await session.write('pwd\r')
    started.resolve({ session_id: 'terminal-2' })
    await opening
    await session.close()
    await session.close()

    expect(transport.bridge.writeProjectTerminal).toHaveBeenCalledWith('terminal-2', 'pwd\r')
    expect(transport.bridge.closeProjectTerminal).toHaveBeenCalledOnce()
    expect(transport.bridge.closeProjectTerminal).toHaveBeenCalledWith('terminal-2')
  })

  it('allows an indeterminate close request to be retried against the same native session', async () => {
    const transport = terminalBridge(Promise.resolve({ session_id: 'terminal-retry' }))
    vi.mocked(transport.bridge.closeProjectTerminal)
      .mockRejectedValueOnce(new Error('desktop transport closed'))
      .mockResolvedValueOnce(undefined)
    const session = new ProjectTerminalSession(transport.bridge, 'orders-api')
    await session.start()

    await expect(session.close()).rejects.toThrow('desktop transport closed')
    await session.close()

    expect(transport.bridge.closeProjectTerminal).toHaveBeenCalledTimes(2)
    expect(transport.bridge.closeProjectTerminal).toHaveBeenNthCalledWith(2, 'terminal-retry')
  })

  it('renders an exit event and does not close an already-reaped shell', async () => {
    const transport = terminalBridge(Promise.resolve({ session_id: 'terminal-3' }))
    const session = new ProjectTerminalSession(transport.bridge, 'orders-api')
    const output: Array<string | Uint8Array> = []
    session.onOutput((data) => output.push(data))
    await session.start()

    transport.emit({
      session_id: 'terminal-3',
      kind: 'exited',
      error: 'exit status 2',
    })
    await session.close()

    expect(output).toEqual(['\r\n[Terminal exited (exit status 2)]\r\n'])
    expect(transport.bridge.closeProjectTerminal).not.toHaveBeenCalled()
  })
})
