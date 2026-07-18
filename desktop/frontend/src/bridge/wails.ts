import type { HarborBridge } from './types'
import type { HarborSnapshot, HarborSnapshotEvent } from '@/domain/harbor'

interface WailsAppBindings {
  OpenResource(resourceId: string): Promise<void>
  Snapshot(): Promise<HarborSnapshot>
}

interface WailsRuntime {
  ClipboardSetText(text: string): Promise<boolean>
  EventsOff(eventName: string): void
  EventsOn(eventName: string, callback: (payload: HarborSnapshotEvent) => void): () => void
}

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
  return typeof window.go?.main?.App?.Snapshot === 'function'
    && typeof window.go?.main?.App?.OpenResource === 'function'
}

export function hasWailsRuntime(): boolean {
  return typeof window.runtime === 'object' || typeof window.go === 'object'
}

export function createWailsBridge(): HarborBridge {
  const app = window.go?.main?.App
  if (!app?.Snapshot || !app.OpenResource) {
    throw new Error('Harbor desktop bindings are unavailable.')
  }

  return {
    getSnapshot: () => app.Snapshot!(),
    openResource: (resourceId) => app.OpenResource!(resourceId),
    subscribe(listener) {
      const cancel = window.runtime?.EventsOn?.('harbor:snapshot', listener)
      return () => {
        if (cancel) {
          cancel()
          return
        }
        window.runtime?.EventsOff?.('harbor:snapshot')
      }
    },
  }
}
