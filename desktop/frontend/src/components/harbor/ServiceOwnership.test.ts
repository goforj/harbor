import { mount } from '@vue/test-utils'
import { describe, expect, it } from 'vitest'
import ServiceOwnership from './ServiceOwnership.vue'

describe('ServiceOwnership', () => {
  it('presents Compose ownership as a project service', () => {
    const wrapper = mount(ServiceOwnership, { props: { owner: 'compose' } })

    expect(wrapper.text()).toBe('Compose service')
    expect(wrapper.find('.lucide-container').exists()).toBe(true)
  })

  it('distinguishes external services without implying container ownership', async () => {
    const wrapper = mount(ServiceOwnership, { props: { owner: 'external', iconOnly: true } })

    expect(wrapper.text()).toBe('')
    expect(wrapper.attributes('aria-label')).toBe('External service')
    expect(wrapper.find('.lucide-cloud').exists()).toBe(true)
  })
})
