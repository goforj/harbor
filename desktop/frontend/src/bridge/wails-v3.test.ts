import { afterEach, describe, expect, it, vi } from 'vitest'
import { harborWireFixture } from './harbor.fixture'
import { bootstrapWailsV3, installWailsV3Bridge } from './wails-v3'

const native = vi.hoisted(() => ({
  Call: { ByName: vi.fn() },
  Events: { On: vi.fn(), Off: vi.fn() },
  Clipboard: { SetText: vi.fn() },
}))
vi.mock('@wailsio/runtime', () => native)

afterEach(() => {
  delete window.go
  delete window.runtime
  delete (window as Window & { _wails?: unknown })._wails
  vi.resetAllMocks()
  vi.unstubAllGlobals()
})

describe('Wails v3 transport', () => {
  it('exposes exactly the generated Go contract and forwards arguments and failures', async () => {
    installWailsV3Bridge(native as never)
    expect(Object.keys(window.go!.main!.App!).sort()).toEqual(Object.values(harborWireFixture.methods).sort())
    const response = { outcome: 'accepted' }
    native.Call.ByName.mockResolvedValueOnce(response)
    await expect(window.go!.main!.App!.StartProject!('project', 'intent')).resolves.toBe(response)
    expect(native.Call.ByName).toHaveBeenCalledWith('main.App.StartProject', 'project', 'intent')
    native.Call.ByName.mockRejectedValueOnce(new Error('permission denied'))
    await expect(window.go!.main!.App!.Snapshot!()).rejects.toThrow('permission denied')
  })

  it('unwraps v3 events and preserves subscription cleanup', () => {
    const dispose = vi.fn()
    native.Events.On.mockReturnValue(dispose)
    installWailsV3Bridge(native as never)
    const callback = vi.fn()
    expect(window.runtime!.EventsOn!('harbor:connection', callback)).toBe(dispose)
    const listener = native.Events.On.mock.calls[0]![1]
    const payload = { state: 'connected' }
    listener({ name: 'harbor:connection', data: payload, sender: 'native' })
    expect(callback).toHaveBeenCalledWith(payload)
    window.runtime!.EventsOff!('harbor:connection')
    expect(native.Events.Off).toHaveBeenCalledWith('harbor:connection')
  })

  it('reports clipboard success and propagates native failures', async () => {
    installWailsV3Bridge(native as never)
    native.Clipboard.SetText.mockResolvedValueOnce(undefined)
    await expect(window.runtime!.ClipboardSetText!('hello')).resolves.toBe(true)
    expect(native.Clipboard.SetText).toHaveBeenCalledWith('hello')
    native.Clipboard.SetText.mockRejectedValueOnce(new Error('clipboard unavailable'))
    await expect(window.runtime!.ClipboardSetText!('hello')).rejects.toThrow('clipboard unavailable')
  })

  it('leaves ordinary browser fixtures independent of the native bridge', async () => {
    await bootstrapWailsV3()
    expect(window.go).toBeUndefined()
  })

  it.each(['wails://localhost/', 'https://wails.localhost/'])('initializes packaged pages at %s before a runtime marker exists', async url => {
    vi.stubGlobal('location', new URL(url))
    await bootstrapWailsV3()
    expect(window.go!.main!.App).toBeDefined()
  })

  it('initializes native pages through the injected marker, including the Vite proxy', async () => {
    ;(window as Window & { _wails?: unknown })._wails = {}
    await bootstrapWailsV3()
    expect(window.go!.main!.App).toBeDefined()
  })
})
