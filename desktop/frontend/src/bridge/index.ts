import type { HarborBridge } from './types'
import { createMockBridge } from './mock'
import { createWailsBridge, hasWailsBridge, hasWailsRuntime } from './wails'

export type HarborBridgeMode = 'fixture' | 'native' | 'unavailable'

export interface HarborBridgeSelection {
  bridge: HarborBridge
  mode: HarborBridgeMode
}

function createUnavailableBridge(): HarborBridge {
  const unavailable = () => Promise.reject(new Error('Harbor daemon bindings are not available in this desktop build.'))

  return {
    getSnapshot: unavailable,
    openResource: unavailable,
    subscribe: () => () => undefined,
  }
}

export function selectHarborBridge(development = import.meta.env.DEV): HarborBridgeSelection {
  if (hasWailsBridge()) {
    return { bridge: createWailsBridge(), mode: 'native' }
  }

  if (hasWailsRuntime()) {
    if (development) {
      return { bridge: createMockBridge(), mode: 'fixture' }
    }

    return { bridge: createUnavailableBridge(), mode: 'unavailable' }
  }

  return { bridge: createMockBridge(), mode: 'fixture' }
}

export function createHarborBridge(development = import.meta.env.DEV): HarborBridge {
  return selectHarborBridge(development).bridge
}

const selection = selectHarborBridge()

export const harborBridge = selection.bridge
export const harborBridgeMode = selection.mode
