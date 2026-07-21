import { flushPromises, mount } from '@vue/test-utils'
import { defineComponent, h, nextTick, ref } from 'vue'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import type { ProjectActivity } from '@/domain/harbor'
import { useProjectActivity } from './useProjectActivity'

type ReadProjectActivity = (projectId: string, sessionId: string, cursor: number) => Promise<ProjectActivity>
type WaitProjectActivity = (projectId: string, sessionId: string, cursor: number, waitMilliseconds: number) => Promise<ProjectActivity>

interface ActivityChunkOptions {
  available?: boolean
  historical?: boolean
  reset?: boolean
  truncated?: boolean
  hasMore?: boolean
}

function projectActivity(
  projectId: string,
  sessionId: string,
  text: string,
  nextCursor: number,
  options: ActivityChunkOptions = {},
): ProjectActivity {
  return {
    project_id: projectId,
    session: {
      id: sessionId,
      state: 'attached',
      generation: 1,
      output: {
        available: options.available ?? true,
        historical: options.historical,
        reset: options.reset ?? false,
        truncated: options.truncated ?? false,
        has_more: options.hasMore ?? false,
        next_cursor: nextCursor,
        text,
      },
    },
  }
}

function deferred<T>() {
  let resolve!: (value: T) => void
  let reject!: (cause: unknown) => void
  const promise = new Promise<T>((nextResolve, nextReject) => {
    resolve = nextResolve
    reject = nextReject
  })
  return { promise, reject, resolve }
}

function never<T>(): Promise<T> {
  return new Promise(() => {})
}

function mountProjectActivity(
  read: ReadProjectActivity,
  wait: WaitProjectActivity = () => never<ProjectActivity>(),
  supportsWait = true,
) {
  const projectId = ref('orders')
  const supported = ref(true)
  const waitSupported = ref(supportsWait)
  const connected = ref(true)
  const snapshotSequence = ref<number | undefined>(1)
  let state!: ReturnType<typeof useProjectActivity>
  const wrapper = mount(defineComponent({
    setup() {
      state = useProjectActivity({ projectId, supported, waitSupported, connected, snapshotSequence, read, wait })
      return () => h('div')
    },
  }))
  return { connected, projectId, snapshotSequence, state, supported, waitSupported, wrapper }
}

async function settle() {
  await flushPromises()
  await nextTick()
}

