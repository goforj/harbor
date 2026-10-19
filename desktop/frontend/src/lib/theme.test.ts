import { beforeEach, describe, expect, it, vi } from 'vitest'

beforeEach(() => {
  localStorage.clear()
  vi.resetModules()
})

describe('shared app appearance preference', () => {
  it('restores a saved preference and publishes changes when the resolved color stays the same', async () => {
    localStorage.setItem('theme', 'dark')
    const theme = await import('./theme')
    theme.applyTheme()
    expect(theme.currentThemePreference.value).toBe('dark')
    theme.setThemePreference('system')
    expect(theme.currentThemePreference.value).toBe('system')
    expect(localStorage.getItem('theme')).toBe('system')
    theme.setThemePreference('light')
    expect(theme.appliedTheme.value).toBe('light')
    expect(document.documentElement.classList.contains('dark')).toBe(false)
  })

  it('falls back to system for an invalid stored value', async () => {
    localStorage.setItem('theme', 'invalid')
    const theme = await import('./theme')
    expect(theme.themePreference()).toBe('system')
  })

  it('retains session appearance when storage reads and writes are blocked', async () => {
    vi.spyOn(Storage.prototype, 'getItem').mockImplementation(() => { throw new Error('blocked') })
    vi.spyOn(Storage.prototype, 'setItem').mockImplementation(() => { throw new Error('blocked') })
    const theme = await import('./theme')
    expect(theme.themePreference()).toBe('system')
    expect(() => theme.setThemePreference('dark')).not.toThrow()
    expect(theme.currentThemePreference.value).toBe('dark')
    expect(theme.appliedTheme.value).toBe('dark')
    expect(theme.themePreference()).toBe('dark')
  })
})
