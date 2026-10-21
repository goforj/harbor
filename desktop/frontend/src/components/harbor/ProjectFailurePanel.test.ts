import { flushPromises, mount } from '@vue/test-utils'
import { describe, expect, it, vi } from 'vitest'
import { copyText } from '@/bridge/clipboard'
import { harborWireFixture } from '@/bridge/harbor.fixture'
import ProjectFailurePanel from './ProjectFailurePanel.vue'

vi.mock('@/bridge/clipboard', () => ({ copyText: vi.fn().mockResolvedValue(undefined) }))

const props = {
  projectName: 'Test App',
  message: 'The development runtime exited unexpectedly.',
  code: 'project.process.exited',
  output: 'error: missing migration\nDB_PASSWORD="private-value"',
  truncated: false,
  retryAvailable: true,
  retryDisabled: false,
}

describe('ProjectFailurePanel', () => {
  it('keeps details open across activity refreshes and resets for a replacement session', async () => {
    const session = structuredClone(harborWireFixture.project_activity.session)
    const wrapper = mount(ProjectFailurePanel, { props: { ...props, session } })
    await wrapper.findAll('button').find(button => button.text() === 'Show details')!.trigger('click')
    await wrapper.setProps({ session: { ...session } })
    expect(wrapper.find('pre').exists()).toBe(true)
    await wrapper.setProps({ session: { ...session, id: 'replacement-session' } })
    expect(wrapper.find('pre').exists()).toBe(false)
    wrapper.unmount()
  })

  it('discloses a bounded redacted excerpt and offers existing output navigation', async () => {
    const wrapper = mount(ProjectFailurePanel, { props })
    expect(wrapper.find('pre').exists()).toBe(false)
    await wrapper.findAll('button').find(button => button.text() === 'Show details')!.trigger('click')
    expect(wrapper.get('pre').text()).toContain('missing migration')
    expect(wrapper.get('pre').text()).not.toContain('private-value')
    await wrapper.findAll('button').find(button => button.text() === 'View development output')!.trigger('click')
    expect(wrapper.emitted('viewOutput')).toHaveLength(1)
    wrapper.unmount()
  })

  it('copies the same redacted context without raw credentials', async () => {
    const wrapper = mount(ProjectFailurePanel, { props })
    await wrapper.findAll('button').find(button => button.text() === 'Copy failure details')!.trigger('click')
    await flushPromises()
    const report = vi.mocked(copyText).mock.lastCall![0]
    expect(report).toContain('Test App')
    expect(report).toContain('missing migration')
    expect(report).not.toContain('private-value')
    expect(wrapper.text()).toContain('Copied')
    wrapper.unmount()
  })

  it('does not dispatch retry when the existing lifecycle guards disable it', async () => {
    const wrapper = mount(ProjectFailurePanel, { props: { ...props, retryDisabled: true } })
    const retry = wrapper.findAll('button').find(button => button.text() === 'Retry start')!
    expect(retry.attributes('disabled')).toBeDefined()
    await retry.trigger('click')
    expect(wrapper.emitted('retry')).toBeUndefined()
    await wrapper.setProps({ retryDisabled: false })
    await retry.trigger('click')
    expect(wrapper.emitted('retry')).toHaveLength(1)
    wrapper.unmount()
  })

  it('explains missing output and surfaces clipboard errors', async () => {
    vi.mocked(copyText).mockRejectedValueOnce(new Error('Clipboard unavailable'))
    const wrapper = mount(ProjectFailurePanel, { props: { ...props, output: '' } })
    await wrapper.findAll('button').find(button => button.text() === 'Show details')!.trigger('click')
    expect(wrapper.text()).toContain('No launch output is available for this session')
    await wrapper.findAll('button').find(button => button.text() === 'Copy failure details')!.trigger('click')
    await flushPromises()
    expect(wrapper.text()).toContain('Clipboard unavailable')
    wrapper.unmount()
  })
})
