import type { CSSProperties } from 'vue'

export type TerminalRunStyle = CSSProperties

export interface TerminalRun {
  text: string
  style: TerminalRunStyle
}

export interface TerminalLine {
  id: number
  runs: TerminalRun[]
}

// TerminalTextSegment is one safe text or web-link fragment for terminal rendering.
export interface TerminalTextSegment {
  text: string
  url?: string
}

interface TerminalStyle {
  background?: string
  bold: boolean
  dim: boolean
  foreground?: string
  hidden: boolean
  inverse: boolean
  italic: boolean
  strike: boolean
  underline: boolean
}

interface TerminalCell {
  character: string
  style: string
}

interface MutableTerminalLine {
  cells: TerminalCell[]
  dirty: boolean
  id: number
  rendered: TerminalLine
}

interface ParsedSequence {
  complete: boolean
  final?: string
  next: number
  parameters?: string
}

const escapeCharacter = '\u001b'
const controlSequenceIntroducer = '\u009b'
const operatingSystemCommand = '\u009d'
const stringTerminator = '\u009c'
const maximumColumns = 4096
const maximumLines = 4096
const maximumRetainedCells = 256 * 1024
const minimumStylePruneThreshold = 4096
const stylePruneHeadroom = 256
const maximumPendingSequenceCharacters = 4096
const defaultForeground = '#e4e4e7'
const defaultBackground = '#09090b'
const terminalColors = [
  '#000000', '#cd3131', '#0dbc79', '#e5e510',
  '#2472c8', '#bc3fbc', '#11a8cd', '#e5e5e5',
  '#666666', '#f14c4c', '#23d18b', '#f5f543',
  '#3b8eea', '#d670d6', '#29b8db', '#ffffff',
] as const

// TerminalModel incrementally applies the terminal controls emitted by interactive development tools.
export class TerminalModel {
  private cellCount = 0
  private column = 0
  private currentStyle = defaultStyle()
  private discardingControlSequence = false
  private discardingStringControl = false
  private lineSequence = 0
  private lines: MutableTerminalLine[] = []
  private pending = ''
  private row = 0
  private savedColumn = 0
  private savedRow = 0
  private readonly styles = new Map<string, TerminalStyle>()

  constructor() {
    this.lines.push(this.newLine())
  }

  // feed applies only the new process bytes so completed scrollback is not reparsed on every update.
  feed(chunk: string): void {
    if (!chunk) return

    let input = this.pending + chunk
    this.pending = ''
    let index = 0

    if (this.discardingStringControl) {
      const end = findStringTerminator(input, 0)
      if (end < 0) return
      this.discardingStringControl = false
      index = end
    }
    if (this.discardingControlSequence) {
      const end = findControlSequenceFinal(input, index)
      if (end < 0) return
      this.discardingControlSequence = false
      index = end + 1
    }

    while (index < input.length) {
      const character = input[index] ?? ''
      if (character === escapeCharacter) {
        const sequence = this.readEscape(input, index)
        if (!sequence.complete) {
          const incomplete = input.slice(index)
          if (isEscapeStringControl(incomplete[1] ?? '')) this.retainIncompleteStringControl(incomplete)
          else this.retainIncompleteSequence(incomplete, false)
          break
        }
        if (sequence.final) this.applyEscape(sequence.final, sequence.parameters ?? '')
        index = sequence.next
        continue
      }
      if (character === controlSequenceIntroducer) {
        const sequence = readControlSequence(input, index + 1)
        if (!sequence.complete) {
          this.retainIncompleteSequence(input.slice(index), true)
          break
        }
        this.applyControlSequence(sequence.final ?? '', sequence.parameters ?? '')
        index = sequence.next
        continue
      }
      if (isStringControl(character)) {
        const end = findStringTerminator(input, index + 1)
        if (end < 0) {
          this.retainIncompleteStringControl(input.slice(index))
          break
        }
        index = end
        continue
      }

      const code = input.charCodeAt(index)
      if (character === '\n') this.lineFeed()
      else if (character === '\r') this.column = 0
      else if (character === '\b') this.column = Math.max(0, this.column - 1)
      else if (character === '\t') this.column = Math.min(maximumColumns - 1, (Math.floor(this.column / 8) + 1) * 8)
      else if (code >= 32 && code !== 127 && !(code >= 128 && code <= 159)) {
        if (code >= 0xd800 && code <= 0xdbff && index + 1 >= input.length) {
          this.pending = character
          break
        }
        const codePoint = input.codePointAt(index)
        const printable = codePoint == null ? character : String.fromCodePoint(codePoint)
        this.write(printable)
        index += printable.length
        continue
      }
      index += 1
    }
  }

