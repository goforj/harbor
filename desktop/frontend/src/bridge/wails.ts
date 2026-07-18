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
  return typeof app?.Status === 'function'
    && typeof app.Snapshot === 'function'
    && typeof app.OpenResource === 'function'
    && hasWailsEventRuntime(window.runtime)
}

export function hasWailsRuntime(): boolean {
  return window.runtime != null || window.go != null
}

export function createWailsBridge(): HarborBridge {
  const app = window.go?.main?.App
  const runtime = window.runtime
  const status = app?.Status
  const snapshot = app?.Snapshot
  const openResource = app?.OpenResource
  if (typeof status !== 'function'
    || typeof snapshot !== 'function'
    || typeof openResource !== 'function'
    || !hasWailsEventRuntime(runtime)) {
    throw new Error('Harbor desktop bindings are unavailable.')
  }

  return {
    getStatus: () => status(),
    getSnapshot: () => snapshot(),
    openResource: (projectId, resourceId) => openResource(projectId, resourceId),
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
