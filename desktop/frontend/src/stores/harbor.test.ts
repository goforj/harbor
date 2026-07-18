import { createPinia, setActivePinia } from 'pinia'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { harborBridge } from '@/bridge'
import { mockSnapshot } from '@/bridge/mock'
import type { HarborSnapshot, HarborSnapshotEvent } from '@/domain/harbor'
import { useHarborStore } from './harbor'

describe('Harbor store', () => {
  beforeEach(() => {
    setActivePinia(createPinia())
  })

  it('loads projects, services, system health, and derived status counts', async () => {
    const store = useHarborStore()

    await store.initialize()

    expect(store.loading).toBe(false)
    expect(store.error).toBeNull()
    expect(store.projects.map((project) => project.name)).toEqual([
      'orders-api',
      'billing',
      'storefront',
      'reports',
    ])
    expect(store.services.map((service) => service.name)).toEqual([
      'MySQL',
      'Redis',
      'PostgreSQL',
      'Mailpit',
    ])
    expect(store.system).toHaveLength(6)
    expect(store.runningCount).toBe(2)
    expect(store.attentionCount).toBe(1)
  })

  it('looks up projects and services by their stable identifiers', async () => {
    const store = useHarborStore()
    await store.refresh()

    expect(store.projectById('orders-api')?.domain).toBe('https://orders.test')
    expect(store.serviceById('orders-redis')?.endpoint).toBe('redis.orders.test:6379')
    expect(store.projectById('unknown')).toBeUndefined()
    expect(store.serviceById('unknown')).toBeUndefined()
  })

  it('surfaces bridge failures and always leaves the loading state', async () => {
    vi.spyOn(harborBridge, 'getSnapshot').mockRejectedValueOnce(new Error('daemon unavailable'))
    const store = useHarborStore()

    await store.refresh()

    expect(store.snapshot).toBeNull()
    expect(store.loading).toBe(false)
    expect(store.error).toBe('daemon unavailable')
  })

  it('accepts newer live snapshots, ignores stale events, and unsubscribes', async () => {
    let listener: ((event: HarborSnapshotEvent) => void) | undefined
    const unsubscribe = vi.fn()
    vi.spyOn(harborBridge, 'subscribe').mockImplementation((nextListener) => {
      listener = nextListener
      return unsubscribe
    })
    const store = useHarborStore()

    await store.initialize()
    expect(listener).toBeTypeOf('function')

    const stale = mockSnapshot()
    stale.sequence = 41
    stale.projects = []
    listener?.({ type: 'snapshot', snapshot: stale })
    expect(store.snapshot?.sequence).toBe(42)
    expect(store.projects).toHaveLength(4)

    const newer = mockSnapshot()
    newer.sequence = 43
    newer.projects = newer.projects.slice(0, 1)
    listener?.({ type: 'snapshot', snapshot: newer })
    expect(store.snapshot?.sequence).toBe(43)
    expect(store.projects.map((project) => project.name)).toEqual(['orders-api'])

    store.dispose()
    expect(unsubscribe).toHaveBeenCalledOnce()
  })

  it('subscribes before refreshing and does not overwrite a newer event', async () => {
    let listener: ((event: HarborSnapshotEvent) => void) | undefined
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
    listener?.({ type: 'snapshot', snapshot: newer })
    resolveSnapshot?.(mockSnapshot())
    await initializing

    expect(store.snapshot?.sequence).toBe(43)
  })

  it('clears a connection error when a valid event recovers state', async () => {
    let listener: ((event: HarborSnapshotEvent) => void) | undefined
    vi.spyOn(harborBridge, 'subscribe').mockImplementation((nextListener) => {
      listener = nextListener
      return () => undefined
    })
    vi.spyOn(harborBridge, 'getSnapshot').mockRejectedValueOnce(new Error('connection lost'))
    const store = useHarborStore()

    await store.initialize()
    expect(store.error).toBe('connection lost')

    listener?.({ type: 'snapshot', snapshot: mockSnapshot() })

    expect(store.error).toBeNull()
    expect(store.snapshot?.sequence).toBe(42)
  })

  it('delegates resource opening to the active bridge', async () => {
    const openResource = vi.spyOn(harborBridge, 'openResource').mockResolvedValueOnce(undefined)
    const store = useHarborStore()

    await store.openResource('orders-app')

    expect(openResource).toHaveBeenCalledWith('orders-app')
  })
})
