import { flushPromises, mount, type VueWrapper } from '@vue/test-utils'
import { createPinia, setActivePinia, type Pinia } from 'pinia'
import { createMemoryHistory, createRouter, type Router } from 'vue-router'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { harborBridge } from '@/bridge'
import { harborWireFixture } from '@/bridge/harbor.fixture'
import { mockSnapshot } from '@/bridge/mock'
import type { ProjectLifecycleOperation } from '@/domain/harbor'
import { useHarborStore } from '@/stores/harbor'
import ProjectView from './ProjectView.vue'

const terminalSessions = vi.hoisted(() => {
  const sessions: Array<{
    close: ReturnType<typeof vi.fn>
    projectId: string
    useEnvironmentOverrides: boolean
  }> = []

  class ProjectTerminalSession {
    readonly close = vi.fn()
    readonly projectId: string
    readonly useEnvironmentOverrides: boolean

    constructor(_bridge: unknown, projectId: string, useEnvironmentOverrides: boolean) {
      this.projectId = projectId
      this.useEnvironmentOverrides = useEnvironmentOverrides
      sessions.push(this)
    }
  }

  return { ProjectTerminalSession, sessions }
})

vi.mock('@/components/harbor/InteractiveTerminal.vue', () => ({
  default: {
    name: 'InteractiveTerminal',
    props: ['session'],
    template: '<div data-testid="interactive-terminal" />',
  },
}))

vi.mock('@/lib/projectTerminalSession', () => ({
  ProjectTerminalSession: terminalSessions.ProjectTerminalSession,
}))

afterEach(async () => {
  delete window.runtime
  await flushPromises()
  terminalSessions.sessions.length = 0
})

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
    global: {
      plugins: [pinia, router],
    },
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
    global: {
      plugins: [pinia, router],
    },
  })
  await flushPromises()
  return { pinia, router, store, wrapper }
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

