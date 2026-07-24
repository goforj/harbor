import { flushPromises, mount } from '@vue/test-utils'
import { afterEach, describe, expect, it, vi } from 'vitest'
import ProjectEnvironmentPanel from './ProjectEnvironmentPanel.vue'
import { harborBridge } from '@/bridge'

vi.mock('./DotenvEditor.vue', () => ({
  default: {
    props: ['label', 'modelValue'],
    emits: ['update:modelValue'],
    template: '<textarea :aria-label="label" :value="modelValue" @input="$emit(\'update:modelValue\', $event.target.value)" />',
  },
}))

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
    expect(wrapper.text()).toContain('runtime.provider')
    expect(wrapper.text()).toContain('Project mappings')
    expect(wrapper.text()).toContain('MEILISEARCH_HOST')
    expect(wrapper.text()).toContain('Project address')
    expect(wrapper.text()).toContain('Effective read-only values')
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

  it('saves repository mappings as the canonical revision-fenced Harbor contract', async () => {
    const save = vi.spyOn(harborBridge, 'saveProjectEnvironmentFile')
    const wrapper = mount(ProjectEnvironmentPanel, {
      props: {
        active: true,
        projectId: 'orders-api',
        supported: true,
      },
    })
    await flushPromises()

    await wrapper.findAll('[role="tab"]')[1]!.trigger('click')
    await wrapper.get('#binding-name-0').setValue('SEARCH_HOST')
    const saveButton = wrapper.findAll('button').find((button) => button.text() === 'Save mappings')
    expect(saveButton).toBeDefined()
    await saveButton!.trigger('click')
    await flushPromises()

    expect(save).toHaveBeenCalledWith(
      'orders-api',
      '.harbor.yml',
      'version: 1\n\nenvironment:\n  SEARCH_HOST:\n    from: project.address\n',
      'c'.repeat(64),
    )
    expect(wrapper.text()).not.toContain('Unsaved changes')
    expect(wrapper.text()).toContain('SEARCH_HOST')
  })

  it('blocks duplicate project mappings before they reach Harbor', async () => {
    const save = vi.spyOn(harborBridge, 'saveProjectEnvironmentFile')
    const wrapper = mount(ProjectEnvironmentPanel, {
      props: {
        active: true,
        projectId: 'orders-api',
        supported: true,
      },
    })
    await flushPromises()

    await wrapper.findAll('[role="tab"]')[1]!.trigger('click')
    await wrapper.findAll('button').find((button) => button.text() === 'Add mapping')!.trigger('click')
    const inputs = wrapper.findAll('input[id^="binding-name-"]')
    await inputs[1]!.setValue((inputs[0]!.element as HTMLInputElement).value)

    expect(wrapper.text()).toContain('is mapped more than once')
    expect(wrapper.findAll('button').find((button) => button.text() === 'Save mappings')!.attributes('disabled')).toBeDefined()
    expect(save).not.toHaveBeenCalled()
  })
})
