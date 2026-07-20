import { flushPromises, mount } from '@vue/test-utils'
import { defineComponent, h, nextTick, ref } from 'vue'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import type { Problem, ServiceLogs } from '@/domain/harbor'
import { useServiceLogs } from './useServiceLogs'

type ReadServiceLogs = (projectId: string, sessionId: string, serviceId: string, cursor: number) => Promise<ServiceLogs>
type WaitServiceLogs = (projectId: string, sessionId: string, serviceId: string, cursor: number, waitMilliseconds: number) => Promise<ServiceLogs>

interface ServiceLogOptions {
  supported?: boolean
  available?: boolean
  outputAvailable?: boolean
  reset?: boolean
  truncated?: boolean
  hasMore?: boolean
  problem?: Problem
}

// serviceLogs keeps the response shape explicit so stream-state tests remain readable.
function serviceLogs(
  projectId: string,
  serviceId: string,
  sessionId: string,
  text: string,
  nextCursor: number,
  options: ServiceLogOptions = {},
): ServiceLogs {
  return {
    project_id: projectId,
    service_id: serviceId,
    session_id: sessionId || undefined,
    supported: options.supported ?? true,
    available: options.available ?? true,
    problem: options.problem,
    output: {
      available: options.outputAvailable ?? true,
      reset: options.reset ?? false,
      truncated: options.truncated ?? false,
      has_more: options.hasMore ?? false,
      next_cursor: nextCursor,
      text,
    },
  }
}

// deferred exposes a controlled promise for testing held cursor reads.
function deferred<T>() {
  let resolve!: (value: T) => void
  let reject!: (cause: unknown) => void
  const promise = new Promise<T>((nextResolve, nextReject) => {
    resolve = nextResolve
    reject = nextReject
  })
  return { promise, reject, resolve }
}

// never models the daemon's held wait without leaving timers behind.
function never<T>(): Promise<T> {
  return new Promise(() => {})
}

// mountServiceLogs hosts the composable inside a component so lifecycle fencing is exercised.
function mountServiceLogs(
  read: ReadServiceLogs,
  wait: WaitServiceLogs = () => never<ServiceLogs>(),
  supportsWait = true,
) {
  const projectId = ref('orders')
  const serviceId = ref('mysql')
  const supported = ref(true)
  const waitSupported = ref(supportsWait)
  const connected = ref(true)
  const snapshotSequence = ref<number | undefined>(1)
  let state!: ReturnType<typeof useServiceLogs>
  const wrapper = mount(defineComponent({
    setup() {
      state = useServiceLogs({ projectId, serviceId, supported, waitSupported, connected, snapshotSequence, read, wait })
      return () => h('div')
    },
  }))
  return { connected, projectId, serviceId, snapshotSequence, state, supported, waitSupported, wrapper }
}

// settle flushes both transport promises and Vue's reactive render queue.
async function settle() {
  await flushPromises()
  await nextTick()
}

