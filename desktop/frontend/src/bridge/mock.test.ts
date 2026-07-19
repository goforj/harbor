import { describe, expect, it, vi } from 'vitest'
import { harborWireFixture } from './harbor.fixture'
import { createMockBridge, mockSnapshot, mockStatus } from './mock'

describe('Harbor mock bridge', () => {
  it('returns independent values using the exact daemon wire shape', async () => {
    const bridge = createMockBridge()
    const first = await bridge.getSnapshot()
    const second = await bridge.getSnapshot()

    expect(first).toMatchObject({
      schema_version: 1,
      sequence: 42,
      captured_at: '2026-07-18T14:35:20Z',
    })
    expect(first.recent_resource_ids).toContainEqual({ project_id: 'orders-api', resource_id: 'application' })
    expect(first.projects).toHaveLength(4)
    expect(first.projects.some((project) => project.state === 'ready')).toBe(true)
    expect(first.projects.some((project) => project.state === 'failed')).toBe(true)
    expect(first.projects[0].apps[0]).toEqual({ id: 'web', name: 'Web', state: 'ready', active: true, required: true })
    expect(first).toEqual(harborWireFixture.snapshot)
    expect(await bridge.getStatus()).toEqual(harborWireFixture.status)
    expect(harborWireFixture.connection_payloads).toEqual({
      connecting: { state: 'connecting' },
      connected: { state: 'connected' },
      disconnected: { state: 'disconnected' },
    })
    expect(harborWireFixture.methods).toEqual({
      add_project: 'AddProject',
      open_resource: 'OpenResource',
      remove_project: 'RemoveProject',
      snapshot: 'Snapshot',
      status: 'Status',
    })
    expect(harborWireFixture.events).toEqual({
      connection: 'harbor:connection',
      snapshot: 'harbor:snapshot',
    })
    expect(harborWireFixture.terminal_operation).toMatchObject({
      state: 'failed',
      problem: { code: 'service_unavailable', retryable: true },
      started_at: expect.any(String),
      finished_at: expect.any(String),
    })

    first.projects[0].name = 'changed by a consumer'
    expect(second.projects[0].name).toBe('Orders API')
    expect(mockSnapshot().projects[0].name).toBe('Orders API')
    expect(mockStatus()).toMatchObject({ snapshot_schema_version: 1, protocol: { major: 1, minor: 0 } })
  })

  it('registers the generated pending project once and replays it without duplication', async () => {
    const bridge = createMockBridge()

    const created = await bridge.addProject()
    const replayed = await bridge.addProject()
    const snapshot = await bridge.getSnapshot()

    expect(created).toMatchObject({
      canceled: false,
      registration: {
        created: true,
        project: { id: 'inventory', name: 'Inventory', state: 'stopped' },
      },
    })
    expect(replayed.registration?.created).toBe(false)
    expect(snapshot.sequence).toBe(43)
    expect(snapshot.projects.filter((project) => project.id === 'inventory')).toHaveLength(1)
  })

  it('removes an inert project once and replays the client intent without another mutation', async () => {
    const bridge = createMockBridge()

    const removed = await bridge.removeProject('reports', 'desktop-remove-reports')
    const replayed = await bridge.removeProject('reports', 'desktop-remove-reports')
    const snapshot = await bridge.getSnapshot()

    expect(removed).toMatchObject({
      revision: 43,
      operation: {
        intent_id: 'desktop-remove-reports',
        project_id: 'reports',
        state: 'succeeded',
      },
    })
    expect(replayed).toEqual(removed)
    expect(snapshot.projects.some((project) => project.id === 'reports')).toBe(false)
    expect(snapshot.operations.some((operation) => operation.project_id === 'reports')).toBe(false)
    expect(snapshot.operations.every((operation) => ['queued', 'running', 'requires_approval'].includes(operation.state))).toBe(true)
    expect(snapshot.sequence).toBe(43)
  })

  it('reports required approval without removing an active project or claiming desktop approval support', async () => {
    const bridge = createMockBridge()

    const removal = await bridge.removeProject('orders-api', 'desktop-remove-orders')
    const snapshot = await bridge.getSnapshot()

    expect(removal).toMatchObject({
      operation: {
        project_id: 'orders-api',
        state: 'requires_approval',
        phase: 'awaiting_host_approval',
      },
    })
    expect(snapshot.projects.some((project) => project.id === 'orders-api')).toBe(true)
  })

  it('opens a known project-scoped resource without giving the new page an opener', async () => {
    const open = vi.spyOn(window, 'open').mockImplementation(() => null)

    await createMockBridge().openResource('orders-api', 'api-reference')

    expect(open).toHaveBeenCalledWith('https://orders.test/swagger', '_blank', 'noopener,noreferrer')
  })

  it('rejects unknown resources instead of opening an arbitrary URL', async () => {
    const open = vi.spyOn(window, 'open').mockImplementation(() => null)

    await expect(createMockBridge().openResource('orders-api', 'missing')).rejects.toThrow(
      'Unknown resource: orders-api/missing',
    )
    expect(open).not.toHaveBeenCalled()
  })
})