  // renderLines returns stable line objects for rows which have not changed since the prior frame.
  renderLines(): TerminalLine[] {
    return this.lines.map((line) => {
      if (!line.dirty) return line.rendered
      const runs: TerminalRun[] = []
      let priorStyle = ''
      for (const cell of line.cells) {
        const prior = runs.at(-1)
        if (prior && priorStyle === cell.style) prior.text += cell.character
        else {
          priorStyle = cell.style
          runs.push({ text: cell.character, style: runStyle(this.styles.get(cell.style) ?? defaultStyle()) })
        }
      }
      line.rendered = { id: line.id, runs }
      line.dirty = false
      return line.rendered
    })
  }

  // reset starts a new terminal presentation while preserving monotonically increasing row identities.
  reset(): void {
    this.cellCount = 0
    this.column = 0
    this.currentStyle = defaultStyle()
    this.discardingControlSequence = false
    this.discardingStringControl = false
    this.lines = [this.newLine()]
    this.pending = ''
    this.row = 0
    this.savedColumn = 0
    this.savedRow = 0
    this.styles.clear()
  }

  // text exposes the visible terminal state for deterministic tests and non-HTML consumers.
  text(): string {
    return this.lines.map((line) => line.cells.map((cell) => cell.character).join('')).join('\n')
  }

  // readEscape recognizes terminal controls while retaining incomplete sequences for the next chunk.
  private readEscape(input: string, start: number): ParsedSequence {
    if (start + 1 >= input.length) return { complete: false, next: start }
    const introducer = input[start + 1] ?? ''
    if (introducer === '[') {
      const sequence = readControlSequence(input, start + 2)
      if (sequence.complete) this.applyControlSequence(sequence.final ?? '', sequence.parameters ?? '')
      return { ...sequence, final: undefined }
    }
    if (introducer === ']' || introducer === 'P' || introducer === 'X' || introducer === '^' || introducer === '_') {
      const end = findStringTerminator(input, start + 2)
      return end < 0 ? { complete: false, next: start } : { complete: true, next: end }
    }
    return { complete: true, final: introducer, next: start + 2 }
  }

  // retainIncompleteSequence bounds malformed control input without exposing its payload as process text.
  private retainIncompleteSequence(sequence: string, controlSequence: boolean): void {
    if (sequence.length <= maximumPendingSequenceCharacters) {
      this.pending = sequence
      return
    }
    this.discardingControlSequence = controlSequence || sequence.startsWith(`${escapeCharacter}[`)
  }

  // retainIncompleteStringControl remembers only parsing state once an unterminated host command becomes excessive.
  private retainIncompleteStringControl(sequence: string): void {
    if (sequence.length <= maximumPendingSequenceCharacters) {
      this.pending = sequence
      return
    }
    this.discardingStringControl = true
  }

  // applyEscape handles the small set of two-byte cursor controls used by terminal renderers.
  private applyEscape(final: string, _parameters: string): void {
    if (final === '7') {
      this.savedRow = this.row
      this.savedColumn = this.column
    }
    else if (final === '8') {
      this.row = clamp(this.savedRow, 0, maximumLines - 1)
      this.column = clamp(this.savedColumn, 0, maximumColumns - 1)
    }
    else if (final === 'D') this.lineFeed()
    else if (final === 'E') this.nextLine(1)
    else if (final === 'M') this.row = Math.max(0, this.row - 1)
  }

