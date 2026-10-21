import { flushPromises, mount } from '@vue/test-utils'
import { nextTick } from 'vue'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { copyText } from '@/bridge/clipboard'
import LogViewer from './LogViewer.vue'

vi.mock('@/bridge/clipboard', () => ({ copyText: vi.fn() }))

describe('LogViewer', () => {
  let frames: FrameRequestCallback[]
  beforeEach(() => {
    frames = []
    vi.spyOn(window, 'requestAnimationFrame').mockImplementation(callback => { frames.push(callback); return frames.length })
    vi.spyOn(window, 'cancelAnimationFrame').mockImplementation(() => {})
  })

  // paint flushes the real incremental terminal renderer without a timed test delay.
  async function paint() {
    while (frames.length) frames.shift()?.(0)
    await nextTick()
    await flushPromises()
  }

  // mountViewer provides a retained transcript without any daemon transport or Pinia dependency.
  function mountViewer() {
    return mount(LogViewer, { props: { output: 'ready\nready\n', resetKey: 0, identity: 'project:session', label: 'Development output' } })
  }

  it('scopes Cmd/Ctrl+F to the viewer, wraps navigation, and closes with Escape', async () => {
    const wrapper = mountViewer()
    await paint()
    await wrapper.get('[data-log-viewport]').trigger('keydown', { key: 'f', ctrlKey: true })
    const input = wrapper.get('input')
    await input.setValue('READY')
    expect(wrapper.text()).toContain('1 of 2')
    expect(wrapper.findAll('mark')).toHaveLength(2)
    await input.trigger('keydown', { key: 'Enter' })
    expect(wrapper.text()).toContain('2 of 2')
    await input.trigger('keydown', { key: 'Enter' })
    expect(wrapper.text()).toContain('1 of 2')
    await input.trigger('keydown', { key: 'Enter', shiftKey: true })
    expect(wrapper.text()).toContain('2 of 2')
    await input.setValue('absent')
    expect(wrapper.text()).toContain('No matches')
    expect(wrapper.get('button[aria-label="Next match"]').attributes('disabled')).toBeDefined()
    await input.trigger('keydown', { key: 'Escape' })
    expect(wrapper.find('input').exists()).toBe(false)
    expect(wrapper.findAll('mark')).toHaveLength(0)
    expect(wrapper.text()).toContain('Resume following')
    wrapper.unmount()
  })

  it('keeps the viewport paused while output arrives and resumes at the newest output explicitly', async () => {
    const wrapper = mountViewer()
    const viewport = wrapper.get('[data-log-viewport]').element as HTMLElement
    Object.defineProperties(viewport, { scrollHeight: { configurable: true, value: 1000 }, clientHeight: { configurable: true, value: 100 } })
    await paint()
    expect(viewport.scrollTop).toBe(1000)
    await wrapper.get('button[aria-pressed]').trigger('click')
    viewport.scrollTop = 120
    await wrapper.setProps({ output: 'ready\nready\nmore output\n' })
    await paint()
    expect(viewport.scrollTop).toBe(120)
    expect(wrapper.text()).toContain('more output')
    await wrapper.findAll('button').find(button => button.text() === 'Jump to latest')!.trigger('click')
    await flushPromises()
    expect(viewport.scrollTop).toBe(1000)
    expect(wrapper.text()).toContain('Pause scrolling')
    viewport.scrollTop = 120
    await wrapper.get('[data-log-viewport]').trigger('scroll')
    expect(wrapper.text()).toContain('Resume following')
    wrapper.unmount()
  })

  it('preserves selection across append, recalculates on retention reset, and clears search on session replacement', async () => {
    const wrapper = mountViewer()
    await paint()
    await wrapper.get('button[aria-label="Search logs"]').trigger('click')
    await wrapper.get('input').setValue('ready')
    await wrapper.get('button[aria-label="Next match"]').trigger('click')
    await wrapper.setProps({ output: 'ready\nready\nready\n' })
    await paint()
    expect(wrapper.text()).toContain('2 of 3')
    await wrapper.setProps({ output: 'ready\n', resetKey: 1 })
    await paint()
    expect(wrapper.text()).toContain('1 of 1')
    await wrapper.setProps({ output: 'different session', resetKey: 2, identity: 'project:next-session' })
    await paint()
    expect(wrapper.find('input').exists()).toBe(false)
    expect(wrapper.findAll('mark')).toHaveLength(0)
    expect(wrapper.text()).toContain('Pause scrolling')
    expect(wrapper.text()).not.toContain('ready')
    wrapper.unmount()
  })

  it('copies full visible plain text rather than only the highlighted match and displays clipboard errors', async () => {
    const wrapper = mountViewer()
    await wrapper.setProps({ output: '\u001b[32mready\u001b[0m\n' })
    await paint()
    await wrapper.get('button[aria-label="Search logs"]').trigger('click')
    await wrapper.get('input').setValue('ready')
    vi.mocked(copyText).mockRejectedValueOnce(new Error('Clipboard unavailable'))
    const copy = wrapper.findAll('button').find(button => button.text() === 'Copy')!
    await copy.trigger('click')
    await flushPromises()
    expect(wrapper.text()).toContain('Clipboard unavailable')
    vi.mocked(copyText).mockResolvedValueOnce(undefined)
    await copy.trigger('click')
    await flushPromises()
    expect(copyText).toHaveBeenLastCalledWith('ready\n')
    expect(wrapper.text()).toContain('Copied')
    expect(wrapper.text()).not.toContain('Clipboard unavailable')
    wrapper.unmount()
  })
})
