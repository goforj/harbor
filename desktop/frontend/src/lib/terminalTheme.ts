// terminalTheme keeps hosted logs and interactive shells on the same neutral surface and vivid ANSI palette.
export const terminalTheme = {
  background: '#0b0b0b',
  foreground: '#e2e2e2',
  chrome: '#171717',
  border: '#2e2e2e',
  black: '#262626', red: '#ff6b6b', green: '#50d890', yellow: '#f2c55c',
  blue: '#5da9ff', magenta: '#c58aff', cyan: '#4dd4d4', white: '#dedede',
  brightBlack: '#8a8a8a', brightRed: '#ff8e8e', brightGreen: '#7ee6a8', brightYellow: '#ffdb7a',
  brightBlue: '#8fc2ff', brightMagenta: '#dbafff', brightCyan: '#80e6e6', brightWhite: '#fafafa',
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