  // applyControlSequence mutates terminal state for cursor, erase, and presentation sequences.
  private applyControlSequence(final: string, parameters: string): void {
    if (final === 'm') {
      applySgr(this.currentStyle, parameters)
      return
    }

    const values = cursorParameters(parameters)
    const count = boundedCount(values[0])
    if (final === 'A') this.row = Math.max(0, this.row - count)
    else if (final === 'B') this.row = Math.min(maximumLines - 1, this.row + count)
    else if (final === 'C') this.column = Math.min(maximumColumns - 1, this.column + count)
    else if (final === 'D') this.column = Math.max(0, this.column - count)
    else if (final === 'E') this.nextLine(count)
    else if (final === 'F') {
      this.row = Math.max(0, this.row - count)
      this.column = 0
    }
    else if (final === 'G' || final === '`') this.column = coordinate(values[0], maximumColumns)
    else if (final === 'H' || final === 'f') {
      this.row = coordinate(values[0], maximumLines)
      this.column = coordinate(values[1], maximumColumns)
    }
    else if (final === 'd') this.row = coordinate(values[0], maximumLines)
    else if (final === 'J') this.eraseDisplay(values[0] ?? 0)
    else if (final === 'K') this.eraseLine(values[0] ?? 0)
    else if (final === 's') {
      this.savedRow = this.row
      this.savedColumn = this.column
    }
    else if (final === 'u') {
      this.row = clamp(this.savedRow, 0, maximumLines - 1)
      this.column = clamp(this.savedColumn, 0, maximumColumns - 1)
    }
  }

  // write overwrites the cell under the cursor, which is what makes carriage-return loaders update in place.
  private write(character: string): void {
    if (this.column >= maximumColumns) this.lineFeed()
    const line = this.ensureLine(this.row)
    const style = this.internCurrentStyle()
    while (line.cells.length < this.column) {
      line.cells.push({ character: ' ', style: this.internDefaultStyle() })
      this.cellCount += 1
    }
    if (this.column < line.cells.length) line.cells[this.column] = { character, style }
    else {
      line.cells.push({ character, style })
      this.cellCount += 1
    }
    line.dirty = true
    this.column += 1
    this.enforceBounds()
    if (this.styles.size > Math.max(minimumStylePruneThreshold, this.cellCount + stylePruneHeadroom)) this.pruneStyles()
  }

  // lineFeed creates scrollback one row at a time and keeps columns aligned with ordinary log output.
  private lineFeed(): void {
    this.row = Math.min(maximumLines - 1, this.row + 1)
    this.column = 0
    this.ensureLine(this.row)
    this.enforceBounds()
  }

  // nextLine implements CSI E without letting hostile counts create unbounded sparse rows.
  private nextLine(count: number): void {
    this.row = Math.min(maximumLines - 1, this.row + count)
    this.column = 0
  }

  // eraseLine applies terminal erase modes without treating cursor controls as printable output.
  private eraseLine(mode: number): void {
    const line = this.ensureLine(this.row)
    if (mode === 2) {
      this.cellCount -= line.cells.length
      line.cells = []
    }
    else if (mode === 1) {
      const end = Math.min(this.column, maximumColumns - 1)
      const style = this.internCurrentStyle()
      while (line.cells.length <= end) {
        line.cells.push({ character: ' ', style })
        this.cellCount += 1
      }
      for (let index = 0; index <= end; index += 1) line.cells[index] = { character: ' ', style }
    }
    else if (this.column < line.cells.length) {
      this.cellCount -= line.cells.length - this.column
      line.cells.splice(this.column)
    }
    line.dirty = true
    this.enforceBounds()
  }

  // eraseDisplay supports clear-to-edge and full-screen loader redraws while retaining bounded scrollback.
  private eraseDisplay(mode: number): void {
    if (mode === 2 || mode === 3) {
      this.cellCount = 0
      this.lines = [this.newLine()]
      this.row = 0
      this.column = 0
      return
    }

    const line = this.ensureLine(this.row)
    if (mode === 1) {
      for (let index = 0; index < this.row; index += 1) this.clearLine(this.lines[index])
      const style = this.internCurrentStyle()
      const end = Math.min(this.column, maximumColumns - 1)
      while (line.cells.length <= end) {
        line.cells.push({ character: ' ', style })
        this.cellCount += 1
      }
      for (let index = 0; index <= end; index += 1) line.cells[index] = { character: ' ', style }
      line.dirty = true
      return
    }

    this.eraseLine(0)
    while (this.lines.length > this.row + 1) {
      const removed = this.lines.pop()
      this.cellCount -= removed?.cells.length ?? 0
    }
  }

