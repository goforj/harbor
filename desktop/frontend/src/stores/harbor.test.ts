import { createPinia, setActivePinia } from 'pinia'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { harborBridge } from '@/bridge'
import { harborWireFixture } from '@/bridge/harbor.fixture'
import { mockSnapshot, mockStatus } from '@/bridge/mock'
import type { ConnectionEvent, DaemonStatus, HarborSnapshot, NetworkSetupOperation, Operation, ProjectLifecycleOperation, ProjectRuntimeRepairConfirmation, ProjectRuntimeRepairInspection, ProjectRuntimeRepairNotActionableReason, ProjectUnregistration } from '@/domain/harbor'
import { useHarborStore } from './harbor'

function deferred<T>() {
  let resolve!: (value: T) => void
  let reject!: (reason?: unknown) => void
  const promise = new Promise<T>((nextResolve, nextReject) => {
    resolve = nextResolve
    reject = nextReject
  })
  return { promise, resolve, reject }
}

function statusWithSequence(sequence: number): DaemonStatus {
  const status = mockStatus()
  status.sequence = sequence
  return status
}

function completedNetworkSetup(): NetworkSetupOperation {
  return {
    operation: {
      id: 'operation-network-setup',
      intent_id: 'intent-network-setup',
      kind: 'network.setup',
      state: 'succeeded',
      phase: 'completed',
      requested_at: '2026-07-19T12:00:00Z',
      started_at: '2026-07-19T12:00:01Z',
      finished_at: '2026-07-19T12:00:02Z',
    },
    revision: 43,
  }
}

function lifecycleOperation(
  projectId: string,
  action: 'start' | 'stop' | 'restart',
  intentId: string,
  state: Operation['state'],
  problem?: Operation['problem'],
): Operation {
  const operation: Operation = structuredClone(
    action === 'start'
      ? harborWireFixture.start_project.operation
      : action === 'stop'
        ? harborWireFixture.stop_project.operation
        : harborWireFixture.restart_project.operation,
  )
  operation.id = `operation-${intentId}`
  operation.intent_id = intentId
  operation.project_id = projectId
  operation.state = state
  operation.phase = state
  operation.problem = problem
  if (state === 'succeeded' || state === 'failed' || state === 'cancelled') {
    operation.started_at = '2026-07-19T18:00:01Z'
    operation.finished_at = '2026-07-19T18:00:02Z'
  }
  return operation
}

function confirmableRuntimeRepairInspection(projectId = 'billing'): Extract<ProjectRuntimeRepairInspection, { disposition: 'confirmable' }> {
  const inspection = structuredClone(harborWireFixture.project_runtime_repair_inspection)
  inspection.project_id = projectId
  return inspection
}

