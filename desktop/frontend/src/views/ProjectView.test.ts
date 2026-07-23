import { flushPromises, mount, type VueWrapper } from '@vue/test-utils'
import { createPinia, setActivePinia, type Pinia } from 'pinia'
import { createMemoryHistory, createRouter, type Router } from 'vue-router'
import { describe, expect, it, vi } from 'vitest'
import { harborBridge } from '@/bridge'
import { harborWireFixture } from '@/bridge/harbor.fixture'
import { mockSnapshot } from '@/bridge/mock'
import type { ProjectLifecycleOperation, ProjectRuntimeRepairInspection } from '@/domain/harbor'
import { useHarborStore } from '@/stores/harbor'
import ProjectView from './ProjectView.vue'

interface MountedProjectView {
  pinia: Pinia
  router: Router
  store: ReturnType<typeof useHarborStore>
  wrapper: VueWrapper
}

function deferred<T>() {
  let resolve!: (value: T) => void
  const promise = new Promise<T>((resolvePromise) => {
    resolve = resolvePromise
  })
  return { promise, resolve }
}

async function mountRecoveryProject(): Promise<MountedProjectView> {
  const pinia = createPinia()
  setActivePinia(pinia)
  const store = useHarborStore()
  await store.initialize()
  const snapshot = mockSnapshot()
  const project = snapshot.projects.find((entry) => entry.id === 'billing')
  if (!project) throw new Error('Billing fixture project is missing')
  project.state = 'unavailable'
  store.$patch({
    snapshot,
    projectLifecycleErrors: {
      billing: 'Harbor could not prove that the previous development runtime stopped.',
    },
    projectLifecycleProblemCodes: {
      billing: 'project.recovery.ambiguous_launch',
    },
  })

  const router = createRouter({
    history: createMemoryHistory(),
    routes: [
      { path: '/projects/:projectId', component: ProjectView },
      { path: '/projects', component: { template: '<div>Projects</div>' } },
    ],
  })
  await router.push('/projects/billing')
  await router.isReady()
  const wrapper = mount(ProjectView, {
    attachTo: document.body,
    global: { plugins: [pinia, router] },
  })
  await flushPromises()
  return { pinia, router, store, wrapper }
}

async function mountProject(projectId = 'orders-api'): Promise<MountedProjectView> {
  const pinia = createPinia()
  setActivePinia(pinia)
  const store = useHarborStore()
  await store.initialize()

  const router = createRouter({
    history: createMemoryHistory(),
    routes: [
      { path: '/projects/:projectId', component: ProjectView },
      { path: '/projects', component: { template: '<div>Projects</div>' } },
    ],
  })
  await router.push(`/projects/${projectId}`)
  await router.isReady()
  const wrapper = mount(ProjectView, {
    attachTo: document.body,
    global: { plugins: [pinia, router] },
  })
  await flushPromises()
  return { pinia, router, store, wrapper }
}

async function mountStaleRuntimeProject(projectId = 'billing'): Promise<MountedProjectView> {
  const mounted = await mountProject(projectId)
  const project = mounted.store.projectById(projectId)
  if (!project) throw new Error(`${projectId} fixture project is missing`)
  project.state = 'stopped'
  project.apps = project.apps.map((app) => ({ ...app, state: 'stopped', active: false }))
  project.services = project.services.map((service) => ({ ...service, state: 'stopped' }))
  project.resources = []
  await mounted.wrapper.vm.$nextTick()
  return mounted
}

async function mountFullNetworkBlockedProject(projectId = 'orders-api'): Promise<MountedProjectView> {
  const mounted = await mountProject(projectId)
  mounted.store.$patch({
    projectLifecycleErrors: {
      [projectId]: 'Harbor DNS is active, but secure ingress is not ready.',
    },
    projectLifecycleProblemCodes: {
      [projectId]: 'project.network.full_setup_required',
    },
  })
  await mounted.wrapper.vm.$nextTick()
  return mounted
}

function confirmableInspection(): Extract<ProjectRuntimeRepairInspection, { disposition: 'confirmable' }> {
  return structuredClone(harborWireFixture.project_runtime_repair_inspection)
}

function bodyButton(label: string): HTMLButtonElement {
  const button = [...document.body.querySelectorAll('button')]
    .find((candidate) => candidate.textContent?.trim() === label)
  if (!(button instanceof HTMLButtonElement)) throw new Error(`Button not found: ${label}`)
  return button
}