function bodyButton(label: string): HTMLButtonElement {
  const button = [...document.body.querySelectorAll('button')]
    .find((candidate) => candidate.textContent?.trim() === label || candidate.getAttribute('aria-label') === label)
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

  it.each(['Development output', 'Connect', 'Services', 'Resources'])('preserves the %s tab when the selected project starts', async (tabLabel) => {
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

describe('ProjectView service connections', () => {
  it('loads every project service and copies the published hostname and address', async () => {
    const getServiceLogs = vi.spyOn(harborBridge, 'getServiceLogs')
    const ClipboardSetText = vi.fn().mockResolvedValue(true)
    window.runtime = { ClipboardSetText }
    const { wrapper } = await mountProject('orders-api')

    await detailTab(wrapper, 'Connect').trigger('mousedown', { button: 0 })
    await flushPromises()

    expect(getServiceLogs).toHaveBeenCalledWith('orders-api', '', 'mysql', 0)
    expect(getServiceLogs).toHaveBeenCalledWith('orders-api', '', 'redis', 0)
    expect(wrapper.text()).toContain('mysql.orders-api.test')
    expect(wrapper.text()).toContain('3306 · TCP')
    expect(wrapper.text()).toContain('No host connection is currently published for this service.')

    const copyHost = wrapper.find('button[aria-label="Copy mysql.orders-api.test hostname"]')
    const copyAddress = wrapper.find('button[aria-label="Copy mysql.orders-api.test:3306 address"]')
    if (!copyHost.exists() || !copyAddress.exists()) throw new Error('Connection copy actions are missing')
    await copyHost.trigger('click')
    await copyAddress.trigger('click')

    expect(ClipboardSetText).toHaveBeenNthCalledWith(1, 'mysql.orders-api.test')
    expect(ClipboardSetText).toHaveBeenNthCalledWith(2, 'mysql.orders-api.test:3306')

    wrapper.unmount()
  })
})

describe('ProjectView interactive terminal', () => {
  it('uses project environment overrides by default and lets new terminals opt out from the title bar', async () => {
    const { wrapper } = await mountProject('orders-api')

    await detailTab(wrapper, 'Terminal').trigger('mousedown', { button: 0 })
    await wrapper.vm.$nextTick()
    expect(terminalSessions.sessions[0]?.useEnvironmentOverrides).toBe(true)

    const environmentOverrides = wrapper.get('[aria-label="Use environment overrides for new terminals"]')
    expect(environmentOverrides.attributes('data-state')).toBe('checked')
    await environmentOverrides.trigger('click')
    await wrapper.get('button[aria-label="New terminal"]').trigger('click')
    await wrapper.vm.$nextTick()

    expect(environmentOverrides.attributes('data-state')).toBe('unchecked')
    expect(terminalSessions.sessions[1]?.useEnvironmentOverrides).toBe(false)
    wrapper.unmount()
  })

  it('creates independent named terminal tabs, switches between their mounted emulators, and closes one shell at a time', async () => {
    const { wrapper } = await mountProject('orders-api')

    expect(wrapper.find('[data-testid="interactive-terminal"]').exists()).toBe(false)
    await detailTab(wrapper, 'Terminal').trigger('mousedown', { button: 0 })
    await wrapper.vm.$nextTick()
    expect(wrapper.find('[data-testid="interactive-terminal"]').exists()).toBe(true)
    expect(wrapper.text()).toContain('Terminal 1')
    expect(terminalSessions.sessions).toHaveLength(1)

    const newTerminal = wrapper.get('button[aria-label="New terminal"]')
    await newTerminal.trigger('click')
    await wrapper.vm.$nextTick()
    expect(wrapper.text()).toContain('Terminal 2')
    expect(wrapper.findAll('[data-testid="interactive-terminal"]')).toHaveLength(2)
    expect(terminalSessions.sessions).toHaveLength(2)

    const terminalOne = wrapper.findAll('[role="tab"]').find((tab) => tab.text() === 'Terminal 1')
    if (!terminalOne) throw new Error('Terminal 1 tab is missing')
    await terminalOne.trigger('click')
    await wrapper.vm.$nextTick()
    expect(terminalOne.attributes('aria-selected')).toBe('true')

    await wrapper.get('button[aria-label="Close Terminal 1"]').trigger('click')
    await flushPromises()
    expect(terminalSessions.sessions[0]?.close).toHaveBeenCalledOnce()
    expect(terminalSessions.sessions[1]?.close).not.toHaveBeenCalled()
    expect(wrapper.findAll('[data-testid="interactive-terminal"]')).toHaveLength(1)
    expect(wrapper.text()).not.toContain('Terminal 1')

    wrapper.unmount()
  })

  it('preserves every terminal while another project tab is active and closes them when leaving the project', async () => {
    const { router, wrapper } = await mountProject('orders-api')

    await detailTab(wrapper, 'Terminal').trigger('mousedown', { button: 0 })
    await wrapper.get('button[aria-label="New terminal"]').trigger('click')
    await wrapper.vm.$nextTick()
    expect(terminalSessions.sessions).toHaveLength(2)

    await detailTab(wrapper, 'Overview').trigger('mousedown', { button: 0 })
    await flushPromises()
    expect(wrapper.findAll('[data-testid="interactive-terminal"]')).toHaveLength(2)
    expect(terminalSessions.sessions.every((session) => session.close.mock.calls.length === 0)).toBe(true)

    await detailTab(wrapper, 'Terminal').trigger('mousedown', { button: 0 })
    await wrapper.vm.$nextTick()
    expect(wrapper.findAll('[data-testid="interactive-terminal"]')).toHaveLength(2)
    expect(terminalSessions.sessions).toHaveLength(2)
    await router.push('/projects')
    await router.isReady()
    await flushPromises()
    expect(terminalSessions.sessions.every((session) => session.close.mock.calls.length === 1)).toBe(true)
  })

  it('retains independent terminal workspaces while switching between projects', async () => {
    const { router, wrapper } = await mountProject('orders-api')

    await detailTab(wrapper, 'Terminal').trigger('mousedown', { button: 0 })
    await wrapper.vm.$nextTick()
    expect(terminalSessions.sessions.map((session) => session.projectId)).toEqual(['orders-api'])

    await router.push('/projects/billing')
    await router.isReady()
    await flushPromises()
    await detailTab(wrapper, 'Terminal').trigger('mousedown', { button: 0 })
    await wrapper.vm.$nextTick()

    expect(terminalSessions.sessions.map((session) => session.projectId)).toEqual(['orders-api', 'billing'])
    expect(terminalSessions.sessions[0]?.close).not.toHaveBeenCalled()
    expect(wrapper.findAll('[data-testid="interactive-terminal"]')).toHaveLength(2)

    await router.push('/projects/orders-api')
    await router.isReady()
    await flushPromises()
    await detailTab(wrapper, 'Terminal').trigger('mousedown', { button: 0 })
    await wrapper.vm.$nextTick()

    expect(terminalSessions.sessions).toHaveLength(2)
    expect(terminalSessions.sessions.every((session) => session.close.mock.calls.length === 0)).toBe(true)
    expect(wrapper.findAll('[data-testid="interactive-terminal"]')).toHaveLength(2)
    expect(wrapper.text()).toContain('Terminal 1')

    wrapper.unmount()
    await flushPromises()
    expect(terminalSessions.sessions.every((session) => session.close.mock.calls.length === 1)).toBe(true)
  })

  it('prevents the UI from exceeding the desktop terminal session limit', async () => {
    const { wrapper } = await mountProject('orders-api')

    await detailTab(wrapper, 'Terminal').trigger('mousedown', { button: 0 })
    const newTerminal = wrapper.get('button[aria-label="New terminal"]')
    for (let index = 1; index < 8; index += 1) {
      await newTerminal.trigger('click')
    }
    await wrapper.vm.$nextTick()

    expect(terminalSessions.sessions).toHaveLength(8)
    expect(wrapper.findAll('[data-testid="interactive-terminal"]')).toHaveLength(8)
    expect(newTerminal.attributes('disabled')).toBeDefined()
    expect(newTerminal.attributes('title')).toBe('Close a terminal tab before opening another.')

    wrapper.unmount()
  })

  it('waits for a closing shell to release its backend slot before allowing a replacement', async () => {
    const pendingClose = deferred<void>()
    const { wrapper } = await mountProject('orders-api')

    await detailTab(wrapper, 'Terminal').trigger('mousedown', { button: 0 })
    const newTerminal = wrapper.get('button[aria-label="New terminal"]')
    for (let index = 1; index < 8; index += 1) {
      await newTerminal.trigger('click')
    }
    terminalSessions.sessions[0]?.close.mockReturnValue(pendingClose.promise)

    await wrapper.get('button[aria-label="Close Terminal 1"]').trigger('click')
    await wrapper.vm.$nextTick()
    expect(newTerminal.attributes('disabled')).toBeDefined()
    await newTerminal.trigger('click')
    expect(terminalSessions.sessions).toHaveLength(8)

    pendingClose.resolve(undefined)
    await flushPromises()
    expect(newTerminal.attributes('disabled')).toBeUndefined()
    await newTerminal.trigger('click')
    expect(terminalSessions.sessions).toHaveLength(9)
    expect(wrapper.findAll('[data-testid="interactive-terminal"]')).toHaveLength(8)

    wrapper.unmount()
  })

  it('retains a failed close reservation until terminal cleanup is retried', async () => {
    const { wrapper } = await mountProject('orders-api')

    await detailTab(wrapper, 'Terminal').trigger('mousedown', { button: 0 })
    const newTerminal = wrapper.get('button[aria-label="New terminal"]')
    for (let index = 1; index < 8; index += 1) {
      await newTerminal.trigger('click')
    }
    terminalSessions.sessions[0]?.close
      .mockRejectedValueOnce(new Error('desktop transport closed'))
      .mockResolvedValueOnce(undefined)

    await wrapper.get('button[aria-label="Close Terminal 1"]').trigger('click')
    await flushPromises()
    expect(newTerminal.attributes('disabled')).toBeDefined()
    expect(wrapper.text()).toContain('desktop transport closed')

    const retryCleanup = wrapper.findAll('button').find((button) => button.text() === 'Retry cleanup')
    if (!retryCleanup) throw new Error('Retry cleanup action is missing')
    await retryCleanup.trigger('click')
    await flushPromises()

    expect(terminalSessions.sessions[0]?.close).toHaveBeenCalledTimes(2)
    expect(newTerminal.attributes('disabled')).toBeUndefined()
    expect(wrapper.text()).not.toContain('desktop transport closed')

    wrapper.unmount()
  })

  it('shares failed cleanup reservations with the next project view instance', async () => {
    const first = await mountProject('orders-api')

    await detailTab(first.wrapper, 'Terminal').trigger('mousedown', { button: 0 })
    terminalSessions.sessions[0]?.close
      .mockRejectedValueOnce(new Error('desktop transport closed'))
      .mockResolvedValueOnce(undefined)
    await first.wrapper.get('button[aria-label="Close Terminal 1"]').trigger('click')
    await flushPromises()
    expect(terminalSessions.sessions[0]?.close).toHaveBeenCalledOnce()
    first.wrapper.unmount()

    const second = await mountProject('orders-api')
    await detailTab(second.wrapper, 'Terminal').trigger('mousedown', { button: 0 })
    await second.wrapper.vm.$nextTick()
    expect(terminalSessions.sessions).toHaveLength(1)
    expect(second.wrapper.find('[data-testid="interactive-terminal"]').exists()).toBe(false)

    await new Promise((resolve) => window.setTimeout(resolve, 550))
    await flushPromises()

    expect(terminalSessions.sessions[0]?.close).toHaveBeenCalledTimes(2)
    expect(terminalSessions.sessions).toHaveLength(2)
    expect(second.wrapper.find('[data-testid="interactive-terminal"]').exists()).toBe(true)
    second.wrapper.unmount()
  })
})

describe('ProjectView stale runtime recovery', () => {
  it('keeps project detail content in compact, task-focused tabs', async () => {
    const { wrapper } = await mountRecoveryProject()

    const tabLabels = wrapper.get('[data-slot="tabs-list"]').findAll('[role="tab"]').map((tab) => tab.text().replace(/\s+\d+$/, ''))
    expect(tabLabels).toEqual(['Overview', 'Development output', 'Terminal', 'Environment', 'Connect', 'Services', 'Resources'])
    expect(wrapper.text()).toContain('Apps')
    expect(wrapper.text()).not.toContain('Reported services for this project.')

    wrapper.unmount()
  })

  it('keeps the ordinary start action available for a recovered project', async () => {
    const { store, wrapper } = await mountRecoveryProject()
    const recover = bodyButton('Start project')
    expect(recover.disabled).toBe(false)
    expect(recover.dataset.variant).toBe('ghost')
    expect(wrapper.get('[data-testid="project-address-bar"]').text()).toContain('https://billing.test')
    expect(wrapper.get('[data-testid="project-address-bar"] span').classes()).toContain('text-muted-foreground')
    expect(wrapper.findAll('button').some((button) => button.attributes('aria-label') === 'Check for stale runtime')).toBe(false)

    store.$patch({ snapshotStale: true })
    await wrapper.vm.$nextTick()
    expect(recover.disabled).toBe(true)

    store.$patch({ snapshotStale: false, connectionState: 'disconnected' })
    await wrapper.vm.$nextTick()
    expect(recover.disabled).toBe(true)

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
    expect(recover.disabled).toBe(true)

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

    await bodyButton('Start project').click()
    await flushPromises()

    expect(start).toHaveBeenCalledWith('billing', expect.any(String))
    expect(inspect).not.toHaveBeenCalled()
    wrapper.unmount()
  })
})

describe('ProjectView network admission', () => {
  it.each([
    ['network readiness', 'project.network.setup_required', 'Secure networking is not ready', 'Set up Harbor\'s secure, trusted local DNS, HTTPS, and ingress before starting this project.'],
    ['missing checkout', 'project.checkout.missing', 'Project folder is missing', 'The project folder no longer exists. Restore it at its registered path or remove the project from Harbor.'],
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

    for (const tabLabel of ['Overview', 'Development output', 'Connect', 'Services', 'Resources']) {
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

  it('repairs missing owned loopback addresses before retrying project start', async () => {
    const { store, wrapper } = await mountProject()
    const repairNetwork = vi.spyOn(harborBridge, 'repairNetwork').mockResolvedValue(structuredClone(harborWireFixture.setup_network))
    const setupNetwork = vi.spyOn(harborBridge, 'setupNetwork')
    const startProject = vi.spyOn(harborBridge, 'startProject').mockImplementation(async (projectId, intentId) => {
      const result = structuredClone(harborWireFixture.start_project)
      result.operation.project_id = projectId
      result.operation.intent_id = intentId
      return result
    })
    store.$patch({
      projectLifecycleErrors: {
        'orders-api': 'The assigned Harbor address is not configured exactly on this machine.',
      },
      projectLifecycleProblemCodes: {
        'orders-api': 'project.network.identity_unavailable',
      },
    })
    await wrapper.vm.$nextTick()

    expect(wrapper.text()).toContain('Harbor networking needs repair')
    expect(wrapper.text()).toContain('macOS no longer has Harbor\'s assigned loopback addresses')
    const repair = bodyButton('Repair networking and start')
    await repair.click()
    await flushPromises()

    expect(repairNetwork).toHaveBeenCalledOnce()
    expect(setupNetwork).not.toHaveBeenCalled()
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
  it('keeps ready-project header actions compact and accessible', async () => {
    const { wrapper } = await mountProject('orders-api')

    expect(wrapper.get('[data-testid="project-address-bar"]').text()).toContain('https://orders.test')
    expect(bodyButton('Copy project URL').dataset.size).toBe('icon-sm')

    for (const label of ['Stop project', 'Restart project', 'Remove project', 'Open resource']) {
      const button = bodyButton(label)
      expect(button.dataset.size).toBe('icon-sm')
      expect(button.textContent?.trim()).toBe('')
    }

    wrapper.unmount()
  })

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

  it('shows a rotating progress arc while the restart request is pending', async () => {
    let resolveRestart: ((value: typeof harborWireFixture.restart_project) => void) | undefined
    vi.spyOn(harborBridge, 'restartProject').mockImplementationOnce(() => new Promise((resolve) => {
      resolveRestart = resolve
    }))
    const { wrapper } = await mountProject('orders-api')

    bodyButton('Restart project').click()
    await wrapper.vm.$nextTick()

    const progress = document.body.querySelector('[role="status"][aria-label="Restarting…"] .animate-spin')
    expect(progress).not.toBeNull()
    expect(progress?.classList.contains('border-r-transparent')).toBe(true)
    expect(progress?.classList.contains('border-b-transparent')).toBe(true)

    resolveRestart?.(structuredClone(harborWireFixture.restart_project))
    await flushPromises()
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
