import { mount } from '@vue/test-utils'
import { afterEach, describe, expect, it } from 'vitest'
import { nextTick } from 'vue'
import { applyTheme } from '@/lib/theme'
import Toaster from './Sonner.vue'

describe('Harbor toaster', () => {
  afterEach(() => {
    applyTheme('light')
  })

  it('uses the applied Harbor theme for rich toast colors', async () => {
    applyTheme('dark')
    const wrapper = mount(Toaster, {
      props: { richColors: true },
    })
    const toaster = wrapper.get('[data-sonner-toaster]')

    expect(toaster.attributes('data-sonner-theme')).toBe('dark')
    expect(toaster.attributes('data-rich-colors')).toBe('true')

    applyTheme('light')
    await nextTick()

    expect(toaster.attributes('data-sonner-theme')).toBe('light')
  })

  it('respects an explicit Sonner theme override', () => {
    applyTheme('dark')
    const wrapper = mount(Toaster, {
      props: { theme: 'light' },
    })

    expect(wrapper.get('[data-sonner-toaster]').attributes('data-sonner-theme')).toBe('light')
  })
})
