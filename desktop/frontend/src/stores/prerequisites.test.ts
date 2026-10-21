import { createPinia, setActivePinia } from 'pinia'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { harborBridge } from '@/bridge'
import { mockSnapshot } from '@/bridge/mock'
import { harborWireFixture } from '@/bridge/harbor.fixture'
import { useHarborStore } from './harbor'
import type { PrerequisiteReport } from '@/domain/harbor'

const ready: PrerequisiteReport = { provider_id: 'goforj', project_id: 'reports', checks: [{ id: 'go', name: 'Go', status: 'ready', detail: 'Available' }] }

describe('project Start prerequisites', () => {
  beforeEach(() => {
    setActivePinia(createPinia())
    vi.spyOn(harborBridge, 'getSnapshot').mockImplementation(async () => mockSnapshot())
  })

  it('blocks a new Start, keeps other projects independent, and rechecks a repaired tool', async () => {
    const store = useHarborStore()
    store.$patch({ snapshot: mockSnapshot(), connectionState: 'connected', snapshotStale: false })
    const check = vi.spyOn(harborBridge, 'getProjectPrerequisites').mockResolvedValueOnce({ ...ready, checks: [{ id: 'go', name: 'Go', status: 'missing', detail: 'Install Go' }] }).mockResolvedValue(ready)
    const start = vi.spyOn(harborBridge, 'startProject')
    expect(await store.startProject('reports')).toBeNull()
    expect(start).not.toHaveBeenCalled()
    expect(store.projectPrerequisites.reports?.checks[0]?.status).toBe('missing')
    expect(store.projectLifecycleBusyFor('reports')).toBe(false)
    await store.checkProjectPrerequisites('reports')
    expect(store.projectPrerequisites.reports?.checks[0]?.status).toBe('ready')
    expect(await store.startProject('reports')).not.toBeNull()
    expect(check).toHaveBeenCalledTimes(3)
    expect(start).toHaveBeenCalledTimes(1)
  })

  it('rejects crossed results and read failures without sending Start', async () => {
    const store = useHarborStore()
    store.$patch({ snapshot: mockSnapshot(), connectionState: 'connected', snapshotStale: false })
    vi.spyOn(harborBridge, 'getProjectPrerequisites').mockResolvedValueOnce({ ...ready, project_id: 'billing' }).mockRejectedValueOnce(new Error('Readiness unavailable'))
    const start = vi.spyOn(harborBridge, 'startProject')
    expect(await store.startProject('reports')).toBeNull()
    expect(store.projectPrerequisiteErrors.reports).toContain('invalid prerequisite')
    expect(await store.startProject('reports')).toBeNull()
    expect(store.projectPrerequisiteErrors.reports).toBe('Readiness unavailable')
    expect(start).not.toHaveBeenCalled()
  })

  it('does not start after the connection becomes stale during a check', async () => {
    const store = useHarborStore()
    store.$patch({ snapshot: mockSnapshot(), connectionState: 'connected', snapshotStale: false })
    let complete!: (report: PrerequisiteReport) => void
    vi.spyOn(harborBridge, 'getProjectPrerequisites').mockImplementation(() => new Promise(resolve => { complete = resolve }))
    const start = vi.spyOn(harborBridge, 'startProject')
    const pending = store.startProject('reports')
    store.snapshotStale = true
    complete(ready)
    expect(await pending).toBeNull()
    expect(start).not.toHaveBeenCalled()
    expect(store.projectPrerequisiteErrors.reports).toContain('state changed')
  })

  it('preserves uncertain Start retries and permits explicit daemon admission after an advisory check fails', async () => {
    const store = useHarborStore()
    store.$patch({ snapshot: mockSnapshot(), connectionState: 'connected', snapshotStale: false })
    const check = vi.spyOn(harborBridge, 'getProjectPrerequisites').mockResolvedValue(ready)
    const start = vi.spyOn(harborBridge, 'startProject').mockImplementation(async (projectId, intentId) => ({ ...harborWireFixture.start_project, operation: { ...harborWireFixture.start_project.operation, project_id: projectId, intent_id: intentId } })).mockRejectedValueOnce(new Error('response lost'))
    expect(await store.startProject('reports')).toBeNull()
    check.mockRejectedValue(new Error('desktop PATH differs'))
    expect(await store.startProject('reports')).not.toBeNull()
    expect(check).toHaveBeenCalledTimes(1)
    expect(start.mock.calls[1]?.[1]).toBe(start.mock.calls[0]?.[1])
    expect(await store.startProject('billing', { checkPrerequisites: false })).not.toBeNull()
    expect(check).toHaveBeenCalledTimes(1)
  })

  it('drops cached observations and late replies when the desktop connection is retired', async () => {
    const store = useHarborStore()
    store.$patch({ snapshot: mockSnapshot(), connectionState: 'connected', snapshotStale: false })
    let complete!: (report: PrerequisiteReport) => void
    vi.spyOn(harborBridge, 'getProjectPrerequisites').mockResolvedValueOnce(ready).mockImplementation(() => new Promise(resolve => { complete = resolve }))
    await store.checkProjectPrerequisites('reports')
    expect(store.projectPrerequisites.reports).toBeDefined()
    const pending = store.checkProjectPrerequisites('reports')
    store.dispose()
    complete(ready)
    expect(await pending).toBeNull()
    expect(store.projectPrerequisites).toEqual({})
    expect(store.projectPrerequisiteChecking).toEqual({})
  })
})