  // clearLine removes retained cells while keeping row coordinates stable for a partial display erase.
  private clearLine(line: MutableTerminalLine | undefined): void {
    if (!line) return
    this.cellCount -= line.cells.length
    line.cells = []
    line.dirty = true
  }

  // ensureLine materializes only a clamped row coordinate.
  private ensureLine(row: number): MutableTerminalLine {
    const target = clamp(row, 0, maximumLines - 1)
    while (this.lines.length <= target) this.lines.push(this.newLine())
    return this.lines[target] ?? this.lines[0]!
  }

  // enforceBounds drops oldest rows before renderer memory can grow beyond the retained activity budget.
  private enforceBounds(): void {
    let remove = Math.max(0, this.lines.length - maximumLines)
    while (this.cellCount > maximumRetainedCells && remove < this.lines.length - 1) {
      this.cellCount -= this.lines[remove]?.cells.length ?? 0
      remove += 1
    }
    if (remove === 0) return
    this.lines.splice(0, remove)
    this.row = Math.max(0, this.row - remove)
    this.savedRow = Math.max(0, this.savedRow - remove)
    this.pruneStyles()
  }

  // pruneStyles releases presentation states that disappeared through overwrite or scrollback eviction.
  private pruneStyles(): void {
    const current = styleKey(this.currentStyle)
    const retained = new Set<string>([current])
    for (const line of this.lines) {
      for (const cell of line.cells) retained.add(cell.style)
    }
    for (const key of this.styles.keys()) {
      if (!retained.has(key)) this.styles.delete(key)
    }
    if (!this.styles.has(current)) this.styles.set(current, { ...this.currentStyle })
  }

  // newLine assigns stable identities so Vue can update only terminal rows touched by a frame.
  private newLine(): MutableTerminalLine {
    const id = this.lineSequence
    this.lineSequence += 1
    return { cells: [], dirty: false, id, rendered: { id, runs: [] } }
  }

  // internCurrentStyle deduplicates immutable style snapshots across terminal cells.
  private internCurrentStyle(): string {
    const key = styleKey(this.currentStyle)
    if (!this.styles.has(key)) this.styles.set(key, { ...this.currentStyle })
    return key
  }

  // internDefaultStyle gives cursor-created spaces the terminal's neutral presentation.
  private internDefaultStyle(): string {
    const style = defaultStyle()
    const key = styleKey(style)
    if (!this.styles.has(key)) this.styles.set(key, style)
    return key
  }
}

// terminalPlainText returns the visible text for clipboard use without terminal control sequences or styling.
export function terminalPlainText(output: string): string {
  const terminal = new TerminalModel()
  terminal.feed(output)
  return terminal.text()
}

// terminalLinkSegments recognizes only absolute HTTP(S) URLs from untrusted process output.
export function terminalLinkSegments(text: string): TerminalTextSegment[] {
  const segments: TerminalTextSegment[] = []
  const matcher = /https?:\/\/[^\s<>"']+/g
  let end = 0
  for (const match of text.matchAll(matcher)) {
    const start = match.index ?? 0
    if (start > end) appendTerminalText(segments, text.slice(end, start))
    const candidate = trimTerminalURLPunctuation(match[0])
    if (candidate && isSafeTerminalURL(candidate)) segments.push({ text: candidate, url: candidate })
    else if (candidate) appendTerminalText(segments, candidate)
    const suffix = match[0].slice(candidate.length)
    if (suffix) appendTerminalText(segments, suffix)
    end = start + match[0].length
  }
  if (end < text.length) appendTerminalText(segments, text.slice(end))
  return segments.length ? segments : [{ text }]
}

// appendTerminalText keeps adjacent plain fragments in one text node for compact terminal DOM output.
function appendTerminalText(segments: TerminalTextSegment[], text: string): void {
  const previous = segments.at(-1)
  if (previous && previous.url == null) previous.text += text
  else segments.push({ text })
}

// trimTerminalURLPunctuation leaves sentence punctuation outside a link without altering a valid URL path.
function trimTerminalURLPunctuation(value: string): string {
  return value.replace(/[.,!?;:]+$/, '').replace(/\)+$/, '')
}

