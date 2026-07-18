import type { HarborBridge } from './types'
import { createMockBridge } from './mock'
import { createWailsBridge, hasWailsBridge, hasWailsRuntime } from './wails'

function createUnavailableBridge(): HarborBridge {
  const unavailable = () => Promise.reject(new Error('Harbor daemon bindings are not available in this desktop build.'))

  return {
    getSnapshot: unavailable,
    openResource: unavailable,
    subscribe: () => () => undefined,
  }
}

export function createHarborBridge(): HarborBridge {
  if (hasWailsBridge()) {
    return createWailsBridge()
  }

  if (hasWailsRuntime()) {
    return createUnavailableBridge()
  }

  return createMockBridge()
}

export const harborBridge = createHarborBridge()
