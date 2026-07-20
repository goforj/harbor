import { mount } from '@vue/test-utils'
import { nextTick } from 'vue'
import { describe, expect, it, vi } from 'vitest'
import { harborBridge } from '@/bridge'
import TerminalOutput from './TerminalOutput.vue'

describe('TerminalOutput', () => {
  it('batches appended output into animation frames and renders process text without HTML execution', async () => {
    const frames: FrameRequestCallback[] = []
    vi.spyOn(window, 'requestAnimationFrame').mockImplementation((callback) => {
      frames.push(callback)
      return frames.length
    })
    vi.spyOn(window, 'cancelAnimationFrame').mockImplementation(() => {})
    const wrapper = mount(TerminalOutput, { props: { output: '', resetKey: 0 } })

    await wrapper.setProps({ output: '\u001b[31m<script>' })
    await wrapper.setProps({ output: '\u001b[31m<script>alert(1)</script>\u001b[0m' })

    expect(frames).toHaveLength(1)
    frames.shift()?.(0)
    await nextTick()

    expect(wrapper.text()).toBe('<script>alert(1)</script>')
    expect(wrapper.find('script').exists()).toBe(false)
    expect(wrapper.get('span').attributes('style')).toContain('color: rgb(205, 49, 49)')
  })

  it('resets the model when retained output is replaced', async () => {
    const frames: FrameRequestCallback[] = []
    vi.spyOn(window, 'requestAnimationFrame').mockImplementation((callback) => {
      frames.push(callback)
      return frames.length
    })
    vi.spyOn(window, 'cancelAnimationFrame').mockImplementation(() => {})
    const wrapper = mount(TerminalOutput, { props: { output: 'old output', resetKey: 0 } })
    frames.shift()?.(0)
    await nextTick()
    expect(wrapper.text()).toBe('old output')

    await wrapper.setProps({ output: 'old output from a new session', resetKey: 1 })
    frames.shift()?.(0)
    await nextTick()

    expect(wrapper.text()).toBe('old output from a new session')
  })

  it('renders safe HTTP links without interpreting terminal text as HTML', async () => {
    const frames: FrameRequestCallback[] = []
    vi.spyOn(window, 'requestAnimationFrame').mockImplementation((callback) => {
      frames.push(callback)
      return frames.length
    })
    vi.spyOn(window, 'cancelAnimationFrame').mockImplementation(() => {})
    const wrapper = mount(TerminalOutput, { props: { output: 'Open https://orders.test/docs.', resetKey: 0 } })
    frames.shift()?.(0)
    await nextTick()

    const link = wrapper.get('a')
    expect(link.attributes('href')).toBe('https://orders.test/docs')
    expect(link.attributes('rel')).toBe('noopener noreferrer')
    expect(link.text()).toBe('https://orders.test/docs')

    const openTerminalURL = vi.spyOn(harborBridge, 'openTerminalURL').mockResolvedValueOnce()
    await link.trigger('click')
    expect(openTerminalURL).toHaveBeenCalledWith('https://orders.test/docs')
  })
})
