import type { HarborBridge } from './types'
import { harborWireFixture } from './harbor.fixture'
import type {
  WailsAppBindings,
  WailsEventName,
  WailsEventPayloads,
  WailsRuntimeEvents,
} from './harbor.fixture'

interface WailsRuntime extends WailsRuntimeEvents {
  ClipboardSetText(text: string): Promise<boolean>
}

type ReadyWailsRuntime = Partial<WailsRuntime> & WailsRuntimeEvents

interface AdditiveWailsAppBindings {
  AttachProjectTerminal(sessionId: string): ReturnType<HarborBridge['attachProjectTerminal']>
  CloseProjectTerminal(sessionId: string): ReturnType<HarborBridge['closeProjectTerminal']>
  OpenTerminalURL(url: string): ReturnType<HarborBridge['openTerminalURL']>
  RemoveOldNetworking(): ReturnType<HarborBridge['removeOldNetworking']>
  ResizeProjectTerminal(sessionId: string, columns: number, rows: number): ReturnType<HarborBridge['resizeProjectTerminal']>
  ServiceLogs(projectId: string, sessionId: string, serviceId: string, cursor: number): ReturnType<HarborBridge['getServiceLogs']>
  StartProjectTerminal(projectId: string, columns: number, rows: number): ReturnType<HarborBridge['startProjectTerminal']>
  ResourceIconURL(projectId: string, resourceId: string): ReturnType<HarborBridge['getResourceIconURL']>
  WaitServiceLogs(projectId: string, sessionId: string, serviceId: string, cursor: number, waitMilliseconds: number): ReturnType<HarborBridge['waitServiceLogs']>
  WriteProjectTerminal(sessionId: string, data: string): ReturnType<HarborBridge['writeProjectTerminal']>
}

type AvailableWailsAppBindings = Partial<WailsAppBindings> & Partial<AdditiveWailsAppBindings>

declare global {
  interface Window {
    go?: {
      main?: {
        App?: AvailableWailsAppBindings
      }
    }
    runtime?: Partial<WailsRuntime>
  }
}

export function hasWailsBridge(): boolean {
  const app = window.go?.main?.App
  return typeof app?.AddProject === 'function'
    && typeof app?.ApproveProjectRemoval === 'function'
    && typeof app.ConfirmProjectRuntimeRepair === 'function'
    && typeof app.InspectProjectRuntimeRepair === 'function'
    && typeof app.Status === 'function'
    && typeof app.Snapshot === 'function'
    && typeof app.OpenResource === 'function'
    && typeof app.ResourceIconURL === 'function'
    && typeof app.ProjectActivity === 'function'
    && typeof app.WaitProjectActivity === 'function'
    && typeof app.RemoveProject === 'function'
    && typeof app.RemoveOldNetworking === 'function'
    && typeof app.SetupNetwork === 'function'
    && typeof app.StartProject === 'function'
    && typeof app.StopProject === 'function'
    && typeof app.RestartProject === 'function'
    && hasWailsEventRuntime(window.runtime)
}

export function hasWailsRuntime(): boolean {
  return window.runtime != null || window.go != null
}