function runtimeRepairConfirmation(projectId = 'billing'): ProjectRuntimeRepairConfirmation {
  const confirmation = structuredClone(harborWireFixture.project_runtime_repair_confirmation)
  confirmation.project.id = projectId
  return confirmation
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

  it('keeps network setup available whenever the daemon supports it', async () => {
    const supported = mockStatus()
    supported.capabilities.push('control.network-setup.v1')
    const empty = mockSnapshot()
    empty.projects = []
    const withProject = mockSnapshot()
    withProject.sequence = 43
    const unsupported = mockStatus()
    unsupported.sequence = 44
    const laterEmpty = mockSnapshot()
    laterEmpty.sequence = 44
    laterEmpty.projects = []
    vi.spyOn(harborBridge, 'getStatus')
      .mockResolvedValueOnce(supported)
      .mockResolvedValueOnce({ ...supported, sequence: 43 })
      .mockResolvedValueOnce(unsupported)
    vi.spyOn(harborBridge, 'getSnapshot')
      .mockResolvedValueOnce(empty)
      .mockResolvedValueOnce(withProject)
      .mockResolvedValueOnce(laterEmpty)
    const store = useHarborStore()

    expect(store.networkSetupOnboarding).toBe(false)

    await store.refresh()
    expect(store.networkSetupOnboarding).toBe(true)

    await store.refresh()
    expect(store.networkSetupOnboarding).toBe(true)

    await store.refresh()
    expect(store.networkSetupOnboarding).toBe(false)
  })

  it('serializes network setup, retains successful progress, and refreshes authoritative state', async () => {
    const store = useHarborStore()
    const pending = deferred<NetworkSetupOperation>()
    const setupNetwork = vi.spyOn(harborBridge, 'setupNetwork').mockReturnValueOnce(pending.promise)
    const getSnapshot = vi.spyOn(harborBridge, 'getSnapshot')

    const setup = store.setupNetwork()
    expect(store.settingUpNetwork).toBe(true)
    await expect(store.setupNetwork()).resolves.toBeNull()
    expect(setupNetwork).toHaveBeenCalledOnce()

    const result = completedNetworkSetup()
    pending.resolve(result)
    await expect(setup).resolves.toEqual(result)

    expect(store.settingUpNetwork).toBe(false)
    expect(store.networkSetupResult).toEqual(result)
    expect(store.networkSetupError).toBeNull()
    expect(getSnapshot).toHaveBeenCalledOnce()
  })

  it('reports rejected or incomplete network setup and still refreshes', async () => {
    const store = useHarborStore()
    const setupNetwork = vi.spyOn(harborBridge, 'setupNetwork')
    const getSnapshot = vi.spyOn(harborBridge, 'getSnapshot')

    setupNetwork.mockRejectedValueOnce(new Error('administrator approval was declined'))
    await expect(store.setupNetwork()).resolves.toBeNull()
    expect(store.networkSetupError).toBe('administrator approval was declined')
    expect(getSnapshot).toHaveBeenCalledTimes(1)

    const incomplete = completedNetworkSetup()
    incomplete.operation.state = 'running'
    setupNetwork.mockResolvedValueOnce(incomplete)
    await expect(store.setupNetwork()).resolves.toBeNull()
    expect(store.networkSetupError).toBe('Harbor returned incomplete network setup progress.')
    expect(store.settingUpNetwork).toBe(false)
    expect(getSnapshot).toHaveBeenCalledTimes(2)
  })

  it('does not send a project lifecycle request while network setup is active', async () => {
    const store = useHarborStore()
    const pending = deferred<NetworkSetupOperation>()
    vi.spyOn(harborBridge, 'setupNetwork').mockReturnValueOnce(pending.promise)
    const startProject = vi.spyOn(harborBridge, 'startProject')

    const setup = store.setupNetwork()
    await vi.waitFor(() => expect(store.settingUpNetwork).toBe(true))

    await expect(store.startProject('reports')).resolves.toBeNull()
    expect(startProject).not.toHaveBeenCalled()
    expect(store.projectLifecycleErrors.reports).toBe('Wait for network setup to finish, then try the project action again.')

    pending.resolve(completedNetworkSetup())
    await setup
  })

  it.each([
    ['disconnected', { connectionState: 'disconnected' as const, snapshotStale: true }, 'Harbor is disconnected. Reconnect, then try again.'],
    ['stale', { connectionState: 'connected' as const, snapshotStale: true }, 'Harbor is still reconciling local state. Wait for a fresh snapshot, then try again.'],
  ])('does not send a lifecycle request while daemon state is %s', async (_name, state, message) => {
    const store = useHarborStore()
    await store.initialize()
    store.$patch(state)
    const startProject = vi.spyOn(harborBridge, 'startProject')

    await expect(store.startProject('reports')).resolves.toBeNull()
    expect(startProject).not.toHaveBeenCalled()
    expect(store.projectLifecycleErrors.reports).toBe(message)
    expect(store.projectLifecycleBusy).toBe(false)
  })

  it('does not send network setup while a project lifecycle request is active', async () => {
    const store = useHarborStore()
    await store.initialize()
    const pending = deferred<ProjectLifecycleOperation>()
    const startResult: ProjectLifecycleOperation = structuredClone(harborWireFixture.start_project)
    vi.spyOn(harborBridge, 'startProject').mockImplementationOnce((projectId, intentId) => {
      startResult.operation.project_id = projectId
      startResult.operation.intent_id = intentId
      return pending.promise
    })
    const setupNetwork = vi.spyOn(harborBridge, 'setupNetwork')

    const starting = store.startProject('reports')
    await vi.waitFor(() => expect(store.projectLifecycleBusy).toBe(true))

    await expect(store.setupNetwork()).resolves.toBeNull()
    expect(setupNetwork).not.toHaveBeenCalled()
    expect(store.networkSetupError).toBe('Wait for the current project action to finish, then try network setup again.')

    pending.resolve(startResult)
    await starting
    expect(store.projectLifecycleBusy).toBe(true)
    await expect(store.setupNetwork()).resolves.toBeNull()
    expect(setupNetwork).not.toHaveBeenCalled()
  })

  it('allows independent project lifecycle requests while preserving each project request state', async () => {
    const store = useHarborStore()
    await store.initialize()
    const reports = deferred<ProjectLifecycleOperation>()
    const billing = deferred<ProjectLifecycleOperation>()
    const startProject = vi.spyOn(harborBridge, 'startProject')
      .mockReturnValueOnce(reports.promise)
      .mockReturnValueOnce(billing.promise)

    const startingReports = store.startProject('reports')
    await vi.waitFor(() => expect(store.projectLifecycleBusyFor('reports')).toBe(true))

    await expect(store.startProject('reports')).resolves.toBeNull()
    expect(startProject).toHaveBeenCalledTimes(1)

    const startingBilling = store.startProject('billing')
    await vi.waitFor(() => expect(startProject).toHaveBeenCalledTimes(2))
    expect(store.projectLifecycleBusyFor('billing')).toBe(true)

    const reportsResult = structuredClone(harborWireFixture.start_project) as ProjectLifecycleOperation
    reportsResult.operation.project_id = 'reports'
    reportsResult.operation.intent_id = startProject.mock.calls[0][1]
    reportsResult.operation.state = 'succeeded'
    reportsResult.operation.phase = 'succeeded'
    reportsResult.operation.started_at = '2026-07-19T18:00:01Z'
    reportsResult.operation.finished_at = '2026-07-19T18:00:02Z'
    reports.resolve(reportsResult)
    await startingReports

    expect(store.projectLifecycleBusyFor('reports')).toBe(false)
    expect(store.projectLifecycleBusyFor('billing')).toBe(true)

    const billingResult = structuredClone(harborWireFixture.start_project) as ProjectLifecycleOperation
    billingResult.operation.project_id = 'billing'
    billingResult.operation.intent_id = startProject.mock.calls[1][1]
    billingResult.operation.state = 'succeeded'
    billingResult.operation.phase = 'succeeded'
    billingResult.operation.started_at = '2026-07-19T18:00:01Z'
    billingResult.operation.finished_at = '2026-07-19T18:00:02Z'
    billing.resolve(billingResult)
    await startingBilling
    expect(store.projectLifecycleBusy).toBe(false)
  })

  it('keeps an uncertain restart through another project refresh and retries it without permitting a conflicting stop', async () => {
    const store = useHarborStore()
    await store.initialize()
    const billingRestart = deferred<ProjectLifecycleOperation>()
    const unchanged = mockSnapshot()
    vi.spyOn(harborBridge, 'getSnapshot').mockResolvedValue(unchanged)
    const restartProject = vi.spyOn(harborBridge, 'restartProject')
      .mockReturnValueOnce(billingRestart.promise)
      .mockImplementationOnce(async (projectId, intentId) => {
        const result = structuredClone(harborWireFixture.restart_project) as ProjectLifecycleOperation
        result.operation.project_id = projectId
        result.operation.intent_id = intentId
        result.operation.state = 'succeeded'
        result.operation.phase = 'succeeded'
        result.operation.started_at = '2026-07-19T18:00:01Z'
        result.operation.finished_at = '2026-07-19T18:00:02Z'
        return result
      })
    const startProject = vi.spyOn(harborBridge, 'startProject').mockImplementationOnce(async (projectId, intentId) => {
      const result = structuredClone(harborWireFixture.start_project) as ProjectLifecycleOperation
      result.operation.project_id = projectId
      result.operation.intent_id = intentId
      result.operation.state = 'succeeded'
      result.operation.phase = 'succeeded'
      result.operation.started_at = '2026-07-19T18:00:01Z'
      result.operation.finished_at = '2026-07-19T18:00:02Z'
      return result
    })
    const stopProject = vi.spyOn(harborBridge, 'stopProject')

    const restartingBilling = store.restartProject('billing')
    await vi.waitFor(() => expect(store.projectLifecycleBusyFor('billing')).toBe(true))

    await store.startProject('reports')
    expect(store.projectLifecycleBusyFor('billing')).toBe(true)

    billingRestart.reject(new Error('connection closed before the operation response'))
    await expect(restartingBilling).resolves.toBeNull()

    expect(store.projectLifecycleBusyFor('billing')).toBe(false)
    expect(store.projectLifecycleBlockedFor('billing', 'restart')).toBe(false)
    expect(store.projectLifecycleBlockedFor('billing', 'stop')).toBe(true)
    await expect(store.stopProject('billing')).resolves.toBeNull()
    expect(stopProject).not.toHaveBeenCalled()

    await store.restartProject('billing')
    expect(restartProject).toHaveBeenCalledTimes(2)
    expect(restartProject.mock.calls[1][1]).toBe(restartProject.mock.calls[0][1])
  })

  it('restores only the latest retained lifecycle outcome and clears it when newer work starts', async () => {
    const baseline = mockSnapshot()
    baseline.sequence = 50
    const reports = baseline.projects.find((project) => project.id === 'reports')
    if (!reports) throw new Error('reports fixture is missing')
    reports.state = 'failed'
    baseline.operations.push(
      lifecycleOperation('reports', 'start', 'reports-failed', 'failed', {
        code: 'project.process.exited',
        message: 'forj dev exited before the project became ready.',
        retryable: true,
      }),
      lifecycleOperation('orders-api', 'start', 'orders-failed', 'failed', {
        code: 'project.process.exited',
        message: 'An earlier start failed.',
        retryable: true,
      }),
      lifecycleOperation('orders-api', 'start', 'orders-succeeded', 'succeeded'),
    )
    const newer = structuredClone(baseline)
    newer.sequence = 51
    newer.operations.push(lifecycleOperation('reports', 'start', 'reports-retry', 'running'))
    const newerReports = newer.projects.find((project) => project.id === 'reports')
    if (!newerReports) throw new Error('reports fixture is missing from newer snapshot')
    newerReports.state = 'starting'
    vi.spyOn(harborBridge, 'getSnapshot')
      .mockResolvedValueOnce(baseline)
      .mockResolvedValueOnce(newer)
    const store = useHarborStore()

    await store.initialize()

    expect(store.projectLifecycleErrors.reports).toBe('forj dev exited before the project became ready.')
    expect(store.projectLifecycleProblemCodes.reports).toBe('project.process.exited')
    expect(store.projectLifecycleErrors['orders-api']).toBeUndefined()

    await store.refresh()

    expect(store.projectLifecycleErrors.reports).toBeUndefined()
    expect(store.projectLifecycleProblemCodes.reports).toBeUndefined()
    expect(store.activeProjectLifecycle('reports')?.intent_id).toBe('reports-retry')
    expect(store.projectLifecycleBusy).toBe(true)
  })

  it('does not resurrect completed runtime recovery from retained failure history', async () => {
    const recovered = mockSnapshot()
    recovered.sequence = 51
    const reports = recovered.projects.find((project) => project.id === 'reports')
    if (!reports) throw new Error('reports fixture is missing')
    reports.state = 'stopped'
    recovered.operations.push(lifecycleOperation('reports', 'start', 'reports-recovery', 'failed', {
      code: 'project.recovery.ambiguous_launch',
      message: 'Harbor could not prove which process belongs to this project.',
      retryable: false,
    }))
    vi.spyOn(harborBridge, 'getSnapshot').mockResolvedValueOnce(recovered)
    const store = useHarborStore()

    await store.initialize()

    expect(store.projectById('reports')?.state).toBe('stopped')
    expect(store.projectLifecycleErrors.reports).toBeUndefined()
    expect(store.projectLifecycleProblemCodes.reports).toBeUndefined()
  })

  it('starts a project with a client-owned intent and adopts the authoritative lifecycle snapshot', async () => {
    const store = useHarborStore()
    await store.initialize()
    const result: ProjectLifecycleOperation = structuredClone(harborWireFixture.start_project)
    const confirmed = mockSnapshot()
    confirmed.sequence = result.revision
    const project = confirmed.projects.find((entry) => entry.id === 'reports')
    if (!project) throw new Error('reports fixture is missing')
    project.state = 'starting'
    confirmed.operations.push(result.operation)
    const startProject = vi.spyOn(harborBridge, 'startProject').mockImplementationOnce(async (projectId, intentId) => {
      result.operation.project_id = projectId
      result.operation.intent_id = intentId
      return result
    })
    vi.spyOn(harborBridge, 'getSnapshot').mockResolvedValueOnce(confirmed)

    await expect(store.startProject('reports')).resolves.toMatchObject({ operation: { kind: 'project.start' } })

    expect(startProject.mock.calls[0][1]).toMatch(/^desktop-project-start-[0-9a-f]{32}$/)
    expect(store.projectById('reports')?.state).toBe('starting')
    expect(store.activeProjectLifecycle('reports')?.kind).toBe('project.start')
    expect(store.projectLifecycleBusyFor('reports')).toBe(true)
    expect(store.projectLifecycleErrors.reports).toBeUndefined()
  })

  it('restarts a project with a client-owned intent and tracks the replacement operation', async () => {
    const store = useHarborStore()
    await store.initialize()
    const result: ProjectLifecycleOperation = structuredClone(harborWireFixture.restart_project)
    const restartProject = vi.spyOn(harborBridge, 'restartProject').mockImplementationOnce(async (projectId, intentId) => {
      result.operation.project_id = projectId
      result.operation.intent_id = intentId
      return result
    })
    const confirmed = mockSnapshot()
    confirmed.sequence = result.revision
    const billing = confirmed.projects.find((project) => project.id === 'billing')
    if (!billing) throw new Error('billing fixture is missing')
    billing.state = 'stopping'
    confirmed.operations = [result.operation]
    vi.spyOn(harborBridge, 'getSnapshot').mockResolvedValueOnce(confirmed)

    await expect(store.restartProject('billing')).resolves.toMatchObject({ operation: { kind: 'project.restart' } })

    expect(restartProject.mock.calls[0][1]).toMatch(/^desktop-project-restart-[0-9a-f]{32}$/)
    expect(store.activeProjectLifecycle('billing')?.kind).toBe('project.restart')
    expect(store.projectLifecycleBusyFor('billing')).toBe(true)
    expect(store.projectLifecycleErrors.billing).toBeUndefined()
  })

  it('keeps the daemon-reviewed problem when a project start fails during refresh', async () => {
    const store = useHarborStore()
    await store.initialize()
    const result: ProjectLifecycleOperation = structuredClone(harborWireFixture.start_project)
    const failed = mockSnapshot()
    failed.sequence = result.revision + 1
    const project = failed.projects.find((entry) => entry.id === 'reports')
    if (!project) throw new Error('reports fixture is missing')
    project.state = 'failed'
    vi.spyOn(harborBridge, 'startProject').mockImplementationOnce(async (projectId, intentId) => {
      result.operation.project_id = projectId
      result.operation.intent_id = intentId
      failed.operations.push({
        ...structuredClone(result.operation),
        state: 'failed',
        phase: 'failed',
        problem: {
          code: 'project.process.exited',
          message: 'forj dev exited before the project became ready.',
          retryable: true,
        },
        started_at: '2026-07-19T18:00:01Z',
        finished_at: '2026-07-19T18:00:02Z',
      })
      return result
    })
    vi.spyOn(harborBridge, 'getSnapshot').mockResolvedValueOnce(failed)

    await expect(store.startProject('reports')).resolves.toMatchObject({ operation: { kind: 'project.start' } })

    expect(store.projectById('reports')?.state).toBe('failed')
    expect(store.projectLifecycleBusyFor('reports')).toBe(false)
    expect(store.activeProjectLifecycle('reports')).toBeUndefined()
    expect(store.projectLifecycleErrors.reports).toBe('forj dev exited before the project became ready.')
    expect(store.projectLifecycleProblemCodes.reports).toBe('project.process.exited')
  })

  it('uses a bounded fallback when terminal lifecycle failure has no daemon problem', async () => {
    const store = useHarborStore()
    await store.initialize()
    const result: ProjectLifecycleOperation = structuredClone(harborWireFixture.stop_project)
    const failed = mockSnapshot()
    failed.sequence = result.revision + 1
    const project = failed.projects.find((entry) => entry.id === 'orders-api')
    if (!project) throw new Error('orders fixture is missing')
    project.state = 'failed'
    vi.spyOn(harborBridge, 'stopProject').mockImplementationOnce(async (projectId, intentId) => {
      result.operation.project_id = projectId
      result.operation.intent_id = intentId
      failed.operations.push({
        ...structuredClone(result.operation),
        state: 'failed',
        phase: 'failed',
        started_at: '2026-07-19T18:00:01Z',
        finished_at: '2026-07-19T18:00:02Z',
      })
      return result
    })
    vi.spyOn(harborBridge, 'getSnapshot').mockResolvedValueOnce(failed)

    await store.stopProject('orders-api')

    expect(store.projectLifecycleErrors['orders-api']).toBe('Harbor could not stop the project.')
    expect(store.projectLifecycleProblemCodes['orders-api']).toBe('project.stop_failed')
  })

  it('drops a terminal lifecycle intent so an app retry creates a new operation', async () => {
    const store = useHarborStore()
    await store.initialize()
    const failed: ProjectLifecycleOperation = structuredClone(harborWireFixture.start_project)
    failed.operation.state = 'failed'
    failed.operation.phase = 'network admission failed'
    failed.operation.problem = {
      code: 'project.network.setup_required',
      message: 'Complete network setup and try again.',
      retryable: true,
    }
    failed.operation.started_at = '2026-07-19T18:00:01Z'
    failed.operation.finished_at = '2026-07-19T18:00:02Z'
    const queued: ProjectLifecycleOperation = structuredClone(harborWireFixture.start_project)
    const intents: string[] = []
    vi.spyOn(harborBridge, 'startProject').mockImplementation(async (projectId, intentId) => {
      intents.push(intentId)
      const result = intents.length === 1 ? failed : queued
      result.operation.project_id = projectId
      result.operation.intent_id = intentId
      return result
    })

    await expect(store.startProject('reports')).resolves.toEqual(failed)
    expect(store.projectLifecycleErrors.reports).toBe('Complete network setup and try again.')
    expect(store.projectLifecycleProblemCodes.reports).toBe('project.network.setup_required')

    await expect(store.startProject('reports')).resolves.toEqual(queued)
    expect(intents).toHaveLength(2)
    expect(intents[1]).not.toBe(intents[0])
    expect(store.projectLifecycleErrors.reports).toBeUndefined()
    expect(store.projectLifecycleProblemCodes.reports).toBeUndefined()
  })

  it('does not preserve an immediate terminal error over a newer successful snapshot', async () => {
    const store = useHarborStore()
    await store.initialize()
    const failed: ProjectLifecycleOperation = structuredClone(harborWireFixture.start_project)
    failed.operation.state = 'failed'
    failed.operation.phase = 'failed'
    failed.operation.problem = {
      code: 'project.process.exited',
      message: 'The first process exited.',
      retryable: true,
    }
    failed.operation.started_at = '2026-07-19T18:00:01Z'
    failed.operation.finished_at = '2026-07-19T18:00:02Z'
    const succeeded = mockSnapshot()
    succeeded.sequence = failed.revision + 1
    succeeded.operations.push(lifecycleOperation('reports', 'start', 'reports-succeeded', 'succeeded'))
    vi.spyOn(harborBridge, 'startProject').mockImplementationOnce(async (projectId, intentId) => {
      failed.operation.project_id = projectId
      failed.operation.intent_id = intentId
      return failed
    })
    vi.spyOn(harborBridge, 'getSnapshot').mockResolvedValueOnce(succeeded)

    await store.startProject('reports')

    expect(store.projectLifecycleErrors.reports).toBeUndefined()
    expect(store.projectLifecycleProblemCodes.reports).toBeUndefined()
  })

  it('reuses an uncertain lifecycle intent so a lost response cannot enqueue a second start', async () => {
    const store = useHarborStore()
    const result: ProjectLifecycleOperation = structuredClone(harborWireFixture.start_project)
    const startProject = vi.spyOn(harborBridge, 'startProject')
      .mockRejectedValueOnce(new Error('connection closed before the operation response'))
      .mockImplementationOnce(async (projectId, intentId) => {
        result.operation.project_id = projectId
        result.operation.intent_id = intentId
        return result
      })
    vi.spyOn(harborBridge, 'getSnapshot').mockRejectedValue(new Error('snapshot unavailable'))

    await expect(store.startProject('reports')).resolves.toBeNull()
    await expect(store.startProject('reports')).resolves.toMatchObject({ operation: { kind: 'project.start' } })

    expect(startProject).toHaveBeenCalledTimes(2)
    expect(startProject.mock.calls[0][1]).toMatch(/^desktop-project-start-[0-9a-f]{32}$/)
    expect(startProject.mock.calls[1][1]).toBe(startProject.mock.calls[0][1])
  })

  it('resumes a daemon-reported stop intent instead of inventing another operation identity', async () => {
    const hydrated = mockSnapshot()
    const stop = structuredClone(harborWireFixture.stop_project)
    stop.operation.intent_id = 'desktop-existing-stop'
    hydrated.sequence = stop.revision
    hydrated.operations.push(stop.operation)
    const orders = hydrated.projects.find((project) => project.id === 'orders-api')
    if (!orders) throw new Error('orders fixture is missing')
    orders.state = 'stopping'
    vi.spyOn(harborBridge, 'getSnapshot').mockResolvedValue(hydrated)
    const store = useHarborStore()
    await store.initialize()
    const stopProject = vi.spyOn(harborBridge, 'stopProject').mockImplementationOnce(async (projectId, intentId) => {
      stop.operation.project_id = projectId
      stop.operation.intent_id = intentId
      return stop
    })

    await store.stopProject('orders-api')

    expect(stopProject).toHaveBeenCalledWith('orders-api', 'desktop-existing-stop')
  })

  it('keeps a confirmable runtime inspection for explicit confirmation without confirming automatically', async () => {
    const store = useHarborStore()
    await store.initialize()
    const inspection = confirmableRuntimeRepairInspection()
    const inspect = vi.spyOn(harborBridge, 'inspectProjectRuntimeRepair').mockResolvedValueOnce(inspection)
    const confirm = vi.spyOn(harborBridge, 'confirmProjectRuntimeRepair')

    await expect(store.inspectProjectRuntimeRepair('billing')).resolves.toEqual(inspection)

    expect(inspect).toHaveBeenCalledWith('billing')
    expect(confirm).not.toHaveBeenCalled()
    expect(store.projectRuntimeRepairInspection).toEqual(inspection)
    expect(store.projectRuntimeRepairBusy).toBe(false)
    expect(store.projectRuntimeRepairNotice('billing')).toBeUndefined()
  })

  it.each([
    {
      reason: 'none',
      title: 'No stale runtime found',
      message: 'Harbor did not find a process listening at this project’s endpoint. No process was stopped.',
    },
    {
      reason: 'ambiguous',
      title: 'Stale runtime is ambiguous',
      message: 'Harbor found more than one possible process scope and cannot safely choose one. No process was stopped.',
    },
    {
      reason: 'foreign',
      title: 'Stale runtime belongs to another user',
      message: 'The process at this project’s endpoint belongs to another user. Harbor will not stop it.',
    },
    {
      reason: 'unreadable',
      title: 'Stale runtime details are incomplete',
      message: 'Harbor could not read all required process details. No process was stopped.',
    },
  ] satisfies Array<{ reason: ProjectRuntimeRepairNotActionableReason; title: string; message: string }>)('shows the fixed $reason runtime diagnostic without retaining a plan', async ({ reason, title, message }) => {
    const store = useHarborStore()
    await store.initialize()
    vi.spyOn(harborBridge, 'inspectProjectRuntimeRepair').mockResolvedValueOnce({
      project_id: 'billing',
      disposition: 'not_actionable',
      reason,
    })

    await expect(store.inspectProjectRuntimeRepair('billing')).resolves.toMatchObject({ disposition: 'not_actionable', reason })

    expect(store.projectRuntimeRepairInspection).toBeNull()
    expect(store.projectRuntimeRepairNotice('billing')).toEqual({
      state: 'not_actionable',
      title,
      message,
    })
  })

  it('labels an unsupported runtime inspection as macOS-only without retaining a plan', async () => {
    const store = useHarborStore()
    await store.initialize()
    vi.spyOn(harborBridge, 'inspectProjectRuntimeRepair').mockResolvedValueOnce({
      project_id: 'billing',
      disposition: 'unsupported',
    })

    await expect(store.inspectProjectRuntimeRepair('billing')).resolves.toMatchObject({ disposition: 'unsupported' })

    expect(store.projectRuntimeRepairInspection).toBeNull()
    expect(store.projectRuntimeRepairNotice('billing')).toEqual({
      state: 'unsupported',
      title: 'Stale runtime inspection unavailable',
      message: 'Stale runtime inspection is currently available only on macOS.',
    })
  })

  it('rejects blocked, failed, cross-project, and incomplete runtime inspections without retaining authority', async () => {
    const store = useHarborStore()
    await store.initialize()
    const inspect = vi.spyOn(harborBridge, 'inspectProjectRuntimeRepair')

    store.$patch({ snapshotStale: true })
    await expect(store.inspectProjectRuntimeRepair('billing')).resolves.toBeNull()
    expect(inspect).not.toHaveBeenCalled()
    expect(store.projectRuntimeRepairNotice('billing')?.message).toContain('fresh Harbor snapshot')

    store.$patch({ snapshotStale: false })
    inspect.mockRejectedValueOnce(new Error('native inspection failed'))
    await expect(store.inspectProjectRuntimeRepair('billing')).resolves.toBeNull()
    expect(store.projectRuntimeRepairNotice('billing')).toMatchObject({
      state: 'failed',
      title: 'Recovery check failed',
      message: 'Harbor could not verify the previous runtime. Try again.',
    })

    inspect.mockResolvedValueOnce(confirmableRuntimeRepairInspection('orders-api'))
    await expect(store.inspectProjectRuntimeRepair('billing')).resolves.toBeNull()
    expect(store.projectRuntimeRepairNotice('billing')?.message).toBe('Harbor could not verify the previous runtime. Try again.')

    const incomplete = confirmableRuntimeRepairInspection()
    incomplete.confirmable.candidate.command = '' as 'forj dev'
    inspect.mockResolvedValueOnce(incomplete)
    await expect(store.inspectProjectRuntimeRepair('billing')).resolves.toBeNull()
    expect(store.projectRuntimeRepairNotice('billing')?.message).toBe('Harbor could not verify the previous runtime. Try again.')
    expect(store.projectRuntimeRepairInspection).toBeNull()
  })

  it('discards a runtime inspection and ignores its delayed result across every reconnect event', async () => {
    let connectionListener: ((event: ConnectionEvent) => void) | undefined
    vi.spyOn(harborBridge, 'subscribeConnection').mockImplementation((listener) => {
      connectionListener = listener
      return () => undefined
    })
    const store = useHarborStore()
    await store.initialize()
    const pending = deferred<ProjectRuntimeRepairInspection>()
    vi.spyOn(harborBridge, 'inspectProjectRuntimeRepair').mockReturnValueOnce(pending.promise)

    const inspecting = store.inspectProjectRuntimeRepair('billing')
    expect(store.projectRuntimeRepairBusy).toBe(true)
    connectionListener?.({ state: 'disconnected' })
    pending.resolve(confirmableRuntimeRepairInspection())

    await expect(inspecting).resolves.toBeNull()
    expect(store.projectRuntimeRepairInspection).toBeNull()
    expect(store.projectRuntimeRepairBusy).toBe(false)

    for (const state of ['connecting', 'connected'] as const) {
      store.$patch({ projectRuntimeRepairInspection: confirmableRuntimeRepairInspection() })
      connectionListener?.({ state })
      expect(store.projectRuntimeRepairInspection).toBeNull()
    }
  })

  it('consumes opaque runtime selectors before confirmation and refreshes the stopped projection afterward', async () => {
    const store = useHarborStore()
    await store.initialize()
    const inspection = confirmableRuntimeRepairInspection()
    vi.spyOn(harborBridge, 'inspectProjectRuntimeRepair').mockResolvedValueOnce(inspection)
    await store.inspectProjectRuntimeRepair('billing')
    const pending = deferred<ProjectRuntimeRepairConfirmation>()
    const confirm = vi.spyOn(harborBridge, 'confirmProjectRuntimeRepair').mockReturnValueOnce(pending.promise)
    const getSnapshot = vi.spyOn(harborBridge, 'getSnapshot')

    const confirming = store.confirmProjectRuntimeRepair('billing')
    expect(store.projectRuntimeRepairInspection).toBeNull()
    expect(store.projectRuntimeRepairBusy).toBe(true)
    expect(confirm).toHaveBeenCalledWith(
      'billing',
      inspection.confirmable.inspection_id,
      inspection.confirmable.candidate_fingerprint,
    )

    const confirmation = runtimeRepairConfirmation()
    pending.resolve(confirmation)
    await expect(confirming).resolves.toEqual(confirmation)

    expect(getSnapshot).toHaveBeenCalledOnce()
    expect(store.projectById('billing')?.state).toBe('stopped')
    expect(store.projectRuntimeRepairBusy).toBe(false)
    expect(store.projectRuntimeRepairNotice('billing')).toMatchObject({ state: 'succeeded' })
  })

  it('requires a fresh runtime plan and refreshes even when a confirmation attempt cannot be sent', async () => {
    const store = useHarborStore()
    await store.initialize()
    const confirm = vi.spyOn(harborBridge, 'confirmProjectRuntimeRepair')
    const getSnapshot = vi.spyOn(harborBridge, 'getSnapshot')

    await expect(store.confirmProjectRuntimeRepair('billing')).resolves.toBeNull()

    expect(confirm).not.toHaveBeenCalled()
    expect(getSnapshot).toHaveBeenCalledOnce()
    expect(store.projectRuntimeRepairNotice('billing')).toEqual({
      state: 'expired',
      title: 'Fresh inspection required',
      message: 'Inspect the stale runtime again before confirming this action.',
    })
  })

  it('consumes expired and failed runtime plans without confirming and refreshes after each attempt', async () => {
    const store = useHarborStore()
    await store.initialize()
    const inspect = vi.spyOn(harborBridge, 'inspectProjectRuntimeRepair')
    const confirm = vi.spyOn(harborBridge, 'confirmProjectRuntimeRepair')
    const getSnapshot = vi.spyOn(harborBridge, 'getSnapshot')

    const expired = confirmableRuntimeRepairInspection()
    expired.confirmable.expires_at = '2020-01-01T00:00:00Z'
    inspect.mockResolvedValueOnce(expired)
    await store.inspectProjectRuntimeRepair('billing')
    await expect(store.confirmProjectRuntimeRepair('billing')).resolves.toBeNull()

    expect(confirm).not.toHaveBeenCalled()
    expect(getSnapshot).toHaveBeenCalledTimes(1)
    expect(store.projectRuntimeRepairInspection).toBeNull()
    expect(store.projectRuntimeRepairNotice('billing')).toMatchObject({ state: 'expired' })

    inspect.mockResolvedValueOnce(confirmableRuntimeRepairInspection())
    confirm.mockRejectedValueOnce(new Error('candidate changed before signal'))
    await store.inspectProjectRuntimeRepair('billing')
    await expect(store.confirmProjectRuntimeRepair('billing')).resolves.toBeNull()

    expect(getSnapshot).toHaveBeenCalledTimes(2)
    expect(store.projectRuntimeRepairInspection).toBeNull()
    expect(store.projectRuntimeRepairNotice('billing')).toEqual({
      state: 'failed',
      title: 'Stale runtime repair failed',
      message: 'candidate changed before signal',
    })
  })

  it('rejects a mismatched or incomplete runtime confirmation and still refreshes authoritative state', async () => {
    const store = useHarborStore()
    await store.initialize()
    const inspect = vi.spyOn(harborBridge, 'inspectProjectRuntimeRepair')
    const confirm = vi.spyOn(harborBridge, 'confirmProjectRuntimeRepair')
    const getSnapshot = vi.spyOn(harborBridge, 'getSnapshot')

    inspect.mockResolvedValueOnce(confirmableRuntimeRepairInspection())
    confirm.mockResolvedValueOnce(runtimeRepairConfirmation('orders-api'))
    await store.inspectProjectRuntimeRepair('billing')
    await expect(store.confirmProjectRuntimeRepair('billing')).resolves.toBeNull()
    expect(store.projectRuntimeRepairNotice('billing')?.message).toContain('another project')

    const incomplete = runtimeRepairConfirmation()
    incomplete.project.state = 'ready'
    inspect.mockResolvedValueOnce(confirmableRuntimeRepairInspection())
    confirm.mockResolvedValueOnce(incomplete)
    await store.inspectProjectRuntimeRepair('billing')
    await expect(store.confirmProjectRuntimeRepair('billing')).resolves.toBeNull()

    expect(getSnapshot).toHaveBeenCalledTimes(2)
    expect(store.projectRuntimeRepairNotice('billing')?.message).toContain('incomplete stale runtime confirmation')
  })

  it('refreshes authoritative state after an immediate project removal', async () => {
    const store = useHarborStore()
    await store.initialize()
    const result: ProjectUnregistration = structuredClone(harborWireFixture.remove_project)
    result.operation.project_id = 'reports'
    result.operation.intent_id = 'placeholder'
    result.operation.state = 'succeeded'
    result.operation.phase = 'completed'
    result.operation.finished_at = '2026-07-18T14:40:02Z'
    result.revision = 43
    const confirmed = mockSnapshot()
    confirmed.sequence = 43
    confirmed.projects = confirmed.projects.filter((project) => project.id !== 'reports')
    const removeProject = vi.spyOn(harborBridge, 'removeProject').mockImplementationOnce(async (projectId, intentId) => {
      result.operation.project_id = projectId
      result.operation.intent_id = intentId
      return result
    })
    const getSnapshot = vi.spyOn(harborBridge, 'getSnapshot').mockResolvedValueOnce(confirmed)

    await expect(store.removeProject('reports')).resolves.toMatchObject({ operation: { state: 'succeeded' } })

    expect(removeProject).toHaveBeenCalledOnce()
    expect(removeProject.mock.calls[0][1]).toMatch(/^desktop-project-remove-[0-9a-f]{32}$/)
    expect(getSnapshot).toHaveBeenCalledOnce()
    expect(store.projectById('reports')).toBeUndefined()
    expect(store.projectRemovalNotice('reports')).toBeUndefined()
    expect(store.removingProjectId).toBeNull()
  })

  it('completes a pending project removal through the approval bridge and consumes the intent', async () => {
    const store = useHarborStore()
    await store.initialize()
    const pending = structuredClone(harborWireFixture.remove_project)
    const approved = structuredClone(harborWireFixture.approve_project_removal)
    const confirmed = mockSnapshot()
    confirmed.sequence = approved.revision
    confirmed.projects = confirmed.projects.filter((project) => project.id !== 'orders-api')
    confirmed.operations = confirmed.operations.filter((operation) => operation.project_id !== 'orders-api')
    confirmed.recent_resource_ids = confirmed.recent_resource_ids.filter((reference) => reference.project_id !== 'orders-api')
    vi.spyOn(harborBridge, 'removeProject').mockImplementationOnce(async (projectId, intentId) => {
      pending.operation.project_id = projectId
      pending.operation.intent_id = intentId
      return pending
    })
    const getSnapshot = vi.spyOn(harborBridge, 'getSnapshot').mockResolvedValueOnce(mockSnapshot()).mockResolvedValueOnce(confirmed)
    const approveProjectRemoval = vi.spyOn(harborBridge, 'approveProjectRemoval').mockImplementationOnce(async (projectId, intentId) => {
      approved.operation.project_id = projectId
      approved.operation.intent_id = intentId
      return approved
    })

    await store.removeProject('orders-api')
    expect(store.projectRemovalNotice('orders-api')?.state).toBe('requires_approval')
    await expect(store.approveProjectRemoval('orders-api')).resolves.toMatchObject({
      operation: { state: 'succeeded', project_id: 'orders-api' },
    })

    expect(approveProjectRemoval).toHaveBeenCalledWith('orders-api', pending.operation.intent_id)
    expect(store.projectById('orders-api')).toBeUndefined()
    expect(store.projectRemovalNotice('orders-api')).toBeUndefined()
    expect(store.projectRemovalApprovalBusy).toBe(false)
    expect(getSnapshot).toHaveBeenCalledTimes(2)
  })

  it('keeps the approval intent retryable when the native approval bridge fails', async () => {
    const store = useHarborStore()
    await store.initialize()
    const pending = structuredClone(harborWireFixture.remove_project)
    vi.spyOn(harborBridge, 'removeProject').mockImplementationOnce(async (projectId, intentId) => {
      pending.operation.project_id = projectId
      pending.operation.intent_id = intentId
      return pending
    })
    vi.spyOn(harborBridge, 'getSnapshot').mockResolvedValueOnce(mockSnapshot())
    const approveProjectRemoval = vi.spyOn(harborBridge, 'approveProjectRemoval').mockRejectedValueOnce(new Error('administrator approval was declined'))

    await store.removeProject('orders-api')
    const result = await store.approveProjectRemoval('orders-api')

    expect(result).toBeNull()
    expect(approveProjectRemoval).toHaveBeenCalledWith('orders-api', pending.operation.intent_id)
    expect(store.projectRemovalNotice('orders-api')).toEqual({
      state: 'requires_approval',
      title: 'Administrator approval still required',
      message: 'administrator approval was declined',
    })
    expect(store.projectRemovalApprovalBusy).toBe(false)
  })

  it('uses a fresh intent after a post-failure snapshot confirms no active removal', async () => {
    const store = useHarborStore()
    const result: ProjectUnregistration = structuredClone(harborWireFixture.remove_project)
    const removeProject = vi.spyOn(harborBridge, 'removeProject')
      .mockRejectedValueOnce(new Error('Harbor is temporarily unavailable.'))
      .mockImplementationOnce(async (projectId, intentId) => {
        result.operation.project_id = projectId
        result.operation.intent_id = intentId
        return result
      })

    await expect(store.removeProject('orders-api')).resolves.toBeNull()
    expect(store.projectRemovalNotice('orders-api')).toEqual({
      state: 'request_failed',
      title: 'Harbor could not start project removal',
      message: 'Harbor is temporarily unavailable.',
    })

    await expect(store.removeProject('orders-api')).resolves.toMatchObject({ operation: { state: 'requires_approval' } })

    expect(removeProject).toHaveBeenCalledTimes(2)
    expect(removeProject.mock.calls[1][1]).not.toBe(removeProject.mock.calls[0][1])
    expect(store.projectRemovalNotice('orders-api')).toEqual({
      state: 'requires_approval',
      title: 'Administrator approval required',
      message: 'Harbor paused removal until it can release this project’s local networking. Approve the one-time administrator action to continue.',
    })
  })

  it('reuses an uncertain intent when no post-failure snapshot can confirm its outcome', async () => {
    vi.spyOn(harborBridge, 'getSnapshot').mockRejectedValueOnce(new Error('snapshot unavailable'))
    const store = useHarborStore()
    const result: ProjectUnregistration = structuredClone(harborWireFixture.remove_project)
    const removeProject = vi.spyOn(harborBridge, 'removeProject')
      .mockRejectedValueOnce(new Error('connection closed before the operation response'))
      .mockImplementationOnce(async (projectId, intentId) => {
        result.operation.project_id = projectId
        result.operation.intent_id = intentId
        return result
      })

    await expect(store.removeProject('orders-api')).resolves.toBeNull()
    await expect(store.removeProject('orders-api')).resolves.toMatchObject({ operation: { state: 'requires_approval' } })

    expect(removeProject).toHaveBeenCalledTimes(2)
    expect(removeProject.mock.calls[1][1]).toBe(removeProject.mock.calls[0][1])
  })

  it('retires an uncertain intent when a later manual refresh confirms no active removal', async () => {
    vi.spyOn(harborBridge, 'getSnapshot').mockRejectedValueOnce(new Error('snapshot unavailable'))
    const store = useHarborStore()
    const result: ProjectUnregistration = structuredClone(harborWireFixture.remove_project)
    const removeProject = vi.spyOn(harborBridge, 'removeProject')
      .mockRejectedValueOnce(new Error('connection closed before the operation response'))
      .mockImplementationOnce(async (projectId, intentId) => {
        result.operation.project_id = projectId
        result.operation.intent_id = intentId
        return result
      })

    await store.removeProject('orders-api')
    const uncertainIntent = removeProject.mock.calls[0][1]
    await store.refresh()
    await store.removeProject('orders-api')

    expect(removeProject.mock.calls[1][1]).not.toBe(uncertainIntent)
  })

  it('does not retire an uncertain intent from a delayed snapshot captured before enqueue', async () => {
    const store = useHarborStore()
    await store.initialize()
    const beforeEnqueue = deferred<HarborSnapshot>()
    const getSnapshot = vi.spyOn(harborBridge, 'getSnapshot')
      .mockReturnValueOnce(beforeEnqueue.promise)
      .mockRejectedValueOnce(new Error('post-call snapshot unavailable'))
    const earlierRefresh = store.refresh()
    const result: ProjectUnregistration = structuredClone(harborWireFixture.remove_project)
    const removeProject = vi.spyOn(harborBridge, 'removeProject')
      .mockRejectedValueOnce(new Error('connection closed before the operation response'))
      .mockImplementationOnce(async (projectId, intentId) => {
        result.operation.project_id = projectId
        result.operation.intent_id = intentId
        return result
      })

    await store.removeProject('orders-api')
    const uncertainIntent = removeProject.mock.calls[0][1]
    beforeEnqueue.resolve(mockSnapshot())
    await earlierRefresh

    await store.removeProject('orders-api')

    expect(removeProject.mock.calls[1][1]).toBe(uncertainIntent)
    expect(getSnapshot).toHaveBeenCalledTimes(3)
  })

  it('resumes the active unregister intent from a restart snapshot', async () => {
    const hydrated = mockSnapshot()
    hydrated.sequence = 44
    const active = structuredClone(harborWireFixture.remove_project.operation)
    active.project_id = 'orders-api'
    active.intent_id = 'desktop-existing-remove'
    hydrated.operations.push(active)
    vi.spyOn(harborBridge, 'getSnapshot').mockResolvedValue(hydrated)
    const store = useHarborStore()
    await store.initialize()
    expect(store.projectRemovalNotice('orders-api')).toEqual({
      state: 'requires_approval',
      title: 'Administrator approval required',
      message: 'Harbor paused removal until it can release this project’s local networking. Approve the one-time administrator action to continue.',
    })
    const result: ProjectUnregistration = structuredClone(harborWireFixture.remove_project)
    const removeProject = vi.spyOn(harborBridge, 'removeProject').mockImplementationOnce(async (projectId, intentId) => {
      result.operation.project_id = projectId
      result.operation.intent_id = intentId
      return result
    })

    await store.removeProject('orders-api')

    expect(removeProject).toHaveBeenCalledWith('orders-api', 'desktop-existing-remove')
  })

  it('retires an absent active intent from a lower-sequence reconnect baseline', async () => {
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
    const hydrated = mockSnapshot()
    hydrated.sequence = 44
    const active = structuredClone(harborWireFixture.remove_project.operation)
    active.project_id = 'orders-api'
    active.intent_id = 'desktop-before-restart'
    hydrated.operations.push(active)
    vi.spyOn(harborBridge, 'getSnapshot').mockResolvedValueOnce(hydrated)
    const store = useHarborStore()
    await store.initialize()

    connectionListener?.({ state: 'disconnected' })
    connectionListener?.({ state: 'connected' })
    const restarted = mockSnapshot()
    restarted.sequence = 3
    snapshotListener?.(restarted)

    expect(store.projectRemovalNotice('orders-api')).toEqual({
      state: 'incomplete',
      title: 'Project removal is no longer active',
      message: 'The project remains registered. You can try again.',
    })
  })

  it('retires an active intent when a newer active-only snapshot keeps the project registered', async () => {
    let snapshotListener: ((snapshot: HarborSnapshot) => void) | undefined
    vi.spyOn(harborBridge, 'subscribe').mockImplementation((listener) => {
      snapshotListener = listener
      return () => undefined
    })
    const hydrated = mockSnapshot()
    hydrated.sequence = 43
    const active = structuredClone(harborWireFixture.remove_project.operation)
    active.project_id = 'orders-api'
    active.intent_id = 'desktop-ended-remove'
    hydrated.operations.push(active)
    const getSnapshot = vi.spyOn(harborBridge, 'getSnapshot').mockResolvedValueOnce(hydrated)
    const store = useHarborStore()
    await store.initialize()

    const ended = mockSnapshot()
    ended.sequence = 44
    snapshotListener?.(ended)
    expect(store.projectRemovalNotice('orders-api')).toEqual({
      state: 'incomplete',
      title: 'Project removal is no longer active',
      message: 'The project remains registered. You can try again.',
    })

    const result: ProjectUnregistration = structuredClone(harborWireFixture.remove_project)
    const confirmed = structuredClone(ended)
    const removeProject = vi.spyOn(harborBridge, 'removeProject').mockImplementationOnce(async (projectId, intentId) => {
      result.operation.project_id = projectId
      result.operation.intent_id = intentId
      result.revision = 45
      confirmed.sequence = 45
      confirmed.operations.push(structuredClone(result.operation))
      return result
    })
    getSnapshot.mockResolvedValueOnce(confirmed)
    await store.removeProject('orders-api')

    expect(removeProject.mock.calls[0][1]).toMatch(/^desktop-project-remove-[0-9a-f]{32}$/)
    expect(removeProject.mock.calls[0][1]).not.toBe('desktop-ended-remove')
    expect(store.projectRemovalNotice('orders-api')?.state).toBe('requires_approval')
  })

  it('clears completed intent state before the same project identity is registered again', async () => {
    let snapshotListener: ((snapshot: HarborSnapshot) => void) | undefined
    vi.spyOn(harborBridge, 'subscribe').mockImplementation((listener) => {
      snapshotListener = listener
      return () => undefined
    })
    const hydrated = mockSnapshot()
    hydrated.sequence = 43
    const active = structuredClone(harborWireFixture.remove_project.operation)
    active.project_id = 'orders-api'
    active.intent_id = 'desktop-completed-remove'
    hydrated.operations.push(active)
    const getSnapshot = vi.spyOn(harborBridge, 'getSnapshot').mockResolvedValueOnce(hydrated)
    const store = useHarborStore()
    await store.initialize()

    const removed = mockSnapshot()
    removed.sequence = 44
    removed.projects = removed.projects.filter((project) => project.id !== 'orders-api')
    removed.operations = removed.operations.filter((operation) => operation.project_id !== 'orders-api')
    removed.recent_resource_ids = removed.recent_resource_ids.filter((reference) => reference.project_id !== 'orders-api')
    snapshotListener?.(removed)
    expect(store.projectById('orders-api')).toBeUndefined()
    expect(store.projectRemovalNotice('orders-api')).toBeUndefined()

    const registeredAgain = structuredClone(removed)
    registeredAgain.sequence = 45
    const orders = mockSnapshot().projects.find((project) => project.id === 'orders-api')
    if (!orders) throw new Error('orders fixture is missing')
    registeredAgain.projects.push(orders)
    snapshotListener?.(registeredAgain)

    const result: ProjectUnregistration = structuredClone(harborWireFixture.remove_project)
    const confirmed = structuredClone(registeredAgain)
    const removeProject = vi.spyOn(harborBridge, 'removeProject').mockImplementationOnce(async (projectId, intentId) => {
      result.operation.project_id = projectId
      result.operation.intent_id = intentId
      result.revision = 46
      confirmed.sequence = 46
      confirmed.operations.push(structuredClone(result.operation))
      return result
    })
    getSnapshot.mockResolvedValueOnce(confirmed)

    await store.removeProject('orders-api')

    expect(removeProject.mock.calls[0][1]).not.toBe('desktop-completed-remove')
  })

  it('reports global request serialization instead of silently dropping another removal', async () => {
    const pending = deferred<ProjectUnregistration>()
    const result: ProjectUnregistration = structuredClone(harborWireFixture.remove_project)
    const removeProject = vi.spyOn(harborBridge, 'removeProject').mockImplementationOnce(async (projectId, intentId) => {
      result.operation.project_id = projectId
      result.operation.intent_id = intentId
      return pending.promise
    })
    const store = useHarborStore()

    const removing = store.removeProject('orders-api')
    await vi.waitFor(() => expect(store.removingProjectId).toBe('orders-api'))

    await expect(store.removeProject('billing')).resolves.toBeNull()
    expect(removeProject).toHaveBeenCalledOnce()
    expect(store.projectRemovalNotice('billing')).toEqual({
      state: 'busy',
      title: 'Another project removal is in progress',
      message: 'Wait for the current removal request to finish, then try again.',
    })

    pending.resolve(result)
    await removing
    expect(store.removingProjectId).toBeNull()
  })

  it('shows the daemon-reviewed problem for a terminal removal failure', async () => {
    const store = useHarborStore()
    const result: ProjectUnregistration = structuredClone(harborWireFixture.remove_project)
    result.operation.state = 'failed'
    result.operation.phase = 'failed'
    result.operation.finished_at = '2026-07-18T14:40:02Z'
    result.operation.problem = {
      code: 'network_release_failed',
      message: 'Harbor could not verify that project networking was released.',
      retryable: true,
    }
    vi.spyOn(harborBridge, 'removeProject').mockResolvedValueOnce(result)

    await store.removeProject('orders-api')

    expect(store.projectRemovalNotice('orders-api')).toEqual({
      state: 'failed',
      title: 'Project removal failed',
      message: 'Harbor could not verify that project networking was released.',
    })
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
