// TerminalDimensions describes the character grid expected by a terminal session.
export interface TerminalDimensions {
  cols: number
  rows: number
}

export type TerminalOutput = string | Uint8Array

// TerminalSession is the transport boundary between an interactive terminal and its host application.
export interface TerminalSession {
  start(): void | Promise<void>
  write(data: string): void | Promise<void>
  resize(dimensions: TerminalDimensions): void | Promise<void>
  close(): void | Promise<void>
  onOutput(listener: (data: TerminalOutput) => void): () => void
}