export function createWailsBridge(): HarborBridge {
  const app = window.go?.main?.App
  const runtime = window.runtime
  const addProject = app?.AddProject
  const approveProjectRemoval = app?.ApproveProjectRemoval
  const confirmProjectRuntimeRepair = app?.ConfirmProjectRuntimeRepair
  const inspectProjectRuntimeRepair = app?.InspectProjectRuntimeRepair
  const status = app?.Status
  const snapshot = app?.Snapshot
  const openResource = app?.OpenResource
  const openTerminalURL = app?.OpenTerminalURL
  const resourceIconURL = app?.ResourceIconURL
  const projectActivity = app?.ProjectActivity
  const serviceLogs = app?.ServiceLogs
  const waitServiceLogs = app?.WaitServiceLogs
  const waitProjectActivity = app?.WaitProjectActivity
  const removeProject = app?.RemoveProject
  const removeOldNetworking = app?.RemoveOldNetworking
  const setupNetwork = app?.SetupNetwork
  const startProject = app?.StartProject
  const stopProject = app?.StopProject
  const restartProject = app?.RestartProject
  const startProjectTerminal = app?.StartProjectTerminal
  const attachProjectTerminal = app?.AttachProjectTerminal
  const writeProjectTerminal = app?.WriteProjectTerminal
  const resizeProjectTerminal = app?.ResizeProjectTerminal
  const closeProjectTerminal = app?.CloseProjectTerminal
  if (typeof addProject !== 'function'
    || typeof approveProjectRemoval !== 'function'
    || typeof confirmProjectRuntimeRepair !== 'function'
    || typeof inspectProjectRuntimeRepair !== 'function'
    || typeof status !== 'function'
    || typeof snapshot !== 'function'
    || typeof openResource !== 'function'
    || typeof resourceIconURL !== 'function'
    || typeof projectActivity !== 'function'
    || typeof waitProjectActivity !== 'function'
    || typeof removeProject !== 'function'
    || typeof removeOldNetworking !== 'function'
    || typeof setupNetwork !== 'function'
    || typeof startProject !== 'function'
    || typeof stopProject !== 'function'
    || typeof restartProject !== 'function'
    || !hasWailsEventRuntime(runtime)) {
    throw new Error('Harbor desktop bindings are unavailable.')
  }

  return {
    addProject: () => addProject(),
    approveProjectRemoval: (projectId, intentId) => approveProjectRemoval(projectId, intentId),
    confirmProjectRuntimeRepair: (projectId, inspectionId, candidateFingerprint) => confirmProjectRuntimeRepair(projectId, inspectionId, candidateFingerprint),
    getStatus: () => status(),
    getSnapshot: () => snapshot(),
    getProjectActivity: (projectId, sessionId, cursor) => projectActivity(projectId, sessionId, cursor),
    getServiceLogs: typeof serviceLogs === 'function'
      ? (projectId, sessionId, serviceId, cursor) => serviceLogs(projectId, sessionId, serviceId, cursor)
      : () => Promise.reject(new Error('Service log bindings are not available in this desktop build.')),
    inspectProjectRuntimeRepair: (projectId) => inspectProjectRuntimeRepair(projectId),
    waitProjectActivity: (projectId, sessionId, cursor, waitMilliseconds) => waitProjectActivity(projectId, sessionId, cursor, waitMilliseconds),
    waitServiceLogs: typeof waitServiceLogs === 'function'
      ? (projectId, sessionId, serviceId, cursor, waitMilliseconds) => waitServiceLogs(projectId, sessionId, serviceId, cursor, waitMilliseconds)
      : () => Promise.reject(new Error('Service log bindings are not available in this desktop build.')),
    openResource: (projectId, resourceId) => openResource(projectId, resourceId),
    openTerminalURL: typeof openTerminalURL === 'function'
      ? (url) => openTerminalURL(url)
      : () => Promise.reject(new Error('Terminal link bindings are not available in this desktop build.')),
    getResourceIconURL: (projectId, resourceId) => resourceIconURL(projectId, resourceId),
    removeProject: (projectId, intentId) => removeProject(projectId, intentId),
    removeOldNetworking: () => removeOldNetworking(),
    setupNetwork: () => setupNetwork(),
    startProject: (projectId, intentId) => startProject(projectId, intentId),
    stopProject: (projectId, intentId) => stopProject(projectId, intentId),
    restartProject: (projectId, intentId) => restartProject(projectId, intentId),
    startProjectTerminal: typeof startProjectTerminal === 'function'
      ? (projectId, columns, rows) => startProjectTerminal(projectId, columns, rows)
      : () => Promise.reject(new Error('Interactive terminal bindings are not available in this desktop build.')),
    attachProjectTerminal: typeof attachProjectTerminal === 'function'
      ? (sessionId) => attachProjectTerminal(sessionId)
      : () => Promise.reject(new Error('Interactive terminal bindings are not available in this desktop build.')),
    writeProjectTerminal: typeof writeProjectTerminal === 'function'
      ? (sessionId, data) => writeProjectTerminal(sessionId, data)
      : () => Promise.reject(new Error('Interactive terminal bindings are not available in this desktop build.')),
    resizeProjectTerminal: typeof resizeProjectTerminal === 'function'
      ? (sessionId, columns, rows) => resizeProjectTerminal(sessionId, columns, rows)
      : () => Promise.reject(new Error('Interactive terminal bindings are not available in this desktop build.')),
    closeProjectTerminal: typeof closeProjectTerminal === 'function'
      ? (sessionId) => closeProjectTerminal(sessionId)
      : () => Promise.reject(new Error('Interactive terminal bindings are not available in this desktop build.')),
    subscribe(listener) {
      return subscribeWailsEvent(runtime, harborWireFixture.events.snapshot, listener)
    },
    subscribeConnection(listener) {
      return subscribeWailsEvent(runtime, harborWireFixture.events.connection, listener)
    },
    subscribeProjectTerminal(listener) {
      return subscribeWailsEvent(runtime, harborWireFixture.events.project_terminal, listener)
    },
  }
}

function hasWailsEventRuntime(runtime: Partial<WailsRuntime> | undefined): runtime is ReadyWailsRuntime {
  return typeof runtime?.EventsOn === 'function'
    && typeof runtime.EventsOff === 'function'
}

function subscribeWailsEvent<Name extends WailsEventName>(
  runtime: WailsRuntimeEvents,
  eventName: Name,
  listener: (payload: WailsEventPayloads[Name]) => void,
): () => void {
  const cancel = runtime.EventsOn(eventName, listener)
  return () => {
    if (cancel) {
      cancel()
      return
    }
    runtime.EventsOff(eventName)
  }
}
