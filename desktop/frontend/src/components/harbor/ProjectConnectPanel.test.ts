import { flushPromises, mount } from '@vue/test-utils'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { harborBridge } from '@/bridge'
import type { ProjectSnapshot, ServiceLogs } from '@/domain/harbor'
import ProjectConnectPanel from './ProjectConnectPanel.vue'

function project(): ProjectSnapshot {
  return {
    id: 'project-test', name: 'Test', path: '/workspace/test', slug: 'test',
    state: 'ready', favorite: false, updated_at: '2026-10-21T00:00:00Z', apps: [],
    services: [{ id: 'mysql', name: 'MySQL', kind: 'compose', state: 'ready', owner: 'compose', selection: 'selected', required: false }],
    resources: [],
  }
}

function logs(): ServiceLogs {
  return {
    project_id: 'project-test', service_id: 'mysql', supported: true, available: true,
    output: { available: true, text: '', next_cursor: 0, reset: false, truncated: false, has_more: false },
    ports: [{ address: '127.77.0.10', private: 3306, public: 3306, protocol: 'tcp', replica: 1 }],
  }
}

afterEach(() => vi.restoreAllMocks())

describe('ProjectConnectPanel', () => {
  it('shows only the current published native connection', async () => {
    vi.spyOn(harborBridge, 'getServiceLogs').mockResolvedValue(logs())
    const wrapper = mount(ProjectConnectPanel, { props: { active: true, fresh: true, project: project() } })
    await flushPromises()
    expect(wrapper.find('[aria-label="Copy mysql.test.test:3306 address"]').exists()).toBe(true)
    wrapper.unmount()
  })

  it.each(['stopped', 'failed', 'starting', 'unavailable'] as const)('does not advertise old addresses for a %s project', async (state) => {
    const read = vi.spyOn(harborBridge, 'getServiceLogs')
    const wrapper = mount(ProjectConnectPanel, { props: { active: true, fresh: true, project: { ...project(), state } } })
    await flushPromises()
    expect(read).not.toHaveBeenCalled()
    expect(wrapper.text()).toContain('Start the project')
    expect(wrapper.find('[aria-label$="address"]').exists()).toBe(false)
    wrapper.unmount()
  })

  it('clears published addresses on disconnect and rejects the late prior observation', async () => {
    let resolve!: (value: ServiceLogs) => void
    vi.spyOn(harborBridge, 'getServiceLogs').mockImplementation(() => new Promise((done) => { resolve = done }))
    const wrapper = mount(ProjectConnectPanel, { props: { active: true, fresh: true, project: project() } })
    await wrapper.setProps({ fresh: false })
    resolve(logs())
    await flushPromises()
    expect(wrapper.text()).toContain('Waiting for a fresh Harbor connection')
    expect(wrapper.find('[aria-label$="address"]').exists()).toBe(false)
    expect(wrapper.get('button').attributes('disabled')).toBeDefined()
    wrapper.unmount()
  })

  it('discards a request when the tab closes and refreshes when it reopens', async () => {
    let resolve!: (value: ServiceLogs) => void
    const read = vi.spyOn(harborBridge, 'getServiceLogs').mockImplementationOnce(() => new Promise((done) => { resolve = done })).mockResolvedValue(logs())
    const wrapper = mount(ProjectConnectPanel, { props: { active: true, fresh: true, project: project() } })
    await wrapper.setProps({ active: false })
    resolve(logs())
    await flushPromises()
    expect(wrapper.find('[aria-label$="address"]').exists()).toBe(false)
    await wrapper.setProps({ active: true })
    await flushPromises()
    expect(read).toHaveBeenCalledTimes(2)
    expect(wrapper.find('[aria-label="Copy mysql.test.test:3306 address"]').exists()).toBe(true)
    wrapper.unmount()
  })

  it.each([
    { supported: false }, { available: false },
    { project_id: 'another-project' }, { service_id: 'another-service' },
    { problem: { code: 'runtime_unavailable', message: 'Engine unavailable', retryable: true } },
  ])('does not turn an unavailable or mismatched observation into a connection: %o', async (change) => {
    vi.spyOn(harborBridge, 'getServiceLogs').mockResolvedValue({ ...logs(), ...change })
    const wrapper = mount(ProjectConnectPanel, { props: { active: true, fresh: true, project: project() } })
    await flushPromises()
    expect(wrapper.text()).toContain('Connection details are unavailable')
    expect(wrapper.find('[aria-label$="address"]').exists()).toBe(false)
    wrapper.unmount()
  })

  it('reports a failed inspection and preserves Refresh for a retry', async () => {
    vi.spyOn(harborBridge, 'getServiceLogs').mockRejectedValue(new Error('disconnected'))
    const wrapper = mount(ProjectConnectPanel, { props: { active: true, fresh: true, project: project() } })
    await flushPromises()
    expect(wrapper.text()).toContain('Connection details are unavailable')
    expect(wrapper.get('button').attributes('disabled')).toBeUndefined()
    wrapper.unmount()
  })
})
