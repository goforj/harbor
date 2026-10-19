import { computed, readonly, ref } from 'vue'
import { appliedTheme } from './theme'
import { terminalThemes, terminalThemeVariables } from './terminalTheme'

export type TerminalAppearance = 'app' | 'dark' | 'light'
const storageKey = 'harbor.terminal-appearance'

// readTerminalAppearance admits only supported preferences from optional local storage.
function readTerminalAppearance(): TerminalAppearance {
  try {
    const value = localStorage.getItem(storageKey)
    if (value === 'dark' || value === 'light') return value
  }
  catch { /* Appearance still works for the current session when storage is unavailable. */ }
  return 'app'
}

const preference = ref<TerminalAppearance>(readTerminalAppearance())
export const terminalAppearance = readonly(preference)
export const terminalMode = computed(() => preference.value === 'app' ? appliedTheme.value : preference.value)
export const activeTerminalTheme = computed(() => terminalThemes[terminalMode.value])
export const terminalCSSVariables = computed(() => terminalThemeVariables(activeTerminalTheme.value))

// setTerminalAppearance updates every terminal without reconnecting shells or restarting projects.
export function setTerminalAppearance(value: TerminalAppearance) {
  preference.value = value
  try { localStorage.setItem(storageKey, value) }
  catch { /* A session-local preference is preferable to failing a visual change. */ }
}
