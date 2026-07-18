import { mount } from '@vue/test-utils'
import { nextTick } from 'vue'
import { describe, expect, it, vi } from 'vitest'
import type { LogLine } from '@/domain/harbor'
import LogStream from './LogStream.vue'

vi.mock('@tanstack/vue-virtual', async () => {
  const { computed, unref } = await import('vue')

  return {
    useVirtualizer(options: unknown) {
      return computed(() => {
        const resolved = unref(options) as {
          count: number
          getItemKey: (index: number) => string | number
        }

        return {
          getTotalSize: () => resolved.count * 27,
          getVirtualItems: () => Array.from({ length: resolved.count }, (_, index) => ({
            index,
            key: resolved.getItemKey(index),
            start: index * 27,
          })),
          scrollToIndex: () => undefined,
        }
      })
    },
  }
})

describe('LogStream', () => {
  it('renders one virtual row without collapsing significant message whitespace', async () => {
    const line: LogLine = {
      id: 7,
      timestamp: 'not-a-date',
      source: 'app',
      stream: 'stdout',
      message: '  indented\tvalue  ',
    }
    const wrapper = mount(LogStream, {
      props: {
        lines: [line],
        ariaLabel: 'Single process output',
      },
    })

    await nextTick()

    expect(wrapper.get('[role="log"]').attributes('aria-label')).toBe('Single process output')
    expect(wrapper.findAll('li')).toHaveLength(1)
    expect(wrapper.get('ol').attributes('style')).toContain('height: 27px')
    expect(wrapper.get('time').text()).toBe('not-a-date')

    const message = wrapper.get('.whitespace-pre')
    expect(message.element.textContent).toBe('  indented\tvalue  ')
    expect(message.classes()).toContain('whitespace-pre')
  })
})
