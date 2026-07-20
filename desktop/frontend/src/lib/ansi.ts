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

const escapeCharacter = '\u001b'
const controlSequenceIntroducer = '\u009b'
const operatingSystemCommand = '\u009d'
const stringTerminator = '\u009c'
const defaultForeground = '#e4e4e7'
const defaultBackground = '#09090b'
const terminalColors = [
  '#000000', '#cd3131', '#0dbc79', '#e5e510',
  '#2472c8', '#bc3fbc', '#11a8cd', '#e5e5e5',
  '#666666', '#f14c4c', '#23d18b', '#f5f543',
  '#3b8eea', '#d670d6', '#29b8db', '#ffffff',
] as const

interface EscapeSequence {
  next: number
  sgr?: string
}

// ansiToHtml converts terminal styling into renderer-owned spans while escaping all process output.
export function ansiToHtml(input: string): string {
  const style = defaultStyle()
  let activeStyle = ''
  let html = ''
  let text = ''

  const flush = () => {
    if (!text) return

    const nextStyle = styleAttribute(style)
    if (nextStyle !== activeStyle) {
      if (activeStyle) html += '</span>'
      if (nextStyle) html += `<span style="${nextStyle}">`
      activeStyle = nextStyle
    }
    html += escapeText(text)
    text = ''
  }

  for (let index = 0; index < input.length;) {
    const character = input[index]
    if (character === escapeCharacter || character === controlSequenceIntroducer || character === operatingSystemCommand) {
      flush()
      const sequence = readEscapeSequence(input, index)
      if (sequence.sgr != null) applySgr(style, sequence.sgr)
      index = sequence.next
      continue
    }

    const code = input.charCodeAt(index)
    if ((code < 32 && character !== '\n' && character !== '\r' && character !== '\t') || (code >= 127 && code <= 159)) {
      index += 1
      continue
    }

    text += character
    index += 1
  }

  flush()
  if (activeStyle) html += '</span>'
  return html
}

// defaultStyle returns an independent style because SGR sequences mutate terminal state.
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

// readEscapeSequence consumes control sequences so terminal metadata cannot become visible or executable markup.
function readEscapeSequence(input: string, start: number): EscapeSequence {
  const character = input[start]
  if (character === operatingSystemCommand) return readOperatingSystemCommand(input, start + 1)
  if (character === controlSequenceIntroducer) return readControlSequence(input, start + 1)
  if (input[start + 1] === ']') return readOperatingSystemCommand(input, start + 2)
  if (input[start + 1] === '[') return readControlSequence(input, start + 2)
  return { next: Math.min(start + 2, input.length) }
}

// readOperatingSystemCommand discards titles, hyperlinks, and other host-directed terminal commands.
function readOperatingSystemCommand(input: string, start: number): EscapeSequence {
  for (let index = start; index < input.length; index += 1) {
    if (input[index] === '\u0007' || input[index] === stringTerminator) return { next: index + 1 }
    if (input[index] === escapeCharacter && input[index + 1] === '\\') return { next: index + 2 }
  }
  return { next: input.length }
}

// readControlSequence recognizes SGR styling and discards cursor or display mutation commands.
function readControlSequence(input: string, start: number): EscapeSequence {
  for (let index = start; index < input.length; index += 1) {
    const code = input.charCodeAt(index)
    if (code >= 0x40 && code <= 0x7e) {
      return {
        next: index + 1,
        sgr: input[index] === 'm' ? input.slice(start, index) : undefined,
      }
    }
  }
  return { next: input.length }
}

// applySgr updates the subset of terminal presentation that has a faithful, static HTML equivalent.
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

// styleAttribute derives only constant property names and validated color values for safe v-html use.
function styleAttribute(style: TerminalStyle): string {
  let foreground = style.foreground
  let background = style.background
  if (style.inverse) {
    const priorForeground = foreground ?? defaultForeground
    foreground = background ?? defaultBackground
    background = priorForeground
  }

  const declarations: string[] = []
  if (foreground) declarations.push(`color:${foreground}`)
  if (background) declarations.push(`background-color:${background}`)
  if (style.bold) declarations.push('font-weight:700')
  if (style.dim) declarations.push('opacity:0.65')
  if (style.italic) declarations.push('font-style:italic')
  if (style.hidden) declarations.push('visibility:hidden')
  const decoration = [style.underline ? 'underline' : '', style.strike ? 'line-through' : ''].filter(Boolean)
  if (decoration.length) declarations.push(`text-decoration-line:${decoration.join(' ')}`)
  return declarations.join(';')
}

// escapeText ensures daemon and child-process output remains text even though the result uses v-html.
function escapeText(value: string): string {
  return value
    .replaceAll('&', '&amp;')
    .replaceAll('<', '&lt;')
    .replaceAll('>', '&gt;')
    .replaceAll('"', '&quot;')
    .replaceAll("'", '&#39;')
}
