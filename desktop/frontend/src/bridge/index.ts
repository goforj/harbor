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
    addProject: unavailable,
    approveProjectRemoval: unavailable,
    confirmProjectRuntimeRepair: unavailable,
    getStatus: unavailable,
    getSnapshot: unavailable,
    getProjectActivity: unavailable,
    getServiceLogs: unavailable,
    inspectProjectRuntimeRepair: unavailable,
    waitProjectActivity: unavailable,
    waitServiceLogs: unavailable,
    openResource: unavailable,
    openTerminalURL: unavailable,
    getResourceIconURL: unavailable,
    removeProject: unavailable,
    setupNetwork: unavailable,
    startProject: unavailable,
    stopProject: unavailable,
    restartProject: unavailable,
    subscribe: () => () => undefined,
    subscribeConnection: () => () => undefined,
  }
}

export function selectHarborBridge(
  development = import.meta.env.DEV,
  browserFixture = import.meta.env.VITE_HARBOR_BROWSER_FIXTURE === 'true',
): HarborBridgeSelection {
  if (hasWailsBridge()) {
    return { bridge: createWailsBridge(), mode: 'native' }
  }

  // A native desktop must never display plausible fixture state while bindings are rebuilding or unavailable.
  if (hasWailsRuntime()) {
    return { bridge: createUnavailableBridge(), mode: 'unavailable' }
  }

  if (development) {
    return { bridge: createMockBridge(), mode: 'fixture' }
  }

  if (browserFixture && !hasWailsRuntime()) {
    return { bridge: createMockBridge(), mode: 'fixture' }
  }

  return { bridge: createUnavailableBridge(), mode: 'unavailable' }
}

export function createHarborBridge(
  development = import.meta.env.DEV,
  browserFixture = import.meta.env.VITE_HARBOR_BROWSER_FIXTURE === 'true',
): HarborBridge {
  return selectHarborBridge(development, browserFixture).bridge
}

const selection = selectHarborBridge()

export const harborBridge = selection.bridge
export const harborBridgeMode = selection.mode
