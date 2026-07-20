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

declare global {
  interface Window {
    go?: {
      main?: {
        App?: Partial<WailsAppBindings>
      }
    }
    runtime?: Partial<WailsRuntime>
  }
}

export function hasWailsBridge(): boolean {
  const app = window.go?.main?.App
  return typeof app?.AddProject === 'function'
    && typeof app.Status === 'function'
    && typeof app.Snapshot === 'function'
    && typeof app.OpenResource === 'function'
    && typeof app.ProjectActivity === 'function'
    && typeof app.WaitProjectActivity === 'function'
    && typeof app.RemoveProject === 'function'
    && typeof app.SetupNetwork === 'function'
    && typeof app.StartProject === 'function'
    && typeof app.StopProject === 'function'
    && hasWailsEventRuntime(window.runtime)
}

export function hasWailsRuntime(): boolean {
  return window.runtime != null || window.go != null
}

export function createWailsBridge(): HarborBridge {
  const app = window.go?.main?.App
  const runtime = window.runtime
  const addProject = app?.AddProject
  const status = app?.Status
  const snapshot = app?.Snapshot
  const openResource = app?.OpenResource
  const projectActivity = app?.ProjectActivity
  const waitProjectActivity = app?.WaitProjectActivity
  const removeProject = app?.RemoveProject
  const setupNetwork = app?.SetupNetwork
  const startProject = app?.StartProject
  const stopProject = app?.StopProject
  if (typeof addProject !== 'function'
    || typeof status !== 'function'
    || typeof snapshot !== 'function'
    || typeof openResource !== 'function'
    || typeof projectActivity !== 'function'
    || typeof waitProjectActivity !== 'function'
    || typeof removeProject !== 'function'
    || typeof setupNetwork !== 'function'
    || typeof startProject !== 'function'
    || typeof stopProject !== 'function'
    || !hasWailsEventRuntime(runtime)) {
    throw new Error('Harbor desktop bindings are unavailable.')
  }

  return {
    addProject: () => addProject(),
    getStatus: () => status(),
    getSnapshot: () => snapshot(),
    getProjectActivity: (projectId, sessionId, cursor) => projectActivity(projectId, sessionId, cursor),
    waitProjectActivity: (projectId, sessionId, cursor, waitMilliseconds) => waitProjectActivity(projectId, sessionId, cursor, waitMilliseconds),
    openResource: (projectId, resourceId) => openResource(projectId, resourceId),
    removeProject: (projectId, intentId) => removeProject(projectId, intentId),
    setupNetwork: () => setupNetwork(),
    startProject: (projectId, intentId) => startProject(projectId, intentId),
    stopProject: (projectId, intentId) => stopProject(projectId, intentId),
    subscribe(listener) {
      return subscribeWailsEvent(runtime, harborWireFixture.events.snapshot, listener)
    },
    subscribeConnection(listener) {
      return subscribeWailsEvent(runtime, harborWireFixture.events.connection, listener)
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
