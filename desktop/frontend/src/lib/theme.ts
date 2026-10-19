import { readonly, ref } from 'vue'

export type ThemePreference = 'light' | 'dark' | 'system'
export type AppliedTheme = Exclude<ThemePreference, 'system'>

const storageKey = 'theme'
const appliedThemeState = ref<AppliedTheme>('light')
const themePreferenceState = ref<ThemePreference>('system')

export const currentThemePreference = readonly(themePreferenceState)

export const appliedTheme = readonly(appliedThemeState)

function systemPrefersDark() {
  return window.matchMedia('(prefers-color-scheme: dark)').matches
}

function resolveTheme(preference: ThemePreference): AppliedTheme {
  if (preference === 'system') {
    return systemPrefersDark() ? 'dark' : 'light'
  }
  return preference
}

export function themePreference(): ThemePreference {
  try {
    const stored = localStorage.getItem(storageKey)
    if (stored === 'light' || stored === 'dark' || stored === 'system') {
      return stored
    }
    return 'system'
  } catch {
    return themePreferenceState.value
  }
}

export function applyTheme(preference: ThemePreference = themePreference()) {
  themePreferenceState.value = preference
  const theme = resolveTheme(preference)
  appliedThemeState.value = theme
  document.documentElement.classList.toggle('dark', theme === 'dark')
  document.documentElement.style.colorScheme = theme
}

export function setThemePreference(preference: ThemePreference) {
  try {
    localStorage.setItem(storageKey, preference)
  } catch {
    // Session preferences remain available when the WebView blocks local storage.
  }
  applyTheme(preference)
}

export function watchSystemTheme() {
  const media = window.matchMedia('(prefers-color-scheme: dark)')
  const listener = () => {
    if (themePreference() === 'system') {
      applyTheme('system')
    }
  }
  media.addEventListener('change', listener)
  return () => media.removeEventListener('change', listener)
}