function detailTab(wrapper: VueWrapper, label: string) {
  const tab = wrapper.findAll('[role="tab"]').find((candidate) => candidate.text().startsWith(label))
  if (!tab) throw new Error(`Detail tab not found: ${label}`)
  return tab
}

function activeDetailTab(wrapper: VueWrapper) {
  const tab = wrapper.findAll('[role="tab"]').find((candidate) => candidate.attributes('data-state') === 'active')
  if (!tab) throw new Error('Active detail tab not found')
  return tab.text()
}

function admittedStart(projectId: string, intentId: string): ProjectLifecycleOperation {
  const result = structuredClone(harborWireFixture.start_project)
  result.operation.project_id = projectId
  result.operation.intent_id = intentId
  return result
}

describe('ProjectView project start output', () => {
  it('switches from Overview to Development output before the selected project start is admitted', async () => {
    const pending = deferred<ProjectLifecycleOperation>()
    vi.spyOn(harborBridge, 'startProject').mockReturnValueOnce(pending.promise)
    const { wrapper } = await mountProject('reports')

    expect(activeDetailTab(wrapper)).toBe('Overview')
    const starting = bodyButton('Start project').click()
    await flushPromises()

    expect(activeDetailTab(wrapper)).toBe('Development output')
    pending.resolve(admittedStart('reports', 'reports-start'))
    await starting
    await flushPromises()

    expect(activeDetailTab(wrapper)).toBe('Development output')
    wrapper.unmount()
  })

  it.each(['Development output', 'Services', 'Resources'])('preserves the %s tab when the selected project starts', async (tabLabel) => {
    vi.spyOn(harborBridge, 'startProject').mockImplementation(async (projectId, intentId) => admittedStart(projectId, intentId))
    const { wrapper } = await mountProject('reports')
    await detailTab(wrapper, tabLabel).trigger('mousedown', { button: 0 })
    await wrapper.vm.$nextTick()
    expect(activeDetailTab(wrapper)).toContain(tabLabel)

    await bodyButton('Start project').click()
    await flushPromises()

    expect(activeDetailTab(wrapper)).toContain(tabLabel)
    wrapper.unmount()
  })

  it('keeps Development output selected when start admission fails', async () => {
    const startProject = vi.spyOn(harborBridge, 'startProject').mockRejectedValueOnce(new Error('Admission denied'))
    const { wrapper } = await mountProject('reports')

    await bodyButton('Start project').click()
    await flushPromises()

    expect(startProject).toHaveBeenCalledOnce()
    expect(activeDetailTab(wrapper)).toBe('Development output')
    wrapper.unmount()
  })

  it('points failed projects without streamed output to Harbor\'s retained launch trace', async () => {
    const { store, wrapper } = await mountProject('reports')
    const project = store.projectById('reports')
    if (!project) throw new Error('Reports fixture project is missing')
    project.state = 'failed'
    store.projectLifecycleProblemCodes.reports = 'project.process.exited'
    await wrapper.vm.$nextTick()

    await detailTab(wrapper, 'Development output').trigger('mousedown', { button: 0 })
    await wrapper.vm.$nextTick()

    expect(wrapper.text()).toContain('Harbor retained the launch trace at _data/harbor/forj-dev.log.')
    wrapper.unmount()
  })

  it('does not claim a launch trace for failures that happened before a runtime started', async () => {
    const { store, wrapper } = await mountProject('reports')
    const project = store.projectById('reports')
    if (!project) throw new Error('Reports fixture project is missing')
    project.state = 'failed'
    store.projectLifecycleProblemCodes.reports = 'project.network.setup_required'
    await wrapper.vm.$nextTick()

    await detailTab(wrapper, 'Development output').trigger('mousedown', { button: 0 })
    await wrapper.vm.$nextTick()

    expect(wrapper.text()).toContain('No development output yet')
    expect(wrapper.text()).not.toContain('Harbor retained the launch trace')
    wrapper.unmount()
  })

  it('does not override a tab selected while start is pending', async () => {
    const pending = deferred<ProjectLifecycleOperation>()
    vi.spyOn(harborBridge, 'startProject').mockReturnValueOnce(pending.promise)
    const { wrapper } = await mountProject('reports')

    const starting = bodyButton('Start project').click()
    await flushPromises()
    expect(activeDetailTab(wrapper)).toBe('Development output')

    await detailTab(wrapper, 'Services').trigger('mousedown', { button: 0 })
    await wrapper.vm.$nextTick()
    pending.resolve(admittedStart('reports', 'reports-start'))
    await starting
    await flushPromises()

    expect(activeDetailTab(wrapper)).toContain('Services')
    wrapper.unmount()
  })

  it('does not change the newly selected project tab when an earlier start completes', async () => {
    const pending = deferred<ProjectLifecycleOperation>()
    vi.spyOn(harborBridge, 'startProject').mockReturnValueOnce(pending.promise)
    const { router, wrapper } = await mountProject('reports')

    const starting = bodyButton('Start project').click()
    await router.push('/projects/billing')
    await flushPromises()

    pending.resolve(admittedStart('reports', 'reports-start'))
    await starting
    await flushPromises()

    expect(router.currentRoute.value.params.projectId).toBe('billing')
    expect(activeDetailTab(wrapper)).toBe('Overview')
    wrapper.unmount()
  })

  it('keeps billing lifecycle controls enabled while reports start is in flight', async () => {
    const pending = deferred<ProjectLifecycleOperation>()
    vi.spyOn(harborBridge, 'startProject').mockReturnValueOnce(pending.promise)
    const { store, wrapper } = await mountProject('billing')

    const startingReports = store.startProject('reports')
    await vi.waitFor(() => expect(store.projectLifecycleBusyFor('reports')).toBe(true))

    expect(bodyButton('Start project').disabled).toBe(false)

    const result = admittedStart('reports', 'reports-start')
    result.operation.state = 'succeeded'
    result.operation.phase = 'succeeded'
    result.operation.started_at = '2026-07-19T18:00:01Z'
    result.operation.finished_at = '2026-07-19T18:00:02Z'
    pending.resolve(result)
    await startingReports
    wrapper.unmount()
  })
})