// isSafeTerminalURL prevents log text from creating non-web navigation targets.
function isSafeTerminalURL(value: string): boolean {
  try {
    const parsed = new URL(value)
    return (parsed.protocol === 'http:' || parsed.protocol === 'https:') && parsed.username === '' && parsed.password === ''
  }
  catch {
    return false
  }
}

// readControlSequence returns parameter bytes and the first standards-defined final byte.
function readControlSequence(input: string, start: number): ParsedSequence {
  const final = findControlSequenceFinal(input, start)
  if (final < 0) return { complete: false, next: start }
  if (final - start > maximumPendingSequenceCharacters) return { complete: true, next: final + 1 }
  return {
    complete: true,
    final: input[final],
    next: final + 1,
    parameters: input.slice(start, final),
  }
}

// findControlSequenceFinal locates the byte that terminates a CSI command.
function findControlSequenceFinal(input: string, start: number): number {
  for (let index = start; index < input.length; index += 1) {
    const code = input.charCodeAt(index)
    if (code >= 0x40 && code <= 0x7e) return index
  }
  return -1
}

// findStringTerminator strips OSC and related host controls through BEL or ST.
function findStringTerminator(input: string, start: number): number {
  for (let index = start; index < input.length; index += 1) {
    if (input[index] === '\u0007' || input[index] === stringTerminator) return index + 1
    if (input[index] === escapeCharacter && input[index + 1] === '\\') return index + 2
  }
  return -1
}

// isStringControl identifies C1 host-directed string commands that must never become visible markup.
function isStringControl(character: string): boolean {
  return character === operatingSystemCommand
    || character === '\u0090'
    || character === '\u0098'
    || character === '\u009e'
    || character === '\u009f'
}

// isEscapeStringControl identifies the seven-bit form of host-directed string commands.
function isEscapeStringControl(character: string): boolean {
  return character === ']' || character === 'P' || character === 'X' || character === '^' || character === '_'
}

// cursorParameters accepts private prefixes but exposes only bounded integer coordinates.
function cursorParameters(parameters: string): number[] {
  const normalized = parameters.replace(/^[?<>=!]+/, '').split(';')
  return normalized.map((parameter) => {
    const value = Number.parseInt(parameter, 10)
    return Number.isFinite(value) && value >= 0 ? value : 0
  })
}

// boundedCount follows the terminal convention that zero and omitted movement counts mean one.
function boundedCount(value: number | undefined): number {
  return clamp(value || 1, 1, Math.max(maximumColumns, maximumLines))
}

// coordinate translates one-based terminal positions into bounded zero-based indexes.
function coordinate(value: number | undefined, limit: number): number {
  return clamp((value || 1) - 1, 0, limit - 1)
}

// clamp keeps control-sequence coordinates within fixed renderer limits.
function clamp(value: number, minimum: number, maximum: number): number {
  return Math.min(maximum, Math.max(minimum, value))
}

// defaultStyle returns independent SGR state because terminal commands mutate presentation in place.
function defaultStyle(): TerminalStyle {
  return {
    background: undefined,
    bold: false,
    dim: false,
    foreground: undefined,
    hidden: false,
    inverse: false,
    italic: false,
    strike: false,
    underline: false,
  }
}

