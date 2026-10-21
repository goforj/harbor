import { DOMWrapper, flushPromises, mount } from '@vue/test-utils'
import { nextTick } from 'vue'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { copyText } from '@/bridge/clipboard'
import { harborWireFixture } from '@/bridge/harbor.fixture'
import type { SupportDiagnosticsSource } from '@/lib/supportDiagnostics'
import SupportDiagnostics from './SupportDiagnostics.vue'

vi.mock('@/bridge/clipboard', () => ({ copyText: vi.fn().mockResolvedValue(undefined) }))

function source(): SupportDiagnosticsSource {
  const snapshot = structuredClone(harborWireFixture.snapshot)
  return {
    connection: 'connected', stale: false, status: structuredClone(harborWireFixture.status), snapshot,
    projectId: snapshot.projects[0]!.id,
    activity: { ...structuredClone(harborWireFixture.project_activity), project_id: snapshot.projects[0]!.id },
    outputFresh: true, output: 'build failed\nDB_PASSWORD=secret-value',
  }
}

const body = () => new DOMWrapper(document.body)
const button = (label: string) => body().findAll('button').find(item => item.text() === label)!
let wrapper: ReturnType<typeof mount>

beforeEach(() => vi.mocked(copyText).mockReset().mockResolvedValue(undefined))
afterEach(() => { wrapper?.unmount(); document.body.innerHTML = '' })

async function open(current = source()) {
  wrapper = mount(SupportDiagnostics, { props: { source: current }, attachTo: document.body })
  await button('Support diagnostics').trigger('click')
  await flushPromises()
  return wrapper
}

describe('SupportDiagnostics', () => {
  it('shows the report before export, keeps output opt-in, and copies exactly the preview', async () => {
    await open()
    expect(copyText).not.toHaveBeenCalled()
    expect(body().get('pre').text()).not.toContain('build failed')
    await body().get('[role="checkbox"]').trigger('click')
    await flushPromises()
    const preview = body().get('pre').text()
    expect(preview).toContain('build failed')
    expect(preview).not.toContain('secret-value')
    await button('Copy report').trigger('click')
    await flushPromises()
    expect(copyText).toHaveBeenCalledExactlyOnceWith(preview)
    expect(button('Copied').exists()).toBe(true)
  })

  it('does not silently change the reviewed preview when logs append', async () => {
    const current = source()
    await open(current)
    await body().get('[role="checkbox"]').trigger('click')
    const preview = body().get('pre').text()
    await wrapper.setProps({ source: { ...current, output: 'new log entry' } })
    expect(body().get('pre').text()).toBe(preview)
    await button('Refresh preview').trigger('click')
    expect(body().get('pre').text()).toContain('new log entry')
  })

  it.each(['connection', 'stale', 'project', 'session', 'unavailable', 'reset', 'authorization', 'foreign', 'output-access'] as const)('invalidates reviewed text when %s changes', async change => {
    const current = source()
    await open(current)
    const next = structuredClone(current)
    if (change === 'connection') next.connection = 'disconnected'
    if (change === 'stale') next.stale = true
    if (change === 'project') next.projectId = 'different-project'
    if (change === 'session') next.activity!.session!.id = 'replacement-session'
    if (change === 'unavailable') next.outputFresh = false
    if (change === 'authorization') next.status!.capabilities = []
    if (change === 'foreign') next.activity!.project_id = 'foreign-project'
    if (change === 'output-access') {
      next.activity!.session!.output.available = false
      next.activity!.session!.output.historical = false
    }
    await wrapper.setProps({ source: next, outputResetKey: change === 'reset' ? 1 : undefined })
    expect(body().find('pre').exists()).toBe(false)
    expect(button('Copy report').attributes('disabled')).toBeDefined()
    await button('Copy report').trigger('click')
    expect(copyText).not.toHaveBeenCalled()
  })

  it('requires a new review after reconnect and does not restore an old preview automatically', async () => {
    const current = source()
    await open(current)
    await wrapper.setProps({ source: { ...current, stale: true } })
    await wrapper.setProps({ source: current })
    expect(body().find('pre').exists()).toBe(false)
    await button('Refresh preview').trigger('click')
    expect(body().find('pre').exists()).toBe(true)
  })

  it('keeps clipboard failures visible without displaying arbitrary error text', async () => {
    vi.mocked(copyText).mockRejectedValueOnce(new Error('secret clipboard implementation details'))
    await open()
    await button('Copy report').trigger('click')
    await flushPromises()
    expect(body().get('[role="alert"]').text()).toContain('Could not copy diagnostics')
    expect(body().text()).not.toContain('secret clipboard')
  })

  it('suppresses late copied feedback after session replacement', async () => {
    let resolve!: () => void
    vi.mocked(copyText).mockReturnValueOnce(new Promise<void>(done => { resolve = done }))
    const current = source()
    await open(current)
    await button('Copy report').trigger('click')
    await wrapper.setProps({ source: { ...current, outputFresh: false } })
    resolve()
    await flushPromises()
    expect(body().findAll('button').some(item => item.text() === 'Copied')).toBe(false)
  })

  it('offers no preview when disconnected and clears optional output on reopen', async () => {
    const current = source()
    await open({ ...current, connection: 'disconnected' })
    expect(body().find('pre').exists()).toBe(false)
    await wrapper.setProps({ source: current })
    await button('Refresh preview').trigger('click')
    await body().get('[role="checkbox"]').trigger('click')
    await button('Close').trigger('click')
    await nextTick()
    await button('Support diagnostics').trigger('click')
    await flushPromises()
    expect(body().get('pre').text()).toContain('Development output: not included.')
  })
})