describe('ProjectView stale runtime recovery', () => {
  it('keeps project detail content in compact, task-focused tabs', async () => {
    const { wrapper } = await mountRecoveryProject()

    const tabLabels = wrapper.findAll('[role="tab"]').map((tab) => tab.text().replace(/\s+\d+$/, ''))
    expect(tabLabels).toEqual(['Overview', 'Development output', 'Services', 'Resources'])
    expect(wrapper.text()).toContain('Apps')
    expect(wrapper.text()).not.toContain('Reported services for this project.')

    wrapper.unmount()
  })

  it('keeps the ordinary start action available for a recovered project', async () => {
    const { store, wrapper } = await mountRecoveryProject()
    const recover = wrapper.findAll('button').find((button) => button.text().includes('Start project'))
    expect(recover).toBeDefined()
    expect(recover?.attributes('disabled')).toBeUndefined()
    expect(wrapper.findAll('button').some((button) => button.text().includes('Inspect stale runtime'))).toBe(false)

    store.$patch({ snapshotStale: true })
    await wrapper.vm.$nextTick()
    expect(recover?.attributes('disabled')).toBeDefined()

    store.$patch({ snapshotStale: false, connectionState: 'disconnected' })
    await wrapper.vm.$nextTick()
    expect(recover?.attributes('disabled')).toBeDefined()

    const snapshot = mockSnapshot()
    snapshot.operations.push({
      ...structuredClone(harborWireFixture.start_project.operation),
      project_id: 'billing',
      intent_id: 'billing-start',
      state: 'running',
      phase: 'running',
    })
    store.$patch({ connectionState: 'connected', snapshot })
    await wrapper.vm.$nextTick()
    expect(recover?.attributes('disabled')).toBeDefined()

    wrapper.unmount()
  })

  it('does not require stale-runtime inspection before retrying start', async () => {
    const inspect = vi.spyOn(harborBridge, 'inspectProjectRuntimeRepair')
    const start = vi.spyOn(harborBridge, 'startProject').mockImplementationOnce(async (projectId, intentId) => {
      const result = structuredClone(harborWireFixture.start_project)
      result.operation.project_id = projectId
      result.operation.intent_id = intentId
      return result
    })
    const { wrapper } = await mountRecoveryProject()

    const recover = wrapper.findAll('button').find((button) => button.text().includes('Start project'))
    if (!recover) throw new Error('Start project action is missing')
    await recover.trigger('click')
    await flushPromises()

    expect(start).toHaveBeenCalledWith('billing', expect.any(String))
    expect(inspect).not.toHaveBeenCalled()
    wrapper.unmount()
  })

  it('offers a read-only stale-runtime check for an otherwise stopped route-free project', async () => {
    const { wrapper } = await mountStaleRuntimeProject()

    const inspect = wrapper.findAll('button').find((button) => button.text().includes('Check for stale runtime'))
    expect(inspect).toBeDefined()
    expect(inspect?.attributes('disabled')).toBeUndefined()

    wrapper.unmount()
  })

  it('opens a destructive review dialog with only the bounded candidate facts and never auto-confirms', async () => {
    const inspection = confirmableInspection()
    const inspectRuntime = vi.spyOn(harborBridge, 'inspectProjectRuntimeRepair').mockResolvedValueOnce(inspection)
    const confirmRuntime = vi.spyOn(harborBridge, 'confirmProjectRuntimeRepair')
    const { store, wrapper } = await mountStaleRuntimeProject('billing')

    const inspect = wrapper.findAll('button').find((button) => button.text().includes('Check for stale runtime'))
    if (!inspect) throw new Error('Inspect stale runtime action is missing')
    await inspect.trigger('click')
    await flushPromises()

    expect(inspectRuntime).toHaveBeenCalledWith('billing')
    expect(confirmRuntime).not.toHaveBeenCalled()
    const dialog = document.body.querySelector('[role="alertdialog"]')
    expect(dialog).not.toBeNull()
    const text = dialog?.textContent ?? ''
    expect(text).toContain('Harbor no longer has its launch receipt. This process is a candidate, not proven Harbor-owned. Continue only if you recognize it as this project.')
    expect(text).toContain('Commandforj dev')
    expect(text).toContain(`Checkout${inspection.confirmable.candidate.checkout}`)
    expect(text).toContain(`Endpoint${inspection.confirmable.candidate.endpoint}`)
    expect(text).toContain(`Root PID${inspection.confirmable.candidate.root_pid}`)
    expect(text).toContain(`Member count${inspection.confirmable.candidate.member_count}`)
    expect(text).not.toContain(inspection.confirmable.inspection_id)
    expect(text).not.toContain(inspection.confirmable.candidate_fingerprint)
    expect(text).not.toContain(inspection.confirmable.expires_at)
    expect(bodyButton('Stop this process and reset project').disabled).toBe(false)

    await bodyButton('Cancel').click()
    await flushPromises()
    expect(store.projectRuntimeRepairInspection).toBeNull()
    expect(confirmRuntime).not.toHaveBeenCalled()
    wrapper.unmount()
  })

  it('submits opaque selectors only after confirmation and refreshes while consuming the plan', async () => {
    const inspection = confirmableInspection()
    vi.spyOn(harborBridge, 'inspectProjectRuntimeRepair').mockResolvedValueOnce(inspection)
    const confirmation = structuredClone(harborWireFixture.project_runtime_repair_confirmation)
    const confirmRuntime = vi.spyOn(harborBridge, 'confirmProjectRuntimeRepair').mockResolvedValueOnce(confirmation)
    const { store, wrapper } = await mountStaleRuntimeProject('billing')
    const getSnapshot = vi.spyOn(harborBridge, 'getSnapshot')

    const inspect = wrapper.findAll('button').find((button) => button.text().includes('Check for stale runtime'))
    if (!inspect) throw new Error('Inspect stale runtime action is missing')
    await inspect.trigger('click')
    await flushPromises()
    bodyButton('Stop this process and reset project').click()
    await flushPromises()

    expect(confirmRuntime).toHaveBeenCalledWith(
      'billing',
      inspection.confirmable.inspection_id,
      inspection.confirmable.candidate_fingerprint,
    )
    expect(store.projectRuntimeRepairInspection).toBeNull()
    expect(getSnapshot).toHaveBeenCalledOnce()
    expect(store.projectById('billing')?.state).toBe('stopped')
    wrapper.unmount()
  })

  it('keeps confirmation disabled after expiry or while connection state is unsafe', async () => {
    const inspection = confirmableInspection()
    vi.spyOn(harborBridge, 'inspectProjectRuntimeRepair').mockResolvedValueOnce(inspection)
    const confirmRuntime = vi.spyOn(harborBridge, 'confirmProjectRuntimeRepair')
    const { store, wrapper } = await mountStaleRuntimeProject('billing')

    const inspect = wrapper.findAll('button').find((button) => button.text().includes('Check for stale runtime'))
    if (!inspect) throw new Error('Inspect stale runtime action is missing')
    await inspect.trigger('click')
    await flushPromises()
    const confirm = bodyButton('Stop this process and reset project')

    store.$patch({ snapshotStale: true })
    await wrapper.vm.$nextTick()
    expect(confirm.disabled).toBe(true)

    store.$patch({ snapshotStale: false, connectionState: 'disconnected' })
    await wrapper.vm.$nextTick()
    expect(confirm.disabled).toBe(true)

    store.$patch({ connectionState: 'connected', settingUpNetwork: true })
    await wrapper.vm.$nextTick()
    expect(confirm.disabled).toBe(true)

    const expired = confirmableInspection()
    expired.confirmable.expires_at = '2020-01-01T00:00:00Z'
    store.$patch({ settingUpNetwork: false, projectRuntimeRepairInspection: expired })
    await flushPromises()
    expect(bodyButton('Stop this process and reset project').disabled).toBe(true)
    expect(document.body.textContent).toContain('This inspection has expired.')
    expect(confirmRuntime).not.toHaveBeenCalled()
    wrapper.unmount()
  })
})

