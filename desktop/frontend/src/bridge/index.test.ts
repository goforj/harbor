import { afterEach, describe, expect, it, vi } from 'vitest'
import { harborWireFixture } from './harbor.fixture'
import { createHarborBridge, selectHarborBridge } from './index'
import { createWailsBridge } from './wails'

function installAppBindings() {
  const AddProject = vi.fn().mockResolvedValue({ canceled: true })
  const Status = vi.fn().mockResolvedValue({ state: 'ready' })
  const Snapshot = vi.fn().mockResolvedValue({ schema_version: 1, sequence: 7 })
  const OpenResource = vi.fn().mockResolvedValue(undefined)
  const RemoveProject = vi.fn().mockResolvedValue(harborWireFixture.remove_project)
  window.go = { main: { App: { AddProject, Status, Snapshot, OpenResource, RemoveProject } } }
  return { AddProject, OpenResource, RemoveProject, Snapshot, Status }
}

function installEventRuntime() {
  const cancel = vi.fn()
  const EventsOn = vi.fn(() => cancel)
  const EventsOff = vi.fn()
  window.runtime = { EventsOn, EventsOff }
  return { cancel, EventsOff, EventsOn }
}

describe('Harbor bridge selection', () => {
  afterEach(() => {
    delete window.go
    delete window.runtime
    vi.unstubAllEnvs()
  })

  it('uses fixtures in a normal browser development session', async () => {
    const bridge = createHarborBridge()

    await expect(bridge.getSnapshot()).resolves.toMatchObject({ schema_version: 1, sequence: 42 })
    await expect(bridge.getStatus()).resolves.toMatchObject({ state: 'ready', sequence: 42 })
  })

  it('uses visibly identified fixtures when Wails development bindings are not ready', async () => {
    window.runtime = {}
    const selection = selectHarborBridge(true)

    expect(selection.mode).toBe('fixture')
    await expect(selection.bridge.getSnapshot()).resolves.toMatchObject({ sequence: 42 })
  })

  it('does not present fixture state in a production Wails build with missing bindings', async () => {
    window.runtime = {}
    const selection = selectHarborBridge(false, false)

    expect(selection.mode).toBe('unavailable')
    await expect(selection.bridge.getSnapshot()).rejects.toThrow('Harbor daemon bindings are not available')
  })

  it('does not select native mode when App bindings have no event runtime', async () => {
    installAppBindings()
    const selection = selectHarborBridge(false, false)

    expect(selection.mode).toBe('unavailable')
    await expect(selection.bridge.getSnapshot()).rejects.toThrow('Harbor daemon bindings are not available')
  })

  it('does not select native mode without the project removal binding', async () => {
    installAppBindings()
    delete window.go?.main?.App?.RemoveProject
    installEventRuntime()

    const selection = selectHarborBridge(false, false)

    expect(selection.mode).toBe('unavailable')
    await expect(selection.bridge.getSnapshot()).rejects.toThrow('Harbor daemon bindings are not available')
  })

  it.each([
    { name: 'EventsOn', runtime: { EventsOff: vi.fn() } },
    { name: 'EventsOff', runtime: { EventsOn: vi.fn(() => vi.fn()) } },
  ])('does not select native mode when the event runtime is missing $name', async ({ runtime }) => {
    installAppBindings()
    window.runtime = runtime
    const selection = selectHarborBridge(false, false)

    expect(selection.mode).toBe('unavailable')
    await expect(selection.bridge.getSnapshot()).rejects.toThrow('Harbor daemon bindings are not available')
  })

  it('rejects direct native bridge construction without the complete event runtime', () => {
    installAppBindings()
    window.runtime = { EventsOn: vi.fn(() => vi.fn()) }

    expect(() => createWailsBridge()).toThrow('Harbor desktop bindings are unavailable')
  })

  it('does not present fixture state in a production browser without an explicit fixture flag', async () => {
    const selection = selectHarborBridge(false, false)

    expect(selection.mode).toBe('unavailable')
    await expect(selection.bridge.getSnapshot()).rejects.toThrow('Harbor daemon bindings are not available')
  })

  it('allows a production browser fixture only through its explicit environment flag', async () => {
    vi.stubEnv('VITE_HARBOR_BROWSER_FIXTURE', 'true')
    const selection = selectHarborBridge(false)

    expect(selection.mode).toBe('fixture')
    await expect(selection.bridge.getSnapshot()).resolves.toMatchObject({ sequence: 42 })
  })

  it('does not let the browser fixture flag hide an incomplete production Wails runtime', async () => {
    installAppBindings()
    window.runtime = { EventsOff: vi.fn() }
    const selection = selectHarborBridge(false, true)

    expect(selection.mode).toBe('unavailable')
    await expect(selection.bridge.getSnapshot()).rejects.toThrow('Harbor daemon bindings are not available')
  })

  it('uses native bindings in Wails development and packaged builds', async () => {
    const { AddProject, OpenResource, RemoveProject } = installAppBindings()
    installEventRuntime()

    for (const development of [true, false]) {
      const selection = selectHarborBridge(development)
      expect(selection.mode).toBe('native')
      await selection.bridge.getStatus()
      await selection.bridge.getSnapshot()
      await selection.bridge.addProject()
      await selection.bridge.openResource('orders', 'application')
      await selection.bridge.removeProject('orders', 'desktop-remove-orders')
    }

    expect(OpenResource).toHaveBeenCalledWith('orders', 'application')
    expect(RemoveProject).toHaveBeenCalledWith('orders', 'desktop-remove-orders')
    expect(AddProject).toHaveBeenCalledTimes(2)
  })

  it('subscribes to authoritative snapshot payloads and cancels the exact Wails listener', () => {
    installAppBindings()
    const { cancel, EventsOn } = installEventRuntime()
    const listener = vi.fn()

    const unsubscribe = selectHarborBridge(false).bridge.subscribe(listener)
    unsubscribe()

    expect(EventsOn).toHaveBeenCalledWith('harbor:snapshot', listener)
    expect(cancel).toHaveBeenCalledOnce()
  })

  it('subscribes to typed connection lifecycle payloads and cancels the exact Wails listener', () => {
    installAppBindings()
    const { cancel, EventsOn } = installEventRuntime()
    const listener = vi.fn()

    const unsubscribe = selectHarborBridge(false).bridge.subscribeConnection(listener)
    unsubscribe()

    expect(EventsOn).toHaveBeenCalledWith('harbor:connection', listener)
    expect(cancel).toHaveBeenCalledOnce()
  })
})
