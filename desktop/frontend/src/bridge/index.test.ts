import { afterEach, describe, expect, it } from 'vitest'
import { createHarborBridge, selectHarborBridge } from './index'

describe('Harbor bridge selection', () => {
  afterEach(() => {
    delete window.go
    delete window.runtime
  })

  it('uses fixtures in a normal browser development session', async () => {
    const bridge = createHarborBridge()

    await expect(bridge.getSnapshot()).resolves.toMatchObject({ sequence: 42 })
  })

  it('uses visibly identified fixtures when Wails development bindings are not ready', async () => {
    window.runtime = {}
    const selection = selectHarborBridge(true)

    expect(selection.mode).toBe('fixture')
    await expect(selection.bridge.getSnapshot()).resolves.toMatchObject({ sequence: 42 })
  })

  it('does not present fixture state in a production Wails build with missing bindings', async () => {
    window.runtime = {}
    const selection = selectHarborBridge(false)

    expect(selection.mode).toBe('unavailable')
    await expect(selection.bridge.getSnapshot()).rejects.toThrow('Harbor daemon bindings are not available')
  })
})
