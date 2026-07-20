import { mount } from '@vue/test-utils'
import { createPinia, setActivePinia } from 'pinia'
import { createMemoryHistory, createRouter } from 'vue-router'
import { describe, expect, it } from 'vitest'
import { mockSnapshot } from '@/bridge/mock'
import { useHarborStore } from '@/stores/harbor'
import ServiceView from './ServiceView.vue'

// mountServiceView builds the smallest project-scoped route needed to verify the detail hierarchy.
async function mountServiceView(owner: 'compose' | 'external') {
  const pinia = createPinia()
  setActivePinia(pinia)
  const store = useHarborStore()
  const snapshot = mockSnapshot()
  const service = snapshot.projects
    .find((project) => project.id === 'orders-api')
    ?.services.find((entry) => entry.id === 'mysql')
  if (!service) throw new Error('MySQL fixture service is missing')
  service.owner = owner
  store.$patch({ snapshot, connectionState: 'connected', snapshotStale: false })

  const router = createRouter({
    history: createMemoryHistory(),
    routes: [
      { path: '/services/:projectId/:serviceId', component: ServiceView },
      { path: '/services', component: { template: '<div>Services</div>' } },
      { path: '/projects/:projectId', component: { template: '<div>Project</div>' } },
    ],
  })
  await router.push('/services/orders-api/mysql')
  await router.isReady()
  const wrapper = mount(ServiceView, {
    global: {
      plugins: [pinia, router],
      stubs: {
        ServiceLogsPanel: { template: '<section data-testid="service-logs">Logs</section>' },
      },
    },
  })
  return wrapper
}

describe('ServiceView', () => {
  it('places Compose service logs before supporting service facts', async () => {
    const wrapper = await mountServiceView('compose')
    const logs = wrapper.get('[data-testid="service-logs"]')
    const facts = wrapper.get('[aria-label="Service facts"]')

    expect(logs.element.compareDocumentPosition(facts.element) & Node.DOCUMENT_POSITION_FOLLOWING).not.toBe(0)
    expect(wrapper.text()).toContain('Compose service')
    wrapper.unmount()
  })

  it('does not request project-owned logs for an external service', async () => {
    const wrapper = await mountServiceView('external')

    expect(wrapper.find('[data-testid="service-logs"]').exists()).toBe(false)
    expect(wrapper.text()).toContain('Logs for this external service are managed outside Harbor.')
    wrapper.unmount()
  })
})
