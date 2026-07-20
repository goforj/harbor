import { mount } from '@vue/test-utils'
import { describe, expect, it } from 'vitest'
import HarborIllustration from './HarborIllustration.vue'

describe('HarborIllustration', () => {
  it('keeps artwork presentation-only and exposes the illustration design controls', async () => {
    const wrapper = mount(HarborIllustration, {
      props: { image: '/assets/harbor-background.png' },
    })
    const illustration = wrapper.get('.harbor-illustration')

    expect(illustration.attributes('aria-hidden')).toBe('true')
    expect(illustration.attributes('data-placement')).toBe('bottom-right')
    expect(illustration.attributes('data-size')).toBe('standard')
    expect(illustration.attributes('data-fade')).toBe('balanced')
    expect((illustration.element as HTMLElement).style.backgroundImage).toContain('harbor-background.png')
    expect(wrapper.text()).toBe('')

    await wrapper.setProps({
      image: '/assets/alternate-background.png',
      placement: 'top-left',
      size: 'compact',
      fade: 'strong',
      opacity: 0.08,
    })

    expect(illustration.attributes('data-placement')).toBe('top-left')
    expect(illustration.attributes('data-size')).toBe('compact')
    expect(illustration.attributes('data-fade')).toBe('strong')
    expect((illustration.element as HTMLElement).style.backgroundImage).toContain('alternate-background.png')
    expect((illustration.element as HTMLElement).style.getPropertyValue('--harbor-illustration-opacity')).toBe('0.08')
  })

  it('keeps configured opacity within the background-artwork range', async () => {
    const wrapper = mount(HarborIllustration, {
      props: { image: '/assets/harbor-background.png', opacity: 1 },
    })
    const illustration = wrapper.get('.harbor-illustration').element as HTMLElement

    expect(illustration.style.getPropertyValue('--harbor-illustration-opacity')).toBe('0.1')

    await wrapper.setProps({ opacity: 0 })

    expect(illustration.style.getPropertyValue('--harbor-illustration-opacity')).toBe('0.04')
  })
})
