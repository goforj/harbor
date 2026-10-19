import { beforeEach, describe, expect, it, vi } from 'vitest'

beforeEach(() => {
  localStorage.clear()
  vi.resetModules()
})

describe('terminal appearance', () => {
  it('follows the applied app theme by default', async () => {
    const appearance = await import('./terminalAppearance')
    const { applyTheme } = await import('./theme')
    expect(appearance.terminalAppearance.value).toBe('app')
    applyTheme('dark')
    expect(appearance.terminalMode.value).toBe('dark')
    expect(appearance.terminalCSSVariables.value['--terminal-background']).toBe('#0a080d')
    applyTheme('light')
    expect(appearance.terminalMode.value).toBe('light')
    expect(appearance.terminalCSSVariables.value['--terminal-background']).toBe('#f6f8fa')
  })

  it('keeps an explicit terminal choice when app appearance changes', async () => {
    const appearance = await import('./terminalAppearance')
    const { applyTheme } = await import('./theme')
    appearance.setTerminalAppearance('dark')
    applyTheme('light')
    expect(appearance.terminalMode.value).toBe('dark')
    appearance.setTerminalAppearance('light')
    applyTheme('dark')
    expect(appearance.terminalMode.value).toBe('light')
  })

  it('restores a saved choice after the app reloads', async () => {
    const appearance = await import('./terminalAppearance')
    appearance.setTerminalAppearance('light')
    vi.resetModules()
    expect((await import('./terminalAppearance')).terminalAppearance.value).toBe('light')
  })

  it.each(['corrupt', '', '{"mode":"dark"}'])('ignores an unsupported saved value: %s', async (value) => {
    localStorage.setItem('harbor.terminal-appearance', value)
    const appearance = await import('./terminalAppearance')
    expect(appearance.terminalAppearance.value).toBe('app')
  })

  it('remains usable when local storage is blocked', async () => {
    vi.spyOn(Storage.prototype, 'getItem').mockImplementation(() => { throw new Error('blocked') })
    vi.spyOn(Storage.prototype, 'setItem').mockImplementation(() => { throw new Error('blocked') })
    const appearance = await import('./terminalAppearance')
    expect(appearance.terminalAppearance.value).toBe('app')
    expect(() => appearance.setTerminalAppearance('dark')).not.toThrow()
    expect(appearance.terminalMode.value).toBe('dark')
  })
})
