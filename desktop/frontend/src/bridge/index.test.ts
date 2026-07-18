import { afterEach, describe, expect, it } from 'vitest'
import { createHarborBridge } from './index'

describe('Harbor bridge selection', () => {
  afterEach(() => {
    delete window.go
    delete window.runtime
  })

  it('uses fixtures in a normal browser development session', async () => {
    const bridge = createHarborBridge()

    await expect(bridge.getSnapshot()).resolves.toMatchObject({ sequence: 42 })
  })

  it('does not present fixture state when native bindings are missing', async () => {
    window.runtime = {}
    const bridge = createHarborBridge()

    await expect(bridge.getSnapshot()).rejects.toThrow('Harbor daemon bindings are not available')
  })
})