describe('useServiceLogs', () => {
  beforeEach(() => {
    vi.useFakeTimers()
  })

  afterEach(() => {
    vi.clearAllTimers()
    vi.useRealTimers()
  })

  it('drains retained output before opening one cursor-addressed held read', async () => {
    const read = vi.fn<ReadServiceLogs>()
      .mockResolvedValueOnce(serviceLogs('orders', 'mysql', 'session-orders', 'first\n', 6, { hasMore: true }))
      .mockResolvedValueOnce(serviceLogs('orders', 'mysql', 'session-orders', 'second\n', 13))
    const wait = vi.fn<WaitServiceLogs>(() => never<ServiceLogs>())
    const { state, wrapper } = mountServiceLogs(read, wait)

    await settle()

    expect(read.mock.calls).toEqual([
      ['orders', '', 'mysql', 0],
      ['orders', 'session-orders', 'mysql', 6],
    ])
    expect(wait).toHaveBeenCalledWith('orders', 'session-orders', 'mysql', 13, 25_000)
    expect(state.output.value).toBe('first\nsecond\n')
    expect(state.state.value).toBe('live')
    wrapper.unmount()
  })

  it('retains visible output while reconnecting and resumes from the same cursor', async () => {
    const read = vi.fn<ReadServiceLogs>()
      .mockResolvedValueOnce(serviceLogs('orders', 'mysql', 'session-orders', 'ready\n', 6))
      .mockResolvedValueOnce(serviceLogs('orders', 'mysql', 'session-orders', 'again\n', 12))
    const wait = vi.fn<WaitServiceLogs>(() => never<ServiceLogs>())
    const { connected, state, wrapper } = mountServiceLogs(read, wait)
    await settle()

    connected.value = false
    await nextTick()
    expect(state.state.value).toBe('reconnecting')
    expect(state.output.value).toBe('ready\n')

    connected.value = true
    await settle()
    expect(read).toHaveBeenLastCalledWith('orders', 'session-orders', 'mysql', 6)
    expect(state.output.value).toBe('ready\nagain\n')
    expect(state.state.value).toBe('live')
    wrapper.unmount()
  })

  it('reports unsupported capability without starting a transport request', async () => {
    const read = vi.fn<ReadServiceLogs>()
    const { state, supported, wrapper } = mountServiceLogs(read)
    supported.value = false
    await settle()

    expect(read).not.toHaveBeenCalled()
    expect(state.state.value).toBe('unsupported')
    wrapper.unmount()
  })

  it('keeps the transcript when a live stream ends', async () => {
    const ended = deferred<ServiceLogs>()
    const read = vi.fn<ReadServiceLogs>().mockResolvedValue(serviceLogs('orders', 'mysql', 'session-orders', 'ready\n', 6))
    const wait = vi.fn<WaitServiceLogs>().mockImplementationOnce(() => ended.promise)
    const { state, wrapper } = mountServiceLogs(read, wait)
    await settle()

    ended.resolve(serviceLogs('orders', 'mysql', 'session-orders', '', 6, { outputAvailable: false }))
    await settle()

    expect(state.state.value).toBe('ended')
    expect(state.output.value).toBe('ready\n')
    expect(vi.getTimerCount()).toBe(0)
    wrapper.unmount()
  })

  it('rechecks a supported unavailable service until its container appears, then opens a held read', async () => {
    const read = vi.fn<ReadServiceLogs>()
      .mockResolvedValueOnce(serviceLogs('orders', 'mysql', 'session-current', '', 0, { available: false, outputAvailable: false }))
      .mockResolvedValueOnce(serviceLogs('orders', 'mysql', 'session-current', 'online\n', 7))
    const wait = vi.fn<WaitServiceLogs>(() => never<ServiceLogs>())
    const { state, wrapper } = mountServiceLogs(read, wait)
    await settle()

    expect(state.state.value).toBe('waiting')
    expect(state.error.value).toBeNull()
    expect(read).toHaveBeenCalledTimes(1)
    expect(vi.getTimerCount()).toBe(1)
    await vi.advanceTimersByTimeAsync(749)
    expect(read).toHaveBeenCalledTimes(1)
    await vi.advanceTimersByTimeAsync(1)
    await settle()

    expect(read.mock.calls).toEqual([
      ['orders', '', 'mysql', 0],
      ['orders', 'session-current', 'mysql', 0],
    ])
    expect(state.state.value).toBe('live')
    expect(state.output.value).toBe('online\n')
    expect(wait).toHaveBeenCalledWith('orders', 'session-current', 'mysql', 7, 25_000)
    expect(vi.getTimerCount()).toBe(0)
    wrapper.unmount()
  })

  it('adopts an unavailable replacement session before rechecking and never mixes its transcript', async () => {
    const replacement = deferred<ServiceLogs>()
    const read = vi.fn<ReadServiceLogs>()
      .mockResolvedValueOnce(serviceLogs('orders', 'mysql', 'session-old', 'old output\n', 11))
      .mockResolvedValueOnce(serviceLogs('orders', 'mysql', 'session-new', 'new output\n', 11, { reset: true }))
    const wait = vi.fn<WaitServiceLogs>()
      .mockImplementationOnce(() => replacement.promise)
      .mockImplementation(() => never<ServiceLogs>())
    const { state, wrapper } = mountServiceLogs(read, wait)
    await settle()
    const initialResetKey = state.outputResetKey.value

    replacement.resolve(serviceLogs(
      'orders',
      'mysql',
      'session-new',
      '',
      0,
      { available: false, outputAvailable: false, reset: true },
    ))
    await settle()

    expect(state.state.value).toBe('waiting')
    expect(state.output.value).toBe('')
    expect(state.outputResetKey.value).toBeGreaterThan(initialResetKey)
    expect(state.truncated.value).toBe(false)

    await vi.advanceTimersByTimeAsync(750)
    await settle()

    expect(read).toHaveBeenLastCalledWith('orders', 'session-new', 'mysql', 0)
    expect(state.state.value).toBe('live')
    expect(state.output.value).toBe('new output\n')
    expect(state.output.value).not.toContain('old output')
    expect(wait).toHaveBeenLastCalledWith('orders', 'session-new', 'mysql', 11, 25_000)
    wrapper.unmount()
  })

  it('never overlaps fallback rechecks while an unavailable-service read is in flight', async () => {
    const pending = deferred<ServiceLogs>()
    const unavailable = serviceLogs('orders', 'mysql', '', '', 0, { available: false, outputAvailable: false })
    const read = vi.fn<ReadServiceLogs>()
      .mockResolvedValueOnce(unavailable)
      .mockImplementationOnce(() => pending.promise)
    const { state, wrapper } = mountServiceLogs(read)
    await settle()

    await vi.advanceTimersByTimeAsync(750)
    expect(read).toHaveBeenCalledTimes(2)
    expect(vi.getTimerCount()).toBe(0)

    await vi.advanceTimersByTimeAsync(10_000)
    expect(read).toHaveBeenCalledTimes(2)

    pending.resolve(unavailable)
    await settle()
    expect(state.state.value).toBe('waiting')
    expect(read).toHaveBeenCalledTimes(2)
    expect(vi.getTimerCount()).toBe(1)
    wrapper.unmount()
  })

  it('surfaces typed failures and retries only retryable problems', async () => {
    const failure: Problem = { code: 'service.logs.failed', message: 'Compose stopped responding.', retryable: true }
    const read = vi.fn<ReadServiceLogs>()
      .mockResolvedValueOnce(serviceLogs('orders', 'mysql', '', '', 0, { problem: failure, available: false, outputAvailable: false }))
      .mockResolvedValueOnce(serviceLogs('orders', 'mysql', 'session-orders', 'recovered\n', 10))
    const { state, wrapper } = mountServiceLogs(read)
    await settle()

    expect(state.state.value).toBe('error')
    expect(state.error.value).toBe('Compose stopped responding.')
    await vi.advanceTimersByTimeAsync(2_000)
    await settle()

    expect(state.state.value).toBe('live')
    expect(state.error.value).toBeNull()
    expect(state.output.value).toBe('recovered\n')
    wrapper.unmount()
  })

  it('marks a retained-history gap and resets terminal presentation', async () => {
    const read = vi.fn<ReadServiceLogs>().mockResolvedValue(serviceLogs(
      'orders',
      'mysql',
      'session-orders',
      'newest\n',
      120,
      { reset: true, truncated: true },
    ))
    const { state, wrapper } = mountServiceLogs(read)
    await settle()

    expect(state.output.value).toBe('newest\n')
    expect(state.truncated.value).toBe(true)
    expect(state.outputResetKey.value).toBeGreaterThan(0)
    wrapper.unmount()
  })

  it('clears only local output while preserving the remote cursor', async () => {
    const next = deferred<ServiceLogs>()
    const read = vi.fn<ReadServiceLogs>().mockResolvedValue(serviceLogs('orders', 'mysql', 'session-orders', 'before\n', 7))
    const wait = vi.fn<WaitServiceLogs>().mockImplementationOnce(() => next.promise).mockImplementation(() => never<ServiceLogs>())
    const { state, wrapper } = mountServiceLogs(read, wait)
    await settle()

    state.clear()
    expect(state.output.value).toBe('')
    next.resolve(serviceLogs('orders', 'mysql', 'session-orders', 'after\n', 13))
    await settle()

    expect(state.output.value).toBe('after\n')
    expect(wait).toHaveBeenCalledWith('orders', 'session-orders', 'mysql', 7, 25_000)
    wrapper.unmount()
  })

  it('backs off a same-session response that repeats text without cursor progress', async () => {
    const read = vi.fn<ReadServiceLogs>()
      .mockResolvedValueOnce(serviceLogs('orders', 'mysql', 'session-orders', 'before\n', 7))
      .mockResolvedValueOnce(serviceLogs('orders', 'mysql', 'session-orders', 'duplicate\n', 7))
    const wait = vi.fn<WaitServiceLogs>().mockRejectedValueOnce(new Error('held read interrupted'))
    const { state, wrapper } = mountServiceLogs(read, wait)
    await settle()
    await vi.advanceTimersByTimeAsync(2_000)
    await settle()

    expect(state.error.value).toBe('Harbor returned service logs without advancing their byte cursor.')
    expect(state.output.value).toBe('before\n')
    expect(vi.getTimerCount()).toBe(1)
    wrapper.unmount()
  })
})
