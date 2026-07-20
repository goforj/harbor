import { flushPromises, mount } from '@vue/test-utils'
import { createPinia, setActivePinia } from 'pinia'
import { describe, expect, it, vi } from 'vitest'
import type { ServiceLogs } from '@/domain/harbor'
import { useHarborStore } from '@/stores/harbor'
import ServiceLogsPanel from './ServiceLogsPanel.vue'

const { copyText } = vi.hoisted(() => ({ copyText: vi.fn() }))

vi.mock('@/bridge/clipboard', () => ({ copyText }))

// never models a native held read while the panel remains mounted.
function never<T>(): Promise<T> {
  return new Promise(() => {})
}

// mountPanel provides a connected store without involving the native bridge.
function mountPanel(capabilities: string[], response?: ServiceLogs) {
  const pinia = createPinia()
  setActivePinia(pinia)
  const store = useHarborStore()
  store.$patch({
    connectionState: 'connected',
    daemonStatus: {
      state: 'ready',
      build: { version: 'dev', modified: false },
      protocol: { major: 1, minor: 0 },
      capabilities,
      snapshot_schema_version: 1,
      sequence: 1,
    },
  })
  const read = vi.spyOn(store, 'readServiceLogs')
  if (response) read.mockResolvedValue(response)
  const wait = vi.spyOn(store, 'waitServiceLogs').mockImplementation(() => never<ServiceLogs>())
  const wrapper = mount(ServiceLogsPanel, {
    props: { projectId: 'orders', serviceId: 'mysql', serviceName: 'MySQL' },
    global: {
      plugins: [pinia],
      stubs: { TerminalOutput: { template: '<div data-testid="terminal-output" />' } },
    },
  })
  return { read, store, wait, wrapper }
}

describe('ServiceLogsPanel', () => {
  it('shows a live Compose transcript with local clear and follow controls', async () => {
    const response: ServiceLogs = {
      project_id: 'orders',
      service_id: 'mysql',
      session_id: 'session-orders',
      supported: true,
      available: true,
      output: {
        available: true,
        reset: false,
        truncated: false,
        has_more: false,
        next_cursor: 6,
        text: 'ready\n',
      },
    }
    const { read, wait, wrapper } = mountPanel(
      ['control.service-logs.v1', 'control.service-logs-wait.v1'],
      response,
    )
    await flushPromises()

    expect(read).toHaveBeenCalledWith('orders', '', 'mysql', 0)
    expect(wait).toHaveBeenCalledWith('orders', 'session-orders', 'mysql', 6, 25_000)
    expect(wrapper.text()).toContain('Live')
    expect(wrapper.find('[data-testid="terminal-output"]').exists()).toBe(true)
    expect(wrapper.get('button[aria-pressed="true"]').text()).toContain('Following')

    const clear = wrapper.findAll('button').find((button) => button.text().includes('Clear'))
    if (!clear) throw new Error('Clear service logs action is missing')
    await clear.trigger('click')
    expect(wrapper.text()).toContain('Waiting for new service log output…')
    wrapper.unmount()
  })

  it('copies the complete unrendered service transcript', async () => {
    const response: ServiceLogs = {
      project_id: 'orders', service_id: 'mysql', session_id: 'session-orders', supported: true, available: true,
      output: { available: true, reset: false, truncated: false, has_more: false, next_cursor: 18, text: '\u001b[32mready\u001b[0m\n' },
    }
    copyText.mockResolvedValueOnce(undefined)
    const { wrapper } = mountPanel(['control.service-logs.v1'], response)
    await flushPromises()

    const copy = wrapper.findAll('button').find((button) => button.text().includes('Copy'))
    if (!copy) throw new Error('Copy service logs action is missing')
    await copy.trigger('click')
    await flushPromises()

    expect(copyText).toHaveBeenCalledWith('\u001b[32mready\u001b[0m\n')
    expect(wrapper.text()).toContain('Copied')
    wrapper.unmount()
  })

  it('explains Docker log unavailability without attempting a request', async () => {
    const { read, wrapper } = mountPanel([])
    await flushPromises()

    expect(read).not.toHaveBeenCalled()
    expect(wrapper.text()).toContain('Unsupported')
    expect(wrapper.text()).toContain('This Harbor build does not support service logs.')
    expect(wrapper.text()).not.toContain('GoForj')
    wrapper.unmount()
  })

  it('presents a typed Docker runtime problem as an error rather than a missing Harbor capability', async () => {
    const response: ServiceLogs = {
      project_id: 'orders',
      service_id: 'mysql',
      supported: true,
      available: false,
      problem: {
        code: 'docker_runtime_unavailable',
        message: 'Harbor could not connect to the Docker runtime.',
        retryable: false,
      },
      output: {
        available: false,
        reset: false,
        truncated: false,
        has_more: false,
        next_cursor: 0,
        text: '',
      },
    }
    const { wrapper } = mountPanel(['control.service-logs.v1'], response)
    await flushPromises()

    expect(wrapper.text()).toContain('Error')
    expect(wrapper.text()).toContain('Harbor could not connect to the Docker runtime.')
    expect(wrapper.text()).not.toContain('This Harbor build does not support service logs.')
    expect(wrapper.text()).not.toContain('GoForj')
    wrapper.unmount()
  })
})
