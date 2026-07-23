import type { HarborBridge } from '@/bridge/types'
import type { ProjectTerminalEvent } from '@/domain/harbor'
import type { TerminalDimensions, TerminalOutput, TerminalSession } from './terminalSession'

const pendingInputLimit = 64 * 1024

// ProjectTerminalSession adapts the typed Harbor bridge to one xterm transport.
export class ProjectTerminalSession implements TerminalSession {
  private dimensions: TerminalDimensions = { cols: 80, rows: 24 }
  private readonly listeners = new Set<(data: TerminalOutput) => void>()
  private pendingInput = ''
  private sessionId = ''
  private unsubscribe: (() => void) | null = null
  private startPromise: Promise<void> | null = null
  private closePromise: Promise<void> | null = null
  private closed = false
  private exited = false

  constructor(
    private readonly bridge: HarborBridge,
    private readonly projectId: string,
  ) {}

  // start opens the native PTY once and catches events that race the Wails response.
  start(): Promise<void> {
    if (this.startPromise) return this.startPromise
    if (this.closed) return Promise.reject(new Error('Project terminal session is closed.'))

    this.unsubscribe = this.bridge.subscribeProjectTerminal((event) => {
      if (event.session_id === this.sessionId) this.receive(event)
    })
    this.startPromise = this.open()
    return this.startPromise
  }

  // write sends keyboard input after the native session identity is known.
  async write(data: string): Promise<void> {
    if (this.closed || this.exited) return
    if (!this.sessionId) {
      if (byteLength(this.pendingInput) + byteLength(data) > pendingInputLimit) {
        throw new Error('Terminal input exceeded 64 KiB before the shell was ready.')
      }
      this.pendingInput += data
      return
    }
    await this.bridge.writeProjectTerminal(this.sessionId, data)
  }

  // resize remembers the fitted grid until start and forwards later changes.
  async resize(dimensions: TerminalDimensions): Promise<void> {
    this.dimensions = dimensions
    if (!this.sessionId || this.closed || this.exited) return
    await this.bridge.resizeProjectTerminal(this.sessionId, dimensions.cols, dimensions.rows)
  }

  // close detaches events and terminates only this desktop-owned shell.
  async close(): Promise<void> {
    if (this.closePromise) return this.closePromise
    this.closed = true
    const closing = this.finishClose()
    this.closePromise = closing
    try {
      await closing
    }
    catch (error) {
      this.closePromise = null
      throw error
    }
  }

  // onOutput registers one binary-safe PTY output consumer.
  onOutput(listener: (data: TerminalOutput) => void): () => void {
    this.listeners.add(listener)
    return () => this.listeners.delete(listener)
  }

  // open resolves the opaque native ID before replaying any early output.
  private async open(): Promise<void> {
    const startedDimensions = this.dimensions
    try {
      const started = await this.bridge.startProjectTerminal(
        this.projectId,
        startedDimensions.cols,
        startedDimensions.rows,
      )
      this.sessionId = started.session_id

      if (this.closed) return
      await this.bridge.attachProjectTerminal(this.sessionId)
      if (this.closed || this.exited) return
      if (this.dimensions.cols !== startedDimensions.cols || this.dimensions.rows !== startedDimensions.rows) {
        await this.bridge.resizeProjectTerminal(
          this.sessionId,
          this.dimensions.cols,
          this.dimensions.rows,
        )
      }
      if (this.pendingInput) {
        const input = this.pendingInput
        this.pendingInput = ''
        await this.bridge.writeProjectTerminal(this.sessionId, input)
      }
    }
    catch (error) {
      this.unsubscribe?.()
      this.unsubscribe = null
      if (this.sessionId && !this.exited) {
        try {
          await this.bridge.closeProjectTerminal(this.sessionId)
        }
        catch {
          // The original start or attach failure remains the actionable error.
        }
      }
      this.startPromise = null
      throw error
    }
  }

  // finishClose reconciles a start response before releasing the native shell.
  private async finishClose(): Promise<void> {
    try {
      await this.startPromise
    }
    catch {
      return
    }
    finally {
      this.unsubscribe?.()
      this.unsubscribe = null
    }
    if (this.sessionId && !this.exited) {
      await this.bridge.closeProjectTerminal(this.sessionId)
    }
  }

  // receive decodes raw PTY bytes or presents the shell's terminal state.
  private receive(event: ProjectTerminalEvent) {
    if (event.dropped) {
      this.emit('\r\n[Some terminal output was dropped because the desktop was not keeping up.]\r\n')
    }
    if (event.kind === 'output') {
      if (!event.data_base64) return
      this.emit(decodeBase64(event.data_base64))
      return
    }

    this.exited = true
    const detail = event.error ? ` (${event.error})` : ''
    this.emit(`\r\n[Terminal exited${detail}]\r\n`)
  }

  // emit delivers a frame to every emulator currently attached to this adapter.
  private emit(data: TerminalOutput) {
    for (const listener of this.listeners) listener(data)
  }
}

// decodeBase64 preserves arbitrary PTY bytes rather than passing them through JSON text.
function decodeBase64(value: string): Uint8Array {
  const binary = atob(value)
  const data = new Uint8Array(binary.length)
  for (let index = 0; index < binary.length; index += 1) {
    data[index] = binary.charCodeAt(index)
  }
  return data
}

// byteLength enforces the backend's UTF-8 input frame limit before a session starts.
function byteLength(value: string): number {
  return new TextEncoder().encode(value).byteLength
}
