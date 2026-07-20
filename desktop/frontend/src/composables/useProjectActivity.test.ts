import { flushPromises, mount } from '@vue/test-utils'
import { defineComponent, h, nextTick, ref } from 'vue'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import type { ProjectActivity } from '@/domain/harbor'
import { useProjectActivity } from './useProjectActivity'

type ReadProjectActivity = (projectId: string, sessionId: string, cursor: number) => Promise<ProjectActivity>

interface ActivityChunkOptions {
  available?: boolean
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

function mountProjectActivity(read: ReadProjectActivity) {
  const projectId = ref('orders')
  const supported = ref(true)
  const connected = ref(true)
  const snapshotSequence = ref<number | undefined>(1)
  let state!: ReturnType<typeof useProjectActivity>
  const wrapper = mount(defineComponent({
    setup() {
      state = useProjectActivity({ projectId, supported, connected, snapshotSequence, read })
      return () => h('div')
    },
  }))
  return { connected, projectId, snapshotSequence, state, supported, wrapper }
}

async function runReadyTimers(milliseconds = 0) {
  await vi.advanceTimersByTimeAsync(milliseconds)
  await flushPromises()
}

describe('useProjectActivity', () => {
  beforeEach(() => {
    vi.useFakeTimers()
  })

  afterEach(() => {
    vi.clearAllTimers()
    vi.useRealTimers()
  })

  it('assembles every immediately retained chunk using the returned cursor', async () => {
    const read = vi.fn<ReadProjectActivity>()
      .mockResolvedValueOnce(projectActivity('orders', 'session-orders', 'first\n', 6, { hasMore: true }))
      .mockResolvedValueOnce(projectActivity('orders', 'session-orders', 'second\n', 13))
    const { state, wrapper } = mountProjectActivity(read)

    await runReadyTimers()

    expect(read.mock.calls).toEqual([
      ['orders', '', 0],
      ['orders', 'session-orders', 6],
    ])
    expect(state.output.value).toBe('first\nsecond\n')
    expect(state.activity.value?.session?.output.next_cursor).toBe(13)
    wrapper.unmount()
  })

  it('coalesces a snapshot refresh while a cursor read is in flight', async () => {
    const pending = deferred<ProjectActivity>()
    const read = vi.fn<ReadProjectActivity>()
      .mockImplementationOnce(() => pending.promise)
      .mockResolvedValueOnce(projectActivity('orders', 'session-orders', '', 1))
    const { snapshotSequence, state, wrapper } = mountProjectActivity(read)

    await runReadyTimers()
    expect(read).toHaveBeenCalledTimes(1)

    snapshotSequence.value = 2
    await nextTick()
    await runReadyTimers()
    expect(read).toHaveBeenCalledTimes(1)

    pending.resolve(projectActivity('orders', 'session-orders', 'A', 1))
    await flushPromises()
    expect(state.output.value).toBe('A')
    expect(read).toHaveBeenCalledTimes(1)

    await runReadyTimers()
    expect(read.mock.calls).toEqual([
      ['orders', '', 0],
      ['orders', 'session-orders', 1],
    ])
    expect(state.output.value).toBe('A')
    wrapper.unmount()
  })

  it('preserves a same-session cursor while its supervised output is unavailable', async () => {
    const read = vi.fn<ReadProjectActivity>()
      .mockResolvedValueOnce(projectActivity('orders', 'session-orders', 'A', 1))
      .mockResolvedValueOnce(projectActivity('orders', 'session-orders', '', 0, { available: false }))
      .mockResolvedValueOnce(projectActivity('orders', 'session-orders', 'B', 2))
    const { state, wrapper } = mountProjectActivity(read)

    await runReadyTimers()
    await runReadyTimers(750)
    expect(state.output.value).toBe('A')
    await runReadyTimers(750)

    expect(read.mock.calls).toEqual([
      ['orders', '', 0],
      ['orders', 'session-orders', 1],
      ['orders', 'session-orders', 1],
    ])
    expect(state.output.value).toBe('AB')
    wrapper.unmount()
  })

  it('replaces output for session changes, resets, and retained-history truncation', async () => {
    const read = vi.fn<ReadProjectActivity>()
      .mockResolvedValueOnce(projectActivity('orders', 'session-one', 'old output', 10))
      .mockResolvedValueOnce(projectActivity('orders', 'session-two', 'new session', 11, { reset: true }))
      .mockResolvedValueOnce(projectActivity('orders', 'session-two', 'reset output', 12, { reset: true }))
      .mockResolvedValueOnce(projectActivity('orders', 'session-two', 'retained tail', 20, { truncated: true }))
    const { state, wrapper } = mountProjectActivity(read)

    await runReadyTimers()
    expect(state.output.value).toBe('old output')
    await runReadyTimers(750)
    expect(state.output.value).toBe('new session')
    expect(state.truncated.value).toBe(false)
    await runReadyTimers(750)
    expect(state.output.value).toBe('reset output')
    expect(state.truncated.value).toBe(false)
    await runReadyTimers(750)
    expect(state.output.value).toBe('retained tail')
    expect(state.truncated.value).toBe(true)
    wrapper.unmount()
  })

  it('clears project state and fences a prior project result', async () => {
    const orders = deferred<ProjectActivity>()
    const billing = deferred<ProjectActivity>()
    const read = vi.fn<ReadProjectActivity>((projectId) => projectId === 'orders' ? orders.promise : billing.promise)
    const { projectId, state, wrapper } = mountProjectActivity(read)

    await runReadyTimers()
    expect(read).toHaveBeenLastCalledWith('orders', '', 0)

    projectId.value = 'billing'
    await nextTick()
    expect(state.output.value).toBe('')
    expect(state.activity.value).toBeNull()
    await runReadyTimers()
    expect(read).toHaveBeenLastCalledWith('billing', '', 0)

    billing.resolve(projectActivity('billing', 'session-billing', 'billing output', 14))
    await flushPromises()
    expect(state.output.value).toBe('billing output')

    orders.resolve(projectActivity('orders', 'session-orders', 'stale orders output', 19))
    await flushPromises()
    expect(state.output.value).toBe('billing output')
    expect(state.activity.value?.project_id).toBe('billing')
    wrapper.unmount()
  })

  it('discards an in-flight result and leaves no timer after unmount', async () => {
    const pending = deferred<ProjectActivity>()
    const read = vi.fn<ReadProjectActivity>(() => pending.promise)
    const { state, wrapper } = mountProjectActivity(read)

    await runReadyTimers()
    expect(read).toHaveBeenCalledTimes(1)
    wrapper.unmount()

    pending.resolve(projectActivity('orders', 'session-orders', 'late output', 11))
    await flushPromises()
    await runReadyTimers(10_000)

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

    await runReadyTimers()

    expect(read).toHaveBeenCalledTimes(5)
    expect(state.output.value).toHaveLength(256 * 1024 - 1)
    expect(state.output.value.startsWith('B')).toBe(true)
    expect(state.output.value.endsWith('E')).toBe(true)
    expect(state.truncated.value).toBe(true)
    expect(state.output.value).not.toContain('�')
    wrapper.unmount()
  })
})
