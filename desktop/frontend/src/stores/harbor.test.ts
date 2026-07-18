import { createPinia, setActivePinia } from 'pinia'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { harborBridge } from '@/bridge'
import { harborWireFixture } from '@/bridge/harbor.fixture'
import { mockSnapshot, mockStatus } from '@/bridge/mock'
import type { ConnectionEvent, DaemonStatus, HarborSnapshot } from '@/domain/harbor'
import { useHarborStore } from './harbor'

function deferred<T>() {
  let resolve!: (value: T) => void
  const promise = new Promise<T>((nextResolve) => {
    resolve = nextResolve
  })
  return { promise, resolve }
}

function statusWithSequence(sequence: number): DaemonStatus {
  const status = mockStatus()
  status.sequence = sequence
  return status
}

describe('Harbor store', () => {
  beforeEach(() => {
    setActivePinia(createPinia())
  })

  afterEach(() => {
    vi.restoreAllMocks()
  })

  it('loads exact snapshots and derives only project-scoped indexes', async () => {
    const store = useHarborStore()

    await store.initialize()

    expect(store.loading).toBe(false)
    expect(store.error).toBeNull()
    expect(store.connectionState).toBe('connected')
    expect(store.snapshotStale).toBe(false)
    expect(store.projects.map((project) => project.name)).toEqual(['Orders API', 'Billing', 'Storefront', 'Reports'])
    expect(store.services.map((service) => `${service.project_id}/${service.id}`)).toEqual([
      'orders-api/mysql',
      'orders-api/redis',
      'billing/database',
      'storefront/mail',
    ])
    expect(store.resources).toHaveLength(3)
    expect(store.recentResources.map((resource) => `${resource.project_id}/${resource.id}`)).toEqual([
      'orders-api/application',
      'orders-api/api-reference',
      'storefront/mail',
    ])
    expect(store.operations).toHaveLength(1)
    expect(store.daemonStatus).toMatchObject({ state: 'ready', sequence: 42 })
    expect(store.runningCount).toBe(2)
    expect(store.attentionCount).toBe(1)
  })

  it('looks up services by project and service identity together', async () => {
    const store = useHarborStore()
    await store.refresh()

    expect(store.projectById('orders-api')?.slug).toBe('orders-api')
    expect(store.serviceById('orders-api', 'redis')?.kind).toBe('cache')
    expect(store.serviceById('billing', 'redis')).toBeUndefined()
    expect(store.projectById('unknown')).toBeUndefined()
  })

  it('keeps a connected client visibly waiting when its first snapshot fails', async () => {
    vi.spyOn(harborBridge, 'getSnapshot').mockRejectedValueOnce(new Error('daemon unavailable'))
    const store = useHarborStore()

    await store.refresh()

    expect(store.snapshot).toBeNull()
    expect(store.loading).toBe(true)
    expect(store.refreshing).toBe(false)
    expect(store.connectionState).toBe('connected')
    expect(store.connectionMessage).toBe('Connected to Harbor. Waiting for the first snapshot.')
    expect(store.error).toBe('daemon unavailable')
  })

  it('keeps no-baseline reconnect states loading without erasing the last explicit error', async () => {
    let connectionListener: ((event: ConnectionEvent) => void) | undefined
    vi.spyOn(harborBridge, 'subscribeConnection').mockImplementation((listener) => {
      connectionListener = listener
      return () => undefined
    })
    vi.spyOn(harborBridge, 'getStatus').mockRejectedValueOnce(new Error('control connection refused'))
    vi.spyOn(harborBridge, 'getSnapshot').mockRejectedValueOnce(new Error('snapshot connection refused'))
    const store = useHarborStore()

    await store.initialize()
    expect(store.snapshot).toBeNull()
    expect(store.connectionState).toBe('disconnected')
    expect(store.loading).toBe(false)
    expect(store.connectionMessage).toBe('Harbor could not load local state')
    expect(store.error).toBe('snapshot connection refused')

    connectionListener?.({ state: 'connecting' })
    expect(store.loading).toBe(true)
    expect(store.connectionMessage).toBe('Connecting to Harbor')
    expect(store.error).toBe('snapshot connection refused')

    connectionListener?.({ state: 'connected' })
    expect(store.loading).toBe(true)
    expect(store.connectionMessage).toBe('Connected to Harbor. Waiting for the first snapshot.')
    expect(store.error).toBe('snapshot connection refused')
  })

  it('marks retained state stale on the first snapshot RPC failure and recovers on an equal validated read', async () => {
    const store = useHarborStore()
    await store.initialize()

    vi.spyOn(harborBridge, 'getSnapshot').mockRejectedValueOnce(new Error('snapshot stream failed'))
    await store.refresh()

    expect(store.snapshot?.sequence).toBe(42)
    expect(store.snapshotStale).toBe(true)
    expect(store.connectionState).toBe('connected')
    expect(store.connectionMessage).toBe('Connected to Harbor. Waiting for a fresh snapshot.')
    expect(store.error).toBe('snapshot stream failed')

    await store.refresh()

    expect(store.snapshot?.sequence).toBe(42)
    expect(store.snapshotStale).toBe(false)
    expect(store.connectionMessage).toBeNull()
    expect(store.error).toBeNull()
  })

  it('retains but marks snapshots stale across connection epochs until that connection publishes a snapshot', async () => {
    let snapshotListener: ((snapshot: HarborSnapshot) => void) | undefined
    let connectionListener: ((event: ConnectionEvent) => void) | undefined
    const unsubscribeSnapshot = vi.fn()
    const unsubscribeConnection = vi.fn()
    vi.spyOn(harborBridge, 'subscribe').mockImplementation((listener) => {
      snapshotListener = listener
      return unsubscribeSnapshot
    })
    vi.spyOn(harborBridge, 'subscribeConnection').mockImplementation((listener) => {
      connectionListener = listener
      return unsubscribeConnection
    })
    const store = useHarborStore()

    await store.initialize()
    expect(store.snapshot?.sequence).toBe(42)
    expect(store.snapshotStale).toBe(false)

    connectionListener?.({ state: 'disconnected' })
    expect(store.snapshot?.sequence).toBe(42)
    expect(store.snapshotStale).toBe(true)
    expect(store.connectionMessage).toBe('Harbor daemon is disconnected.')

    connectionListener?.({ state: 'connecting' })
    expect(store.snapshotStale).toBe(true)
    expect(store.connectionMessage).toBe('Reconnecting to Harbor. Showing the last snapshot.')

    connectionListener?.({ state: 'connected' })
    expect(store.snapshotStale).toBe(true)
    expect(store.connectionMessage).toBe('Connected to Harbor. Waiting for a fresh snapshot.')

    const restarted = mockSnapshot()
    restarted.sequence = 3
    restarted.projects = restarted.projects.slice(0, 1)
    restarted.recent_resource_ids = restarted.recent_resource_ids.filter((reference) => reference.project_id === 'orders-api')
    snapshotListener?.(restarted)
    expect(store.snapshot?.sequence).toBe(3)
    expect(store.snapshotStale).toBe(false)
    expect(store.connectionMessage).toBeNull()

    const stale = mockSnapshot()
    stale.sequence = 2
    stale.projects = []
    snapshotListener?.(stale)
    expect(store.snapshot?.sequence).toBe(3)
    expect(store.projects).toHaveLength(1)

    const duplicate = mockSnapshot()
    duplicate.sequence = 3
    duplicate.projects = []
    snapshotListener?.(duplicate)
    expect(store.projects).toHaveLength(1)

    store.dispose()
    expect(unsubscribeSnapshot).toHaveBeenCalledOnce()
    expect(unsubscribeConnection).toHaveBeenCalledOnce()
  })

  it('subscribes before direct reads and does not overwrite a newer event from the same connection', async () => {
    let listener: ((snapshot: HarborSnapshot) => void) | undefined
    let resolveSnapshot: ((snapshot: HarborSnapshot) => void) | undefined
    vi.spyOn(harborBridge, 'subscribe').mockImplementation((nextListener) => {
      listener = nextListener
      return () => undefined
    })
    vi.spyOn(harborBridge, 'getSnapshot').mockReturnValueOnce(new Promise((resolve) => {
      resolveSnapshot = resolve
    }))
    const store = useHarborStore()

    const initializing = store.initialize()
    expect(listener).toBeTypeOf('function')

    const newer = mockSnapshot()
    newer.sequence = 43
    listener?.(newer)
    resolveSnapshot?.(mockSnapshot())
    await initializing

    expect(store.snapshot?.sequence).toBe(43)
    expect(store.connectionState).toBe('connected')
    expect(store.snapshotStale).toBe(false)
  })

  it('keeps only the last-started status request across connection, event, and direct refresh paths', async () => {
    let snapshotListener: ((snapshot: HarborSnapshot) => void) | undefined
    let connectionListener: ((event: ConnectionEvent) => void) | undefined
    vi.spyOn(harborBridge, 'subscribe').mockImplementation((listener) => {
      snapshotListener = listener
      return () => undefined
    })
    vi.spyOn(harborBridge, 'subscribeConnection').mockImplementation((listener) => {
      connectionListener = listener
      return () => undefined
    })
    const store = useHarborStore()
    await store.initialize()

    const fromConnection = deferred<DaemonStatus>()
    const fromSnapshot = deferred<DaemonStatus>()
    const fromRefresh = deferred<DaemonStatus>()
    vi.spyOn(harborBridge, 'getStatus')
      .mockReturnValueOnce(fromConnection.promise)
      .mockReturnValueOnce(fromSnapshot.promise)
      .mockReturnValueOnce(fromRefresh.promise)

    connectionListener?.({ state: 'connected' })
    const eventSnapshot = mockSnapshot()
    eventSnapshot.sequence = 43
    snapshotListener?.(eventSnapshot)
    const refreshing = store.refresh()

    fromRefresh.resolve(statusWithSequence(300))
    await refreshing
    fromSnapshot.resolve(statusWithSequence(200))
    fromConnection.resolve(statusWithSequence(100))
    await Promise.resolve()

    expect(store.daemonStatus?.sequence).toBe(300)
  })

  it('invalidates status work from an older connection epoch', async () => {
    let connectionListener: ((event: ConnectionEvent) => void) | undefined
    vi.spyOn(harborBridge, 'subscribeConnection').mockImplementation((listener) => {
      connectionListener = listener
      return () => undefined
    })
    const store = useHarborStore()
    await store.initialize()

    const previousConnection = deferred<DaemonStatus>()
    vi.spyOn(harborBridge, 'getStatus').mockReturnValueOnce(previousConnection.promise)
    connectionListener?.({ state: 'connected' })
    connectionListener?.({ state: 'disconnected' })
    previousConnection.resolve(statusWithSequence(99))
    await Promise.resolve()

    expect(store.daemonStatus?.sequence).toBe(42)
    expect(store.connectionState).toBe('disconnected')
    expect(store.snapshotStale).toBe(true)
  })

  it('clears a connection error when a valid event recovers state', async () => {
    let listener: ((snapshot: HarborSnapshot) => void) | undefined
    vi.spyOn(harborBridge, 'subscribe').mockImplementation((nextListener) => {
      listener = nextListener
      return () => undefined
    })
    vi.spyOn(harborBridge, 'getSnapshot').mockRejectedValueOnce(new Error('connection lost'))
    const store = useHarborStore()

    await store.initialize()
    expect(store.error).toBe('connection lost')

    listener?.(mockSnapshot())

    expect(store.error).toBeNull()
    expect(store.snapshot?.sequence).toBe(42)
    expect(store.snapshotStale).toBe(false)
  })

  it('stages a successful registration immediately and confirms it from a fresh snapshot', async () => {
    const store = useHarborStore()
    await store.initialize()
    const registration = structuredClone(harborWireFixture.add_project.registration)
    const confirmed = mockSnapshot()
    confirmed.sequence = registration.revision
    confirmed.projects.push(registration.project)
    const snapshotRead = deferred<HarborSnapshot>()
    vi.spyOn(harborBridge, 'addProject').mockResolvedValueOnce({ canceled: false, registration })
    vi.spyOn(harborBridge, 'getSnapshot').mockReturnValueOnce(snapshotRead.promise)

    const adding = store.addProject()
    await vi.waitFor(() => expect(store.projectById('inventory')?.name).toBe('Inventory'))

    expect(store.addingProject).toBe(true)
    expect(store.snapshot?.sequence).toBe(43)
    expect(store.snapshotStale).toBe(true)
    snapshotRead.resolve(confirmed)

    await expect(adding).resolves.toMatchObject({ created: true, project: { id: 'inventory' } })
    expect(store.addingProject).toBe(false)
    expect(store.snapshotStale).toBe(false)
    expect(store.projectRegistrationError).toBeNull()
  })

  it('keeps picker cancellation silent and reports registration failures', async () => {
    const store = useHarborStore()
    const addProject = vi.spyOn(harborBridge, 'addProject').mockResolvedValueOnce({ canceled: true })

    await expect(store.addProject()).resolves.toBeNull()
    expect(store.projectRegistrationError).toBeNull()

    addProject.mockRejectedValueOnce(new Error('selected folder is not a GoForj project'))
    await expect(store.addProject()).resolves.toBeNull()
    expect(store.projectRegistrationError).toBe('selected folder is not a GoForj project')

    addProject.mockResolvedValueOnce({ canceled: false })
    await expect(store.addProject()).resolves.toBeNull()
    expect(store.projectRegistrationError).toBe('Harbor returned an incomplete project registration.')
    expect(store.addingProject).toBe(false)
  })

  it('delegates resource opening with both scoped identities', async () => {
    const openResource = vi.spyOn(harborBridge, 'openResource').mockResolvedValueOnce(undefined)
    const store = useHarborStore()

    await store.openResource('orders-api', 'application')

    expect(openResource).toHaveBeenCalledWith('orders-api', 'application')
    expect(store.actionError).toBeNull()
  })

  it('turns a resource-open rejection into visible state without rejecting the caller', async () => {
    vi.spyOn(harborBridge, 'openResource').mockRejectedValueOnce(new Error('browser denied the request'))
    const store = useHarborStore()

    await expect(store.openResource('orders-api', 'application')).resolves.toBeUndefined()

    expect(store.actionError).toBe('browser denied the request')
  })
})
