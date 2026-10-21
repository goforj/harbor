import { flushPromises, mount } from '@vue/test-utils'
import { createPinia, setActivePinia } from 'pinia'
import { createMemoryHistory, createRouter } from 'vue-router'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { mockSnapshot, mockStatus } from '@/bridge/mock'
import { projectGuideState } from '@/lib/projectGuide'
import { newProjectDialogOpen } from '@/lib/projectCreationDialog'
import { useHarborStore } from '@/stores/harbor'
import FirstProjectGuide from './FirstProjectGuide.vue'

// mountGuide keeps the guide on real snapshot/store semantics while replacing only external effects in each test.
async function mountGuide(empty = false) {
  const pinia = createPinia()
  setActivePinia(pinia)
  const store = useHarborStore()
  const snapshot = mockSnapshot()
  snapshot.network_setup = { configured: true }
  if (empty) { snapshot.projects = []; snapshot.operations = []; snapshot.recent_resource_ids = [] }
  store.$patch({ snapshot, daemonStatus: mockStatus(), snapshotStale: false, connectionState: 'connected' })
  const router = createRouter({ history: createMemoryHistory(), routes: [{ path: '/', component: { template: '<div />' } }, { path: '/projects/:projectId', component: { template: '<div />' } }] })
  await router.push('/')
  const wrapper = mount(FirstProjectGuide, { global: { plugins: [pinia, router] } })
  await flushPromises()
  return { wrapper, store, router }
}

describe('FirstProjectGuide', () => {
  beforeEach(() => {
    projectGuideState.value = { offered: true, visible: true, projectId: 'reports', appOpened: false }
    newProjectDialogOpen.value = false
  })

  it('opens the shared creator and emits registration without inventing project or URL state', async () => {
    const { wrapper } = await mountGuide(true)
    await wrapper.findAll('button').find(button => button.text() === 'New project')!.trigger('click')
    expect(newProjectDialogOpen.value).toBe(true)
    await wrapper.findAll('button').find(button => button.text() === 'Add existing')!.trigger('click')
    expect(wrapper.emitted('add')).toHaveLength(1)
    expect(wrapper.findAll('button').find(button => button.text() === 'Start project')!.attributes('disabled')).toBeDefined()
    expect(wrapper.findAll('button').some(button => button.text() === 'Open app')).toBe(false)
    wrapper.unmount()
  })

  it('uses guarded Start and cannot act on a retained stale snapshot', async () => {
    const { wrapper, store } = await mountGuide()
    const start = vi.spyOn(store, 'startProject').mockResolvedValue(null)
    const button = wrapper.findAll('button').find(button => button.text() === 'Start project')!
    await button.trigger('click')
    expect(start).toHaveBeenCalledWith('reports')
    store.snapshotStale = true
    await flushPromises()
    expect(button.attributes('disabled')).toBeDefined()
    expect(wrapper.text()).toContain('Reconnect to Harbor')
    wrapper.unmount()
  })

  it('offers pending network continuation and respects existing removal guards', async () => {
    const { wrapper, store } = await mountGuide()
    store.snapshot!.network_setup = { configured: false, pending: { operation_id: 'pending', intent_id: 'intent', operation_revision: 1, kind: 'setup', phase: 'approval' } }
    store.daemonStatus!.capabilities.push('control.network-setup.v1', 'control.network-resolver-policy-migration.v1')
    store.networkSetupError = 'Old networking needs attention.'
    await flushPromises()
    await wrapper.findAll('button').find(button => button.text() === 'Resume setup')!.trigger('click')
    expect(wrapper.emitted('setup')).toHaveLength(1)
    expect(wrapper.text()).toContain('Old networking needs attention.')
    expect(wrapper.findAll('button').find(button => button.text() === 'Start project')!.attributes('disabled')).toBeDefined()
    wrapper.unmount()
  })

  it('opens only an actually published App resource and keeps failures visible', async () => {
    const { wrapper, store } = await mountGuide()
    const project = store.projectById('reports')!
    project.state = 'ready'
    project.resources = [{ id: 'application', name: 'Application', kind: 'application', owner: { kind: 'app', app_id: project.apps[0]!.id }, url: 'https://reports.test' }]
    const open = vi.spyOn(store, 'openResource').mockImplementation(async () => { store.actionError = 'Browser unavailable' })
    await flushPromises()
    const button = wrapper.findAll('button').find(button => button.text() === 'Open app')!
    await button.trigger('click')
    expect(open).toHaveBeenCalledWith('reports', 'application')
    expect(projectGuideState.value.appOpened).toBe(false)
    expect(wrapper.text()).toContain('Browser unavailable')
    open.mockImplementation(async () => { store.actionError = null })
    await button.trigger('click')
    expect(projectGuideState.value.appOpened).toBe(true)
    await wrapper.findAll('button').find(button => button.text() === 'Finish setup')!.trigger('click')
    expect(projectGuideState.value.visible).toBe(false)
    wrapper.unmount()
  })

  it('explains unavailable networking capabilities instead of leaving a disabled setup action', async () => {
    const { wrapper, store } = await mountGuide()
    store.snapshot!.network_setup = { configured: false }
    store.daemonStatus!.capabilities = []
    await flushPromises()
    expect(wrapper.text()).toContain('This background service cannot set up networking')
    expect(wrapper.findAll('button').some(button => button.text() === 'Daemon settings')).toBe(true)
    wrapper.unmount()
  })
})
