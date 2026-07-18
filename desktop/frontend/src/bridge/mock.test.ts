import { describe, expect, it, vi } from 'vitest'
import { createMockBridge, mockSnapshot } from './mock'

describe('Harbor mock bridge', () => {
  it('returns independent snapshots with representative Harbor state', async () => {
    const bridge = createMockBridge()
    const first = await bridge.getSnapshot()
    const second = await bridge.getSnapshot()

    expect(first.sequence).toBe(42)
    expect(first.projects).toHaveLength(4)
    expect(first.services).toHaveLength(4)
    expect(first.projects.some((project) => project.status === 'ready')).toBe(true)
    expect(first.projects.some((project) => project.status === 'failed')).toBe(true)

    first.projects[0].name = 'changed by a consumer'
    expect(second.projects[0].name).toBe('orders-api')
    expect(mockSnapshot().projects[0].name).toBe('orders-api')
  })

  it('opens known resources without giving the new page an opener', async () => {
    const open = vi.spyOn(window, 'open').mockImplementation(() => null)

    await createMockBridge().openResource('orders-api-reference')

    expect(open).toHaveBeenCalledWith(
      'https://orders.test/swagger',
      '_blank',
      'noopener,noreferrer',
    )
  })

  it('rejects unknown resources instead of opening an arbitrary URL', async () => {
    const open = vi.spyOn(window, 'open').mockImplementation(() => null)

    await expect(createMockBridge().openResource('missing-resource')).rejects.toThrow(
      'Unknown resource: missing-resource',
    )
    expect(open).not.toHaveBeenCalled()
  })
})