// applySgr updates the subset of terminal presentation with a faithful browser equivalent.
function applySgr(style: TerminalStyle, parameters: string): void {
  const normalized = parameters.replaceAll('::', ':')
  const codes = (normalized === '' ? ['0'] : normalized.split(/[;:]/)).map((value) => Number.parseInt(value || '0', 10))

  for (let index = 0; index < codes.length; index += 1) {
    const code = codes[index]
    if (!Number.isFinite(code)) continue
    if (code === 0) Object.assign(style, defaultStyle())
    else if (code === 1) style.bold = true
    else if (code === 2) style.dim = true
    else if (code === 3) style.italic = true
    else if (code === 4) style.underline = true
    else if (code === 7) style.inverse = true
    else if (code === 8) style.hidden = true
    else if (code === 9) style.strike = true
    else if (code === 21) style.bold = false
    else if (code === 22) {
      style.bold = false
      style.dim = false
    }
    else if (code === 23) style.italic = false
    else if (code === 24) style.underline = false
    else if (code === 27) style.inverse = false
    else if (code === 28) style.hidden = false
    else if (code === 29) style.strike = false
    else if (code >= 30 && code <= 37) style.foreground = terminalColors[code - 30]
    else if (code === 38 || code === 48) {
      const extended = readExtendedColor(codes, index + 1)
      if (extended.color) {
        if (code === 38) style.foreground = extended.color
        else style.background = extended.color
      }
      index = extended.next - 1
    }
    else if (code === 39) style.foreground = undefined
    else if (code >= 40 && code <= 47) style.background = terminalColors[code - 40]
    else if (code === 49) style.background = undefined
    else if (code >= 90 && code <= 97) style.foreground = terminalColors[code - 90 + 8]
    else if (code >= 100 && code <= 107) style.background = terminalColors[code - 100 + 8]
  }
}

// readExtendedColor converts terminal palette and true-color forms into bounded CSS colors.
function readExtendedColor(codes: number[], start: number): { color?: string, next: number } {
  const mode = codes[start]
  if (mode === 5) {
    const index = codes[start + 1]
    return {
      color: Number.isInteger(index) && index >= 0 && index <= 255 ? indexedColor(index) : undefined,
      next: Math.min(start + 2, codes.length),
    }
  }
  if (mode === 2) {
    const channels = codes.slice(start + 1, start + 4)
    const valid = channels.length === 3 && channels.every((channel) => Number.isInteger(channel) && channel >= 0 && channel <= 255)
    return {
      color: valid ? `rgb(${channels.join(' ')})` : undefined,
      next: Math.min(start + 4, codes.length),
    }
  }
  return { next: Math.min(start + 1, codes.length) }
}

// indexedColor follows the xterm 256-color palette used by Go terminal formatters.
function indexedColor(index: number): string {
  if (index < terminalColors.length) return terminalColors[index] ?? defaultForeground
  if (index >= 232) {
    const channel = 8 + ((index - 232) * 10)
    return `rgb(${channel} ${channel} ${channel})`
  }
  const offset = index - 16
  const levels = [0, 95, 135, 175, 215, 255]
  const red = levels[Math.floor(offset / 36)] ?? 0
  const green = levels[Math.floor((offset % 36) / 6)] ?? 0
  const blue = levels[offset % 6] ?? 0
  return `rgb(${red} ${green} ${blue})`
}

// styleKey provides a stable value for style interning and renderer run coalescing.
function styleKey(style: TerminalStyle): string {
  return [
    style.foreground ?? '', style.background ?? '', style.bold ? '1' : '0', style.dim ? '1' : '0',
    style.italic ? '1' : '0', style.underline ? '1' : '0', style.inverse ? '1' : '0',
    style.hidden ? '1' : '0', style.strike ? '1' : '0',
  ].join('|')
}

// runStyle derives only constant property names and validated colors for safe Vue style binding.
function runStyle(style: TerminalStyle): TerminalRunStyle {
  let foreground = style.foreground
  let background = style.background
  if (style.inverse) {
    const priorForeground = foreground ?? defaultForeground
    foreground = background ?? defaultBackground
    background = priorForeground
  }
  const result: TerminalRunStyle = {}
  if (foreground) result.color = foreground
  if (background) result.backgroundColor = background
  if (style.bold) result.fontWeight = '700'
  if (style.dim) result.opacity = '0.65'
  if (style.italic) result.fontStyle = 'italic'
  if (style.hidden) result.visibility = 'hidden'
  const decoration = [style.underline ? 'underline' : '', style.strike ? 'line-through' : ''].filter(Boolean)
  if (decoration.length) result.textDecorationLine = decoration.join(' ')
  return result
}
