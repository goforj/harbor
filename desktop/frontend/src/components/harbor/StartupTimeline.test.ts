import { mount } from '@vue/test-utils'
import { describe, expect, it } from 'vitest'
import type { StartupEvent } from '@/domain/harbor'
import StartupTimeline from './StartupTimeline.vue'

function event(sequence: number, label: string, status: StartupEvent['status'], phase: StartupEvent['phase'] = 'task'): StartupEvent {
  return { schema_version: 1, sequence, session: 'session', phase, label, status, elapsed_ms: 1200 }
}

describe('StartupTimeline', () => {
  it('joins repeated task names to the correct open span and keeps their timings', () => {
    const wrapper = mount(StartupTimeline, { props: { progress: { events: [
      event(1, 'Setup', 'started'), event(2, 'Setup', 'completed'),
      event(3, 'Setup', 'started'), event(4, 'Setup', 'failed'),
      event(5, 'Development startup', 'failed', 'startup'),
    ] } } })
    expect(wrapper.findAll('li')).toHaveLength(2)
    expect(wrapper.findAll('li')[0]!.text()).toContain('completed')
    expect(wrapper.findAll('li')[1]!.text()).toContain('failed')
    expect(wrapper.text()).toContain('1.2 s')
    expect(wrapper.get('details').attributes('open')).toBeDefined()
    expect(wrapper.text()).toContain('Startup failed')
  })

  it('reports producer completion without claiming Harbor readiness', () => {
    const wrapper = mount(StartupTimeline, { props: { progress: { events: [event(1, 'Startup', 'completed', 'startup')] } } })
    expect(wrapper.text()).toContain('Startup steps completed')
    expect(wrapper.text()).not.toContain('Ready')
    expect(wrapper.get('details').attributes('open')).toBeUndefined()
  })

  it('preserves raw-output guidance for malformed optional diagnostics', () => {
    const wrapper = mount(StartupTimeline, { props: { progress: { events: [], invalid: true } } })
    expect(wrapper.text()).toContain('Development output is still available')
  })
})
