import { flushPromises, mount } from '@vue/test-utils'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { harborBridge } from '@/bridge'
import { mockSnapshot } from '@/bridge/mock'
import ProjectIcon from './ProjectIcon.vue'

afterEach(() => vi.restoreAllMocks())

describe('ProjectIcon', () => {
  it.each(['stopped', 'starting', 'stopping', 'failed', 'unavailable'] as const)('uses local GoForj artwork without a favicon lookup while %s', async (state) => {
    const project = structuredClone(mockSnapshot().projects[0]!)
    project.state = state
    const lookup = vi.spyOn(harborBridge, 'getResourceIconURL')
    const wrapper = mount(ProjectIcon, { props: { project } })
    await flushPromises()
    expect(wrapper.get('img').attributes('src')).toContain('goforj-ios.png')
    expect(lookup).not.toHaveBeenCalled()
  })

  it('uses GoForj until an online project reports its favicon, then restores GoForj when stopped', async () => {
    const project = structuredClone(mockSnapshot().projects[0]!)
    project.state = 'stopped'
    const resource = project.resources.find(resource => resource.kind === 'application')!
    const favicon = new URL('/app-icon.svg', resource.url).href
    const lookup = vi.spyOn(harborBridge, 'getResourceIconURL').mockResolvedValue(favicon)
    const wrapper = mount(ProjectIcon, { props: { project } })
    expect(wrapper.get('img').attributes('src')).toContain('goforj-ios.png')
    await wrapper.setProps({ project: { ...project, state: 'ready' } })
    await flushPromises()
    expect(lookup).toHaveBeenCalledWith(project.id, resource.id)
    expect(wrapper.get('img').attributes('src')).toBe(favicon)
    await wrapper.setProps({ project: { ...project, state: 'stopped' } })
    expect(wrapper.get('img').attributes('src')).toContain('goforj-ios.png')
  })

  it('keeps an online project identifiable when favicon discovery fails', async () => {
    const project = structuredClone(mockSnapshot().projects[0]!)
    project.state = 'ready'
    vi.spyOn(harborBridge, 'getResourceIconURL').mockRejectedValue(new Error('unreachable'))
    const wrapper = mount(ProjectIcon, { props: { project } })
    await flushPromises()
    expect(wrapper.get('img').attributes('src')).toContain('goforj-ios.png')
  })
})
