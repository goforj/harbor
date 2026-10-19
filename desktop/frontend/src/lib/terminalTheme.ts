// terminalTheme keeps hosted logs and interactive shells on the same restrained dark palette.
export const terminalTheme = {
  background: '#0a080d',
  foreground: '#e2e2e2',
  chrome: '#100d14',
  border: '#29232f',
  black: '#262626', red: '#e98b85', green: '#76c69c', yellow: '#dfbd78',
  blue: '#7eb1e2', magenta: '#c19ddd', cyan: '#7dc6cf', white: '#dedede',
  brightBlack: '#8a8a8a', brightRed: '#efaaa4', brightGreen: '#a4d0b5', brightYellow: '#e8d4a5',
  brightBlue: '#aac6e2', brightMagenta: '#ceb8df', brightCyan: '#a5d0d5', brightWhite: '#fafafa',
}

export type TerminalPalette = typeof terminalTheme

export const terminalThemes: Record<'dark' | 'light', TerminalPalette> = {
  dark: terminalTheme,
  light: {
    background: '#f6f8fa', foreground: '#1f2328', chrome: '#ffffff', border: '#dfe3e8',
    black: '#1f2328', red: '#b3213c', green: '#0f6e52', yellow: '#8a5a00',
    blue: '#0550ae', magenta: '#8250df', cyan: '#096b79', white: '#59636e',
    brightBlack: '#6e7781', brightRed: '#9f2c19', brightGreen: '#116329', brightYellow: '#6e4700',
    brightBlue: '#0969da', brightMagenta: '#6639ba', brightCyan: '#055d69', brightWhite: '#1f2328',
  },
}

// terminalThemeVariables keeps retained ANSI runs reactive without replaying output or changing cursors.
export function terminalThemeVariables(palette: TerminalPalette): Record<string, string> {
  return Object.fromEntries(Object.entries(palette).map(([key, value]) => [
    `--terminal-${key.replace(/[A-Z]/g, character => `-${character.toLowerCase()}`)}`, value,
  ]))
}