describe('ProjectView network admission', () => {
  it.each([
    ['network readiness', 'project.network.setup_required', 'Secure networking is not ready', 'Set up Harbor\'s secure, trusted local DNS, HTTPS, and ingress before starting this project.'],
    ['runtime', 'project.process.exited', 'Project action failed', 'The development runtime exited unexpectedly.'],
  ])('keeps the %s lifecycle error above the primary tabs across project surfaces', async (_, problemCode, title, message) => {
    const { store, wrapper } = await mountProject()
    store.$patch({
      projectLifecycleErrors: { 'orders-api': message },
      projectLifecycleProblemCodes: { 'orders-api': problemCode },
    })
    await wrapper.vm.$nextTick()

    const lifecycleAlert = wrapper.find('[role="alert"]')
    const primaryTabList = wrapper.find('[role="tablist"]')
    expect(lifecycleAlert.text()).toContain(title)
    expect(lifecycleAlert.text()).toContain(message)
    expect(lifecycleAlert.element.compareDocumentPosition(primaryTabList.element) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy()

    for (const tabLabel of ['Overview', 'Development output', 'Services', 'Resources']) {
      await detailTab(wrapper, tabLabel).trigger('mousedown', { button: 0 })
      await wrapper.vm.$nextTick()
      expect(lifecycleAlert.isVisible()).toBe(true)
      expect(lifecycleAlert.text()).toContain(message)
    }

    wrapper.unmount()
  })

  it('offers the same trusted-ingress setup action when full network authority is missing', async () => {
    const { wrapper } = await mountFullNetworkBlockedProject()
    const setupNetwork = vi.spyOn(harborBridge, 'setupNetwork').mockResolvedValue(structuredClone(harborWireFixture.setup_network))
    const startProject = vi.spyOn(harborBridge, 'startProject').mockImplementation(async (projectId, intentId) => {
      const result = structuredClone(harborWireFixture.start_project)
      result.operation.project_id = projectId
      result.operation.intent_id = intentId
      return result
    })

    expect(wrapper.text()).toContain('Secure networking is not ready')
    expect(wrapper.text()).toContain('Harbor\'s DNS foundation is active')
    expect(wrapper.text()).toContain('secure, trusted local ingress')
    const setup = bodyButton('Set up secure networking and start')
    expect(setup.disabled).toBe(false)
    await setup.click()
    await flushPromises()

    expect(setupNetwork).toHaveBeenCalledOnce()
    expect(startProject).toHaveBeenCalledWith('orders-api', expect.stringMatching(/^desktop-project-start-/))

    wrapper.unmount()
  })

  it('offers the trusted-ingress setup action when initial network setup is required', async () => {
    const { store, wrapper } = await mountProject()
    const setupNetwork = vi.spyOn(harborBridge, 'setupNetwork').mockResolvedValue(structuredClone(harborWireFixture.setup_network))
    const startProject = vi.spyOn(harborBridge, 'startProject').mockImplementation(async (projectId, intentId) => {
      const result = structuredClone(harborWireFixture.start_project)
      result.operation.project_id = projectId
      result.operation.intent_id = intentId
      return result
    })
    store.$patch({
      projectLifecycleErrors: {
        'orders-api': 'Network setup is required.',
      },
      projectLifecycleProblemCodes: {
        'orders-api': 'project.network.setup_required',
      },
    })
    await wrapper.vm.$nextTick()

    expect(wrapper.text()).toContain('secure, trusted local DNS, HTTPS, and ingress')
    const setup = bodyButton('Set up secure networking and start')
    expect(setup.disabled).toBe(false)
    await setup.click()
    await flushPromises()

    expect(setupNetwork).toHaveBeenCalledOnce()
    expect(startProject).toHaveBeenCalledWith('orders-api', expect.stringMatching(/^desktop-project-start-/))

    wrapper.unmount()
  })
})

describe('ProjectView project removal approval', () => {
  it('surfaces a pending approval action and completes removal through the typed bridge', async () => {
    const { store, router, wrapper } = await mountProject()
    const approval = structuredClone(harborWireFixture.approve_project_removal)
    const confirmed = mockSnapshot()
    confirmed.sequence = approval.revision
    confirmed.projects = confirmed.projects.filter((project) => project.id !== 'orders-api')
    confirmed.operations = confirmed.operations.filter((operation) => operation.project_id !== 'orders-api')
    confirmed.recent_resource_ids = confirmed.recent_resource_ids.filter((reference) => reference.project_id !== 'orders-api')
    const approveProjectRemoval = vi.spyOn(harborBridge, 'approveProjectRemoval').mockImplementationOnce(async (projectId, intentId) => {
      approval.operation.project_id = projectId
      approval.operation.intent_id = intentId
      return approval
    })
    vi.spyOn(harborBridge, 'getSnapshot').mockResolvedValueOnce(confirmed)
    store.$patch({
      projectRemovalNotices: {
        'orders-api': {
          state: 'requires_approval',
          title: 'Administrator approval required',
          message: 'Approve the one-time administrator action to continue.',
        },
      },
    })
    await wrapper.vm.$nextTick()

    expect(wrapper.text()).toContain('Approve the one-time administrator action to continue.')
    const approve = bodyButton('Approve and remove')
    expect(approve.disabled).toBe(false)
    await approve.click()
    await flushPromises()

    expect(approveProjectRemoval).toHaveBeenCalledWith('orders-api', expect.stringMatching(/^desktop-project-remove-/))
    expect(router.currentRoute.value.path).toBe('/projects')
    expect(store.projectById('orders-api')).toBeUndefined()
    wrapper.unmount()
  })
})

describe('ProjectView project restart', () => {
  it('exposes restart for a ready project and sends the typed intent', async () => {
    const { wrapper } = await mountProject('orders-api')
    const restart = vi.spyOn(harborBridge, 'restartProject').mockImplementationOnce(async (projectId, intentId) => {
      const result = structuredClone(harborWireFixture.restart_project)
      result.operation.project_id = projectId
      result.operation.intent_id = intentId
      return result
    })

    const button = bodyButton('Restart project')
    expect(button.disabled).toBe(false)
    await button.click()
    await flushPromises()

    expect(restart).toHaveBeenCalledWith('orders-api', expect.stringMatching(/^desktop-project-restart-/))
    wrapper.unmount()
  })

  it('allows an uncertain restart retry while blocking the conflicting stop action', async () => {
    vi.spyOn(harborBridge, 'restartProject').mockRejectedValueOnce(new Error('connection closed before the operation response'))
    const { store, wrapper } = await mountProject('orders-api')

    await store.restartProject('orders-api')
    await flushPromises()

    expect(bodyButton('Stop project').disabled).toBe(true)
    expect(bodyButton('Restart project').disabled).toBe(false)
    expect(bodyButton('Stop project').querySelector('.animate-spin')).toBeNull()
    expect(bodyButton('Restart project').querySelector('.animate-spin')).toBeNull()
    wrapper.unmount()
  })
})
