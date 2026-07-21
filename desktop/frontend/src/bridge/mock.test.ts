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
      approve_project_removal: 'ApproveProjectRemoval',
      confirm_project_runtime_repair: 'ConfirmProjectRuntimeRepair',
      inspect_project_runtime_repair: 'InspectProjectRuntimeRepair',
      open_resource: 'OpenResource',
      open_terminal_url: 'OpenTerminalURL',
      project_activity: 'ProjectActivity',
      resource_icon_url: 'ResourceIconURL',
      service_logs: 'ServiceLogs',
      wait_project_activity: 'WaitProjectActivity',
      wait_service_logs: 'WaitServiceLogs',
      remove_project: 'RemoveProject',
      snapshot: 'Snapshot',
      setup_network: 'SetupNetwork',
      start_project: 'StartProject',
      restart_project: 'RestartProject',
      status: 'Status',
      stop_project: 'StopProject',
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

  it('streams only the current mock session with byte-addressed cursors', async () => {
    const bridge = createMockBridge()

    const first = await bridge.getProjectActivity('orders-api', '', 0)
    expect(first).toEqual(harborWireFixture.project_activity)
    const session = first.session
    expect(session).toBeDefined()
    if (!session) return

    const complete = await bridge.getProjectActivity('orders-api', session.id, session.output.next_cursor)
    expect(complete.session?.output).toMatchObject({ text: '', has_more: false, next_cursor: session.output.next_cursor })

    const changed = await bridge.getProjectActivity('orders-api', 'session-prior', 20)
    expect(changed.session?.output).toMatchObject({ reset: true, text: harborWireFixture.project_activity.session.output.text })
    const future = await bridge.getProjectActivity('orders-api', session.id, session.output.next_cursor + 1)
    expect(future.session?.output).toMatchObject({ reset: true, text: harborWireFixture.project_activity.session.output.text })
    await expect(bridge.getProjectActivity('missing', '', 0)).rejects.toThrow('Unknown project')
    await expect(bridge.getProjectActivity('reports', '', 0)).resolves.toEqual({ project_id: 'reports' })
  })

  it('completes mock network setup once and safely replays the result', async () => {
    const bridge = createMockBridge()

    const completed = await bridge.setupNetwork()
    const replayed = await bridge.setupNetwork()
    const snapshot = await bridge.getSnapshot()

    expect(completed).toMatchObject({
      revision: 43,
      operation: {
        intent_id: 'intent-network-setup',
        kind: 'network.setup',
        state: 'succeeded',
      },
    })
    expect(replayed).toEqual(completed)
    expect(snapshot.sequence).toBe(43)
    expect(snapshot.operations).not.toContainEqual(expect.objectContaining({ kind: 'network.setup' }))
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

  it('reports required approval without removing an active project before explicit approval', async () => {
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

  it('completes one retained project removal after approval and replays the terminal result', async () => {
    const bridge = createMockBridge()

    const pending = await bridge.removeProject('orders-api', 'desktop-remove-orders')
    const approved = await bridge.approveProjectRemoval('orders-api', 'desktop-remove-orders')
    const replayed = await bridge.approveProjectRemoval('orders-api', 'desktop-remove-orders')
    const snapshot = await bridge.getSnapshot()

    expect(pending.operation.state).toBe('requires_approval')
    expect(approved).toMatchObject({
      revision: 44,
      operation: {
        id: pending.operation.id,
        intent_id: 'desktop-remove-orders',
        project_id: 'orders-api',
        state: 'succeeded',
        phase: 'completed',
      },
    })
    expect(replayed).toEqual(approved)
    expect(snapshot.projects.some((project) => project.id === 'orders-api')).toBe(false)
    expect(snapshot.operations.some((operation) => operation.project_id === 'orders-api')).toBe(false)
  })

  it('starts and stops projects through authoritative mock snapshots with replayable intents', async () => {
    const startBridge = createMockBridge()

    const started = await startBridge.startProject('reports', 'desktop-start-reports')
    const replayedStart = await startBridge.startProject('reports', 'desktop-start-reports')
    const startedSnapshot = await startBridge.getSnapshot()

    expect(started).toMatchObject({
      operation: {
        kind: 'project.start',
        project_id: 'reports',
        intent_id: 'desktop-start-reports',
        state: 'queued',
      },
    })
    expect(replayedStart).toEqual(started)
    expect(startedSnapshot.projects.find((project) => project.id === 'reports')?.state).toBe('starting')

    const stopBridge = createMockBridge()
    const stopped = await stopBridge.stopProject('orders-api', 'desktop-stop-orders')
    const stoppedSnapshot = await stopBridge.getSnapshot()

    expect(stopped.operation).toMatchObject({
      kind: 'project.stop',
      project_id: 'orders-api',
      intent_id: 'desktop-stop-orders',
      state: 'queued',
    })
    expect(stoppedSnapshot.projects.find((project) => project.id === 'orders-api')?.state).toBe('stopping')
  })

  it('does not let one lifecycle intent cross project or action boundaries', async () => {
    const bridge = createMockBridge()
    await bridge.startProject('reports', 'desktop-lifecycle-replay')

    await expect(bridge.startProject('billing', 'desktop-lifecycle-replay')).rejects.toThrow('another project action')
    await expect(bridge.stopProject('reports', 'desktop-lifecycle-replay')).rejects.toThrow('another project action')
  })

  it('requires inspection before one opaque stale-runtime confirmation and consumes the plan on every attempt', async () => {
    const bridge = createMockBridge()
    await expect(bridge.confirmProjectRuntimeRepair('billing', 'missing', 'missing')).rejects.toThrow('no longer available')

    const mismatched = await bridge.inspectProjectRuntimeRepair('billing')
    expect(mismatched).toMatchObject({
      project_id: 'billing',
      disposition: 'confirmable',
      confirmable: {
        candidate: {
          command: 'forj dev',
          checkout: '/workspace/apps/billing',
        },
      },
    })
    if (mismatched.disposition !== 'confirmable') return
    await expect(bridge.confirmProjectRuntimeRepair(
      'billing',
      mismatched.confirmable.inspection_id,
      'wrong-fingerprint',
    )).rejects.toThrow('does not match')
    await expect(bridge.confirmProjectRuntimeRepair(
      'billing',
      mismatched.confirmable.inspection_id,
      mismatched.confirmable.candidate_fingerprint,
    )).rejects.toThrow('no longer available')

    const inspected = await bridge.inspectProjectRuntimeRepair('billing')
    if (inspected.disposition !== 'confirmable') return
    const confirmed = await bridge.confirmProjectRuntimeRepair(
      'billing',
      inspected.confirmable.inspection_id,
      inspected.confirmable.candidate_fingerprint,
    )
    const snapshot = await bridge.getSnapshot()

    expect(confirmed).toMatchObject({ project: { id: 'billing', state: 'stopped' } })
    expect(snapshot.projects.find((project) => project.id === 'billing')?.state).toBe('stopped')
    await expect(bridge.confirmProjectRuntimeRepair(
      'billing',
      inspected.confirmable.inspection_id,
      inspected.confirmable.candidate_fingerprint,
    )).rejects.toThrow('no longer available')
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
