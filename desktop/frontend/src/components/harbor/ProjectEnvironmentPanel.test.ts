import { flushPromises, mount } from '@vue/test-utils'
import { afterEach, describe, expect, it, vi } from 'vitest'
import ProjectEnvironmentPanel from './ProjectEnvironmentPanel.vue'
import { harborBridge } from '@/bridge'

afterEach(() => {
  vi.restoreAllMocks()
})

describe('ProjectEnvironmentPanel', () => {
  it('presents dotenv files and Harbor overrides as peer tabs', async () => {
    const wrapper = mount(ProjectEnvironmentPanel, {
      props: {
        active: true,
        projectId: 'orders-api',
        supported: true,
      },
    })
    await flushPromises()

    const tabs = wrapper.findAll('[role="tab"]')
    expect(tabs.map((tab) => tab.text())).toEqual(['.env', 'Environment overrides'])
    expect(wrapper.find('textarea[aria-label=".env contents"]').exists()).toBe(true)
    expect(wrapper.text()).not.toContain('IP_ADDRESS')

    await tabs[1]!.trigger('click')

    expect(wrapper.find('textarea').exists()).toBe(false)
    expect(wrapper.text()).toContain('IP_ADDRESS')
    expect(wrapper.text()).toContain('127.77.0.10')
  })

  it('saves the selected file against the revision that was displayed', async () => {
    const save = vi.spyOn(harborBridge, 'saveProjectEnvironmentFile').mockResolvedValue({
      name: '.env',
      contents: 'APP_NAME=Edited\n',
      revision: 'b'.repeat(64),
    })
    const wrapper = mount(ProjectEnvironmentPanel, {
      props: {
        active: true,
        projectId: 'orders-api',
        supported: true,
      },
    })
    await flushPromises()

    await wrapper.get('textarea[aria-label=".env contents"]').setValue('APP_NAME=Edited\n')
    const saveButton = wrapper.findAll('button').find((button) => button.text() === 'Save')
    expect(saveButton).toBeDefined()
    await saveButton!.trigger('click')
    await flushPromises()

    expect(save).toHaveBeenCalledWith(
      'orders-api',
      '.env',
      'APP_NAME=Edited\n',
      'a'.repeat(64),
    )
    expect(wrapper.text()).not.toContain('Unsaved changes')
  })
})
