import { afterEach, describe, expect, it, vi } from 'vitest'
import { harborWireFixture } from './harbor.fixture'
import { createHarborBridge, selectHarborBridge } from './index'
import { createWailsBridge } from './wails'

function installAppBindings() {
  const AddProject = vi.fn().mockResolvedValue({ canceled: true })
  const ConfirmProjectRuntimeRepair = vi.fn().mockResolvedValue(harborWireFixture.project_runtime_repair_confirmation)
  const InspectProjectRuntimeRepair = vi.fn().mockResolvedValue(harborWireFixture.project_runtime_repair_inspection)
  const Status = vi.fn().mockResolvedValue({ state: 'ready' })
  const Snapshot = vi.fn().mockResolvedValue({ schema_version: 1, sequence: 7 })
  const OpenResource = vi.fn().mockResolvedValue(undefined)
  const ProjectActivity = vi.fn().mockResolvedValue(harborWireFixture.project_activity)
  const WaitProjectActivity = vi.fn().mockResolvedValue(harborWireFixture.project_activity)
  const RemoveProject = vi.fn().mockResolvedValue(harborWireFixture.remove_project)
  const SetupNetwork = vi.fn().mockResolvedValue({
    operation: {
      id: 'operation-network-setup',
      intent_id: 'intent-network-setup',
      kind: 'network.setup',
      state: 'succeeded',
      phase: 'completed',
      requested_at: '2026-07-19T12:00:00Z',
      started_at: '2026-07-19T12:00:01Z',
      finished_at: '2026-07-19T12:00:02Z',
    },
    revision: 43,
  })
  const StartProject = vi.fn().mockResolvedValue(harborWireFixture.start_project)
  const StopProject = vi.fn().mockResolvedValue(harborWireFixture.stop_project)
  window.go = { main: { App: { AddProject, ConfirmProjectRuntimeRepair, InspectProjectRuntimeRepair, Status, Snapshot, OpenResource, ProjectActivity, WaitProjectActivity, RemoveProject, SetupNetwork, StartProject, StopProject } } }
  return { AddProject, ConfirmProjectRuntimeRepair, InspectProjectRuntimeRepair, OpenResource, ProjectActivity, WaitProjectActivity, RemoveProject, SetupNetwork, Snapshot, StartProject, Status, StopProject }
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

  it('never presents fixtures when Wails development bindings are not ready', async () => {
    window.runtime = {}
    const selection = selectHarborBridge(true)

    expect(selection.mode).toBe('unavailable')
    await expect(selection.bridge.getSnapshot()).rejects.toThrow('Harbor daemon bindings are not available')
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

  it.each(['ConfirmProjectRuntimeRepair', 'InspectProjectRuntimeRepair', 'ProjectActivity', 'WaitProjectActivity', 'SetupNetwork', 'StartProject', 'StopProject'] as const)('does not select native mode without the %s binding', async (method) => {
    installAppBindings()
    delete window.go?.main?.App?.[method]
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
    const { AddProject, ConfirmProjectRuntimeRepair, InspectProjectRuntimeRepair, OpenResource, ProjectActivity, WaitProjectActivity, RemoveProject, SetupNetwork, StartProject, StopProject } = installAppBindings()
    installEventRuntime()

    for (const development of [true, false]) {
      const selection = selectHarborBridge(development)
      expect(selection.mode).toBe('native')
      await selection.bridge.getStatus()
      await selection.bridge.getSnapshot()
      await selection.bridge.getProjectActivity('orders-api', 'session-orders-api', 4)
      await selection.bridge.inspectProjectRuntimeRepair('billing')
      await selection.bridge.confirmProjectRuntimeRepair('billing', 'inspection-1', 'fingerprint-1')
      await selection.bridge.waitProjectActivity('orders-api', 'session-orders-api', 4, 20_000)
      await selection.bridge.addProject()
      await selection.bridge.openResource('orders', 'application')
      await selection.bridge.removeProject('orders', 'desktop-remove-orders')
      await selection.bridge.setupNetwork()
      await selection.bridge.startProject('reports', 'desktop-start-reports')
      await selection.bridge.stopProject('orders', 'desktop-stop-orders')
    }

    expect(OpenResource).toHaveBeenCalledWith('orders', 'application')
    expect(ProjectActivity).toHaveBeenCalledWith('orders-api', 'session-orders-api', 4)
    expect(InspectProjectRuntimeRepair).toHaveBeenCalledWith('billing')
    expect(ConfirmProjectRuntimeRepair).toHaveBeenCalledWith('billing', 'inspection-1', 'fingerprint-1')
    expect(WaitProjectActivity).toHaveBeenCalledWith('orders-api', 'session-orders-api', 4, 20_000)
    expect(RemoveProject).toHaveBeenCalledWith('orders', 'desktop-remove-orders')
    expect(SetupNetwork).toHaveBeenCalledTimes(2)
    expect(StartProject).toHaveBeenCalledWith('reports', 'desktop-start-reports')
    expect(StopProject).toHaveBeenCalledWith('orders', 'desktop-stop-orders')
    expect(AddProject).toHaveBeenCalledTimes(2)
  })

  it('keeps the authoritative native bridge when additive service-log bindings are absent', async () => {
    installAppBindings()
    installEventRuntime()

    const selection = selectHarborBridge(true)

    expect(selection.mode).toBe('native')
    await expect(selection.bridge.getSnapshot()).resolves.toMatchObject({ sequence: 7 })
    await expect(selection.bridge.getServiceLogs('orders-api', '', 'mysql', 0)).rejects.toThrow('Service log bindings are not available')
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