describe('useProjectActivity', () => {
  beforeEach(() => {
    vi.useFakeTimers()
  })

  afterEach(() => {
    vi.clearAllTimers()
    vi.useRealTimers()
  })

  it('drains retained chunks and immediately opens one cursor-addressed long poll', async () => {
    const read = vi.fn<ReadProjectActivity>()
      .mockResolvedValueOnce(projectActivity('orders', 'session-orders', 'first\n', 6, { hasMore: true }))
      .mockResolvedValueOnce(projectActivity('orders', 'session-orders', 'second\n', 13))
    const wait = vi.fn<WaitProjectActivity>(() => never<ProjectActivity>())
    const { state, wrapper } = mountProjectActivity(read, wait)

    await settle()

    expect(read.mock.calls).toEqual([
      ['orders', '', 0],
      ['orders', 'session-orders', 6],
    ])
    expect(wait).toHaveBeenCalledWith('orders', 'session-orders', 13, 25_000)
    expect(state.output.value).toBe('first\nsecond\n')
    wrapper.unmount()
  })

  it('appends each completed live wait and begins the next wait without a polling timer', async () => {
    const firstWait = deferred<ProjectActivity>()
    const secondWait = deferred<ProjectActivity>()
    const read = vi.fn<ReadProjectActivity>().mockResolvedValue(projectActivity('orders', 'session-orders', 'ready\n', 6))
    const wait = vi.fn<WaitProjectActivity>()
      .mockImplementationOnce(() => firstWait.promise)
      .mockImplementationOnce(() => secondWait.promise)
    const { state, wrapper } = mountProjectActivity(read, wait)
    await settle()

    firstWait.resolve(projectActivity('orders', 'session-orders', 'live\n', 11))
    await settle()

    expect(state.output.value).toBe('ready\nlive\n')
    expect(wait.mock.calls).toEqual([
      ['orders', 'session-orders', 6, 25_000],
      ['orders', 'session-orders', 11, 25_000],
    ])
    expect(vi.getTimerCount()).toBe(0)
    wrapper.unmount()
  })

  it('coalesces a snapshot refresh into an immediate backfill after an active wait', async () => {
    const pending = deferred<ProjectActivity>()
    const read = vi.fn<ReadProjectActivity>()
      .mockResolvedValueOnce(projectActivity('orders', 'session-orders', 'A', 1))
      .mockResolvedValueOnce(projectActivity('orders', 'session-orders', 'B', 2))
    const wait = vi.fn<WaitProjectActivity>().mockImplementationOnce(() => pending.promise)
    const { snapshotSequence, state, wrapper } = mountProjectActivity(read, wait)
    await settle()

    snapshotSequence.value = 2
    await nextTick()
    expect(read).toHaveBeenCalledTimes(1)

    pending.resolve(projectActivity('orders', 'session-orders', '', 1))
    await settle()

    expect(read.mock.calls.at(-1)).toEqual(['orders', 'session-orders', 1])
    expect(state.output.value).toBe('AB')
    wrapper.unmount()
  })

  it('keeps 750 millisecond cursor polling against an older daemon', async () => {
    const read = vi.fn<ReadProjectActivity>()
      .mockResolvedValueOnce(projectActivity('orders', 'session-orders', 'A', 1))
      .mockResolvedValueOnce(projectActivity('orders', 'session-orders', 'B', 2))
    const wait = vi.fn<WaitProjectActivity>()
    const { state, wrapper } = mountProjectActivity(read, wait, false)
    await settle()

    expect(read).toHaveBeenCalledTimes(1)
    await vi.advanceTimersByTimeAsync(749)
    expect(read).toHaveBeenCalledTimes(1)
    await vi.advanceTimersByTimeAsync(1)
    await settle()

    expect(read).toHaveBeenCalledTimes(2)
    expect(wait).not.toHaveBeenCalled()
    expect(state.output.value).toBe('AB')
    wrapper.unmount()
  })

  it('waits for a snapshot instead of spinning when no live session exists', async () => {
    const read = vi.fn<ReadProjectActivity>()
      .mockResolvedValueOnce({ project_id: 'orders' })
      .mockResolvedValueOnce(projectActivity('orders', 'session-orders', 'online', 6))
    const wait = vi.fn<WaitProjectActivity>(() => never<ProjectActivity>())
    const { snapshotSequence, wrapper } = mountProjectActivity(read, wait)
    await settle()

    expect(read).toHaveBeenCalledTimes(1)
    expect(wait).not.toHaveBeenCalled()
    expect(vi.getTimerCount()).toBe(0)

    snapshotSequence.value = 2
    await settle()

    expect(read).toHaveBeenCalledTimes(2)
    expect(wait).toHaveBeenCalledWith('orders', 'session-orders', 6, 25_000)
    wrapper.unmount()
  })

  it('backs off an invalid early wait response that makes no cursor progress', async () => {
    const read = vi.fn<ReadProjectActivity>()
      .mockResolvedValue(projectActivity('orders', 'session-orders', 'ready', 5))
    const wait = vi.fn<WaitProjectActivity>()
      .mockResolvedValue(projectActivity('orders', 'session-orders', '', 5))
    const { wrapper } = mountProjectActivity(read, wait)
    await settle()

    expect(wait).toHaveBeenCalledTimes(1)
    expect(vi.getTimerCount()).toBe(1)
    await vi.advanceTimersByTimeAsync(99)
    expect(read).toHaveBeenCalledTimes(1)
    await vi.advanceTimersByTimeAsync(1)
    await settle()
    expect(read).toHaveBeenCalledTimes(2)
    wrapper.unmount()
  })

  it('retries only after a transport error and clears the error after recovery', async () => {
    const read = vi.fn<ReadProjectActivity>()
      .mockResolvedValueOnce(projectActivity('orders', 'session-orders', 'A', 1))
      .mockResolvedValueOnce(projectActivity('orders', 'session-orders', 'B', 2))
    const wait = vi.fn<WaitProjectActivity>()
      .mockRejectedValueOnce(new Error('connection interrupted'))
      .mockImplementation(() => never<ProjectActivity>())
    const { state, wrapper } = mountProjectActivity(read, wait)
    await settle()

    expect(state.error.value).toBe('connection interrupted')
    expect(vi.getTimerCount()).toBe(1)
    await vi.advanceTimersByTimeAsync(2_000)
    await settle()

    expect(read).toHaveBeenLastCalledWith('orders', 'session-orders', 1)
    expect(state.output.value).toBe('AB')
    expect(state.error.value).toBeNull()
    wrapper.unmount()
  })

  it('preserves a same-session cursor while its supervised output is unavailable', async () => {
    const unavailable = deferred<ProjectActivity>()
    const read = vi.fn<ReadProjectActivity>()
      .mockResolvedValueOnce(projectActivity('orders', 'session-orders', 'A', 1))
      .mockResolvedValueOnce(projectActivity('orders', 'session-orders', 'B', 2))
    const wait = vi.fn<WaitProjectActivity>().mockImplementationOnce(() => unavailable.promise)
    const { snapshotSequence, state, wrapper } = mountProjectActivity(read, wait)
    await settle()

    snapshotSequence.value = 2
    unavailable.resolve(projectActivity('orders', 'session-orders', '', 0, { available: false }))
    await settle()

    expect(read).toHaveBeenLastCalledWith('orders', 'session-orders', 1)
    expect(state.output.value).toBe('AB')
    wrapper.unmount()
  })

  it('shows retained history as non-live and replaces it when live output resumes', async () => {
    const read = vi.fn<ReadProjectActivity>()
      .mockResolvedValueOnce(projectActivity('orders', 'session-orders', 'retained\n', 9, { available: false, historical: true }))
      .mockResolvedValueOnce(projectActivity('orders', 'session-orders', 'live\n', 5))
    const wait = vi.fn<WaitProjectActivity>(() => never<ProjectActivity>())
    const { state, snapshotSequence, wrapper } = mountProjectActivity(read, wait)

    await settle()
    expect(state.output.value).toBe('retained\n')
    expect(state.activity.value?.session?.output.historical).toBe(true)

    snapshotSequence.value = 2
    await settle()
    expect(state.output.value).toBe('live\n')
    expect(state.activity.value?.session?.output.historical).toBeUndefined()
    wrapper.unmount()
  })

  it('replaces output for session changes, resets, and retained-history truncation', async () => {
    const read = vi.fn<ReadProjectActivity>()
      .mockResolvedValueOnce(projectActivity('orders', 'session-one', 'old output', 10, { hasMore: true }))
      .mockResolvedValueOnce(projectActivity('orders', 'session-two', 'new session', 11, { reset: true, hasMore: true }))
      .mockResolvedValueOnce(projectActivity('orders', 'session-two', 'reset output', 12, { reset: true, hasMore: true }))
      .mockResolvedValueOnce(projectActivity('orders', 'session-two', 'retained tail', 20, { truncated: true }))
    const { state, wrapper } = mountProjectActivity(read)

    await settle()

    expect(state.output.value).toBe('retained tail')
    expect(state.truncated.value).toBe(true)
    wrapper.unmount()
  })

  it('clears project state and fences a prior project result', async () => {
    const orders = deferred<ProjectActivity>()
    const billing = deferred<ProjectActivity>()
    const read = vi.fn<ReadProjectActivity>((projectId) => projectId === 'orders' ? orders.promise : billing.promise)
    const { projectId, state, wrapper } = mountProjectActivity(read)
    await settle()

    projectId.value = 'billing'
    await nextTick()
    await settle()
    expect(state.output.value).toBe('')

    billing.resolve(projectActivity('billing', 'session-billing', 'billing output', 14))
    await settle()
    orders.resolve(projectActivity('orders', 'session-orders', 'stale orders output', 19))
    await settle()

    expect(state.output.value).toBe('billing output')
    expect(state.activity.value?.project_id).toBe('billing')
    wrapper.unmount()
  })

  it('discards an in-flight result and leaves no timer after unmount', async () => {
    const pending = deferred<ProjectActivity>()
    const read = vi.fn<ReadProjectActivity>(() => pending.promise)
    const { state, wrapper } = mountProjectActivity(read)
    await settle()
    wrapper.unmount()

    pending.resolve(projectActivity('orders', 'session-orders', 'late output', 11))
    await settle()
    await vi.advanceTimersByTimeAsync(10_000)

    expect(read).toHaveBeenCalledTimes(1)
    expect(state.activity.value).toBeNull()
    expect(state.output.value).toBe('')
    expect(vi.getTimerCount()).toBe(0)
  })

  it('bounds visible output and preserves a complete JavaScript string', async () => {
    const chunkSize = 64 * 1024
    const texts = [
      `${'A'.repeat(chunkSize - 6)}😀`,
      'B'.repeat(chunkSize),
      'C'.repeat(chunkSize),
      'D'.repeat(chunkSize),
      'E'.repeat(chunkSize - 1),
    ]
    let cursor = 0
    const chunks = texts.map((text, index) => {
      cursor += new TextEncoder().encode(text).length
      return projectActivity('orders', 'session-orders', text, cursor, { hasMore: index < texts.length - 1 })
    })
    const read = vi.fn<ReadProjectActivity>()
    for (const chunk of chunks) read.mockResolvedValueOnce(chunk)
    const { state, wrapper } = mountProjectActivity(read)

    await settle()

    expect(read).toHaveBeenCalledTimes(5)
    expect(state.output.value).toHaveLength(192 * 1024)
    expect(state.output.value.startsWith('B')).toBe(true)
    expect(state.output.value.endsWith('E')).toBe(true)
    expect(state.truncated.value).toBe(true)
    expect(state.output.value).not.toContain('�')
    wrapper.unmount()
  })
})
