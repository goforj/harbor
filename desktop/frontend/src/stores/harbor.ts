import { computed, ref } from 'vue'
import { defineStore } from 'pinia'
import { harborBridge } from '@/bridge'
import type {
  ConnectionEvent,
  ConnectionState,
  DaemonStatus,
  HarborSnapshot,
  Operation,
  OperationState,
  ProjectRegistration,
  ProjectResource,
  ProjectService,
  ProjectUnregistration,
} from '@/domain/harbor'

export interface ProjectRemovalNotice {
  state: OperationState | 'busy' | 'incomplete' | 'request_failed'
  title: string
  message: string
}

interface TrackedProjectRemovalIntent {
  id: string
  revision?: number
  state: 'active' | 'new' | 'uncertain'
}

export const useHarborStore = defineStore('harbor', () => {
  const snapshot = ref<HarborSnapshot | null>(null)
  const daemonStatus = ref<DaemonStatus | null>(null)
  const connectionState = ref<ConnectionState>('connecting')
  const snapshotStale = ref(true)
  const refreshing = ref(false)
  const addingProject = ref(false)
  const removingProjectId = ref<string | null>(null)
  const projectRemovalNotices = ref<Record<string, ProjectRemovalNotice>>({})
  const error = ref<string | null>(null)
  const actionError = ref<string | null>(null)
  const projectRegistrationError = ref<string | null>(null)
  let connectionEpoch = 0
  let statusRequest = 0
  let refreshRequest = 0
  let acceptedSnapshots = 0
  let snapshotNeedsBaseline = true
  let unsubscribeSnapshot: (() => void) | null = null
  let unsubscribeConnection: (() => void) | null = null
  const projectRemovalIntents = new Map<string, TrackedProjectRemovalIntent>()

  const projects = computed(() => snapshot.value?.projects ?? [])
  const services = computed<ProjectService[]>(() => projects.value.flatMap((project) =>
    project.services.map((service) => ({
      ...service,
      project_id: project.id,
      project_name: project.name,
    })),
  ))
  const resources = computed<ProjectResource[]>(() => projects.value.flatMap((project) =>
    project.resources.map((resource) => ({
      ...resource,
      project_id: project.id,
      project_name: project.name,
    })),
  ))
  const recentResources = computed<ProjectResource[]>(() => {
    if (!snapshot.value) {
      return []
    }

    return snapshot.value.recent_resource_ids.flatMap((reference) => {
      const project = projects.value.find((entry) => entry.id === reference.project_id)
      const resource = project?.resources.find((entry) => entry.id === reference.resource_id)
      return project && resource
        ? [{ ...resource, project_id: project.id, project_name: project.name }]
        : []
    })
  })
  const operations = computed(() => snapshot.value?.operations ?? [])
  const attentionCount = computed(() => projects.value.filter((project) =>
    project.state === 'failed' || project.state === 'degraded' || project.state === 'unavailable',
  ).length)
  const runningCount = computed(() => projects.value.filter((project) =>
    project.state === 'ready' || project.state === 'starting' || project.state === 'rebuilding' || project.state === 'degraded',
  ).length)
  const loading = computed(() => refreshing.value || (!snapshot.value
    && (connectionState.value === 'connecting' || connectionState.value === 'connected')))
  const connectionMessage = computed(() => {
    if (!snapshot.value) {
      switch (connectionState.value) {
        case 'connecting':
          return 'Connecting to Harbor'
        case 'connected':
          return 'Connected to Harbor. Waiting for the first snapshot.'
        case 'disconnected':
          return 'Harbor could not load local state'
      }
    }

    if (!snapshotStale.value) {
      return null
    }

    switch (connectionState.value) {
      case 'connecting':
        return 'Reconnecting to Harbor. Showing the last snapshot.'
      case 'connected':
        return 'Connected to Harbor. Waiting for a fresh snapshot.'
      case 'disconnected':
        return error.value ?? 'Harbor is disconnected. Showing the last snapshot.'
    }
  })

  function acceptSnapshot(nextSnapshot: HarborSnapshot, confirmsCurrent = false) {
    if (!snapshotNeedsBaseline && nextSnapshot.sequence <= (snapshot.value?.sequence ?? 0)) {
      if (confirmsCurrent && nextSnapshot.sequence === snapshot.value?.sequence) {
        reconcileProjectRemovals(nextSnapshot)
      }
      if (confirmsCurrent
        && snapshotStale.value
        && nextSnapshot.sequence === snapshot.value?.sequence) {
        snapshotStale.value = false
        connectionState.value = 'connected'
        error.value = null
        acceptedSnapshots += 1
        return true
      }
      return false
    }

    const establishesBaseline = snapshotNeedsBaseline
    snapshot.value = nextSnapshot
    reconcileProjectRemovals(nextSnapshot, establishesBaseline)
    snapshotNeedsBaseline = false
    snapshotStale.value = false
    connectionState.value = 'connected'
    error.value = null
    acceptedSnapshots += 1
    return true
  }

  function transitionConnection(event: ConnectionEvent) {
    connectionEpoch += 1
    statusRequest += 1
    connectionState.value = event.state
    snapshotStale.value = true
    snapshotNeedsBaseline = true
    if (event.state === 'disconnected' && !error.value) {
      error.value = 'Harbor daemon is disconnected.'
    }

    if (event.state === 'connected') {
      void refreshStatus(connectionEpoch)
    }
  }

  async function refreshStatus(epoch = connectionEpoch) {
    const request = ++statusRequest
    try {
      const status = await harborBridge.getStatus()
      if (epoch !== connectionEpoch || request !== statusRequest) {
        return
      }
      daemonStatus.value = status
      connectionState.value = 'connected'
    } catch {
      // Connection events own transport state; a single diagnostic failure must not regress a newer result.
    }
  }

  async function refresh() {
    const epoch = connectionEpoch
    const statusRequestForRefresh = ++statusRequest
    const refreshRequestForCall = ++refreshRequest
    const acceptedSnapshotsBeforeRefresh = acceptedSnapshots
    const uncertainRemovalIntents = new Map([...projectRemovalIntents]
      .filter(([projectId, tracked]) => tracked.state === 'uncertain' && removingProjectId.value !== projectId)
      .map(([projectId, tracked]) => [projectId, tracked.id]))
    refreshing.value = true
    const [statusResult, snapshotResult] = await Promise.allSettled([
      harborBridge.getStatus(),
      harborBridge.getSnapshot(),
    ])

    if (epoch !== connectionEpoch) {
      if (refreshRequestForCall === refreshRequest) {
        refreshing.value = false
      }
      return false
    }

    if (statusResult.status === 'fulfilled' && statusRequestForRefresh === statusRequest) {
      daemonStatus.value = statusResult.value
      connectionState.value = 'connected'
    }
    if (snapshotResult.status === 'fulfilled') {
      acceptSnapshot(snapshotResult.value, true)
    }

    const snapshotWasSuperseded = acceptedSnapshots !== acceptedSnapshotsBeforeRefresh
      && snapshotResult.status === 'rejected'
    if (snapshotResult.status === 'rejected' && !snapshotWasSuperseded) {
      snapshotStale.value = true
    }

    if (statusResult.status === 'rejected'
      && snapshotResult.status === 'rejected'
      && statusRequestForRefresh === statusRequest) {
      connectionEpoch += 1
      statusRequest += 1
      connectionState.value = 'disconnected'
      snapshotStale.value = true
      snapshotNeedsBaseline = true
    }

    const failure = snapshotResult.status === 'rejected'
      ? snapshotResult.reason
      : statusResult.status === 'rejected'
        ? statusResult.reason
        : null
    if (failure
      && snapshotStale.value
      && !snapshotWasSuperseded) {
      error.value = failure instanceof Error ? failure.message : 'Unable to load Harbor state.'
    }
    if (snapshotResult.status === 'fulfilled') {
      for (const [projectId, intentId] of uncertainRemovalIntents) {
        confirmUncertainProjectRemoval(projectId, intentId)
      }
    }
    if (refreshRequestForCall === refreshRequest) {
      refreshing.value = false
    }
    return snapshotResult.status === 'fulfilled'
  }

  async function initialize() {
    unsubscribeSnapshot?.()
    unsubscribeConnection?.()
    unsubscribeConnection = harborBridge.subscribeConnection(transitionConnection)
    unsubscribeSnapshot = harborBridge.subscribe((nextSnapshot) => {
      acceptSnapshot(nextSnapshot)
      void refreshStatus(connectionEpoch)
    })
    await refresh()
  }

  function dispose() {
    unsubscribeSnapshot?.()
    unsubscribeConnection?.()
    unsubscribeSnapshot = null
    unsubscribeConnection = null
  }

  function projectById(projectId: string) {
    return projects.value.find((project) => project.id === projectId)
  }

  function serviceById(projectId: string, serviceId: string) {
    return services.value.find((service) => service.project_id === projectId && service.id === serviceId)
  }

  function stageProjectRegistration(registration: ProjectRegistration) {
    const current = snapshot.value
    if (!current || registration.revision < current.sequence) {
      return
    }

    const projects = [...current.projects]
    const existingIndex = projects.findIndex((project) => project.id === registration.project.id)
    if (existingIndex < 0) {
      projects.push(registration.project)
    }
    else {
      projects[existingIndex] = registration.project
    }
    snapshot.value = {
      ...current,
      sequence: registration.revision,
      projects,
    }
    snapshotStale.value = true
  }

  async function addProject(): Promise<ProjectRegistration | null> {
    addingProject.value = true
    projectRegistrationError.value = null
    try {
      const result = await harborBridge.addProject()
      if (result.canceled) {
        return null
      }
      if (!result.registration) {
        throw new Error('Harbor returned an incomplete project registration.')
      }

      stageProjectRegistration(result.registration)
      await refresh()
      return result.registration
    }
    catch (cause) {
      projectRegistrationError.value = cause instanceof Error
        ? cause.message
        : 'Harbor could not add the project.'
      return null
    }
    finally {
      addingProject.value = false
    }
  }

  function projectRemovalNotice(projectId: string) {
    return projectRemovalNotices.value[projectId]
  }

  function setProjectRemovalNotice(projectId: string, notice: ProjectRemovalNotice | null) {
    const notices = { ...projectRemovalNotices.value }
    if (notice) {
      notices[projectId] = notice
    }
    else {
      delete notices[projectId]
    }
    projectRemovalNotices.value = notices
  }

  function reconcileProjectRemovals(nextSnapshot: HarborSnapshot, establishesBaseline = false) {
    const trackedBeforeSnapshot = new Map(projectRemovalIntents)
    const projectIds = new Set(nextSnapshot.projects.map((project) => project.id))
    const activeByProject = new Map<string, Operation>()
    for (const operation of nextSnapshot.operations) {
      if (operation.project_id && operation.kind === 'project.unregister' && isActiveOperation(operation)) {
        activeByProject.set(operation.project_id, operation)
      }
    }

    for (const [projectId, operation] of activeByProject) {
      projectRemovalIntents.set(projectId, {
        id: operation.intent_id,
        revision: nextSnapshot.sequence,
        state: 'active',
      })
      setProjectRemovalNotice(projectId, activeProjectRemovalNotice(operation))
    }

    for (const [projectId, tracked] of trackedBeforeSnapshot) {
      if (activeByProject.has(projectId)) {
        continue
      }
      if (!projectIds.has(projectId)) {
        projectRemovalIntents.delete(projectId)
        setProjectRemovalNotice(projectId, null)
        continue
      }
      // A snapshot accepted while the request is still in flight may have been captured before enqueue.
      if (removingProjectId.value === projectId) {
        continue
      }
      if (!establishesBaseline && tracked.revision !== undefined && nextSnapshot.sequence < tracked.revision) {
        continue
      }
      if (tracked.state === 'uncertain' && !establishesBaseline) {
        continue
      }

      projectRemovalIntents.delete(projectId)
      if (tracked.state === 'active') {
        const notice = projectRemovalNotice(projectId)
        if (notice?.state !== 'failed' && notice?.state !== 'cancelled') {
          setProjectRemovalNotice(projectId, {
            state: 'incomplete',
            title: 'Project removal is no longer active',
            message: 'The project remains registered. You can try again.',
          })
        }
      }
    }

    for (const projectId of Object.keys(projectRemovalNotices.value)) {
      if (!projectIds.has(projectId)) {
        setProjectRemovalNotice(projectId, null)
      }
    }
  }

  function activeProjectRemoval(projectId: string) {
    return operations.value.find((operation) => operation.project_id === projectId
      && operation.kind === 'project.unregister'
      && isActiveOperation(operation))
  }

  function projectRemovalIntent(projectId: string) {
    const active = activeProjectRemoval(projectId)
    if (active) {
      const tracked: TrackedProjectRemovalIntent = {
        id: active.intent_id,
        revision: snapshot.value?.sequence,
        state: 'active',
      }
      projectRemovalIntents.set(projectId, tracked)
      return tracked
    }

    const remembered = projectRemovalIntents.get(projectId)
    if (remembered) {
      return remembered
    }

    const created: TrackedProjectRemovalIntent = {
      id: newProjectRemovalIntent(),
      state: 'new',
    }
    projectRemovalIntents.set(projectId, created)
    return created
  }

  function confirmUncertainProjectRemoval(projectId: string, intentId: string) {
    if (removingProjectId.value === projectId) {
      return
    }
    const tracked = projectRemovalIntents.get(projectId)
    if (!tracked || tracked.id !== intentId || tracked.state !== 'uncertain') {
      return
    }

    const active = activeProjectRemoval(projectId)
    if (active?.intent_id === intentId) {
      projectRemovalIntents.set(projectId, {
        id: active.intent_id,
        revision: snapshot.value?.sequence,
        state: 'active',
      })
      setProjectRemovalNotice(projectId, activeProjectRemovalNotice(active))
      return
    }

    projectRemovalIntents.delete(projectId)
    if (snapshot.value && !projectById(projectId)) {
      setProjectRemovalNotice(projectId, null)
    }
  }

  function stageTerminalProjectRemoval(result: ProjectUnregistration) {
    const current = snapshot.value
    if (!current || result.revision < current.sequence) {
      return
    }

    const succeeded = result.operation.state === 'succeeded'
    const nextSnapshot: HarborSnapshot = {
      ...current,
      sequence: result.revision,
      projects: succeeded
        ? current.projects.filter((project) => project.id !== result.operation.project_id)
        : current.projects,
      operations: current.operations.filter((operation) => succeeded
        ? operation.project_id !== result.operation.project_id
        : operation.intent_id !== result.operation.intent_id),
      recent_resource_ids: succeeded
        ? current.recent_resource_ids.filter((reference) => reference.project_id !== result.operation.project_id)
        : current.recent_resource_ids,
    }
    snapshot.value = nextSnapshot
    snapshotStale.value = true
    reconcileProjectRemovals(nextSnapshot)
  }

  async function removeProject(projectId: string): Promise<ProjectUnregistration | null> {
    if (removingProjectId.value) {
      setProjectRemovalNotice(projectId, {
        state: 'busy',
        title: 'Another project removal is in progress',
        message: 'Wait for the current removal request to finish, then try again.',
      })
      return null
    }

    const tracked = projectRemovalIntent(projectId)
    removingProjectId.value = projectId
    setProjectRemovalNotice(projectId, null)

    try {
      const result = await harborBridge.removeProject(projectId, tracked.id)
      const operation = result.operation
      switch (operation.state) {
        case 'succeeded':
          projectRemovalIntents.delete(projectId)
          setProjectRemovalNotice(projectId, null)
          stageTerminalProjectRemoval(result)
          break
        case 'requires_approval':
          projectRemovalIntents.set(projectId, {
            id: operation.intent_id,
            revision: result.revision,
            state: 'active',
          })
          setProjectRemovalNotice(projectId, activeProjectRemovalNotice(operation))
          break
        case 'failed':
          projectRemovalIntents.delete(projectId)
          stageTerminalProjectRemoval(result)
          setProjectRemovalNotice(projectId, {
            state: operation.state,
            title: 'Project removal failed',
            message: operation.problem?.message ?? 'Harbor could not complete the project removal.',
          })
          break
        case 'cancelled':
          projectRemovalIntents.delete(projectId)
          stageTerminalProjectRemoval(result)
          setProjectRemovalNotice(projectId, {
            state: operation.state,
            title: 'Project removal cancelled',
            message: 'Harbor cancelled this removal before changing the project registration.',
          })
          break
        case 'queued':
        case 'running':
          projectRemovalIntents.set(projectId, {
            id: operation.intent_id,
            revision: result.revision,
            state: 'active',
          })
          setProjectRemovalNotice(projectId, activeProjectRemovalNotice(operation))
          break
      }
      return result
    }
    catch (cause) {
      const active = activeProjectRemoval(projectId)
      if (snapshot.value && !projectById(projectId)) {
        projectRemovalIntents.delete(projectId)
        setProjectRemovalNotice(projectId, null)
      }
      else if (active?.intent_id === tracked.id) {
        projectRemovalIntents.set(projectId, {
          id: active.intent_id,
          revision: snapshot.value?.sequence,
          state: 'active',
        })
        setProjectRemovalNotice(projectId, activeProjectRemovalNotice(active))
      }
      else {
        projectRemovalIntents.set(projectId, { ...tracked, state: 'uncertain' })
        setProjectRemovalNotice(projectId, {
          state: 'request_failed',
          title: 'Harbor could not start project removal',
          message: cause instanceof Error ? cause.message : 'The removal request failed before Harbor returned an operation.',
        })
      }
      return null
    }
    finally {
      removingProjectId.value = null
      await refresh()
    }
  }

  async function openResource(projectId: string, resourceId: string) {
    actionError.value = null
    try {
      await harborBridge.openResource(projectId, resourceId)
    } catch (cause) {
      actionError.value = cause instanceof Error ? cause.message : 'Harbor could not open the resource.'
    }
  }

  return {
    snapshot,
    daemonStatus,
    connectionState,
    connectionMessage,
    snapshotStale,
    refreshing,
    addingProject,
    removingProjectId,
    loading,
    error,
    actionError,
    projectRegistrationError,
    projectRemovalNotices,
    projects,
    services,
    resources,
    recentResources,
    operations,
    attentionCount,
    runningCount,
    refresh,
    initialize,
    dispose,
    projectById,
    serviceById,
    addProject,
    projectRemovalNotice,
    removeProject,
    openResource,
  }
})

function newProjectRemovalIntent(): string {
  const bytes = crypto.getRandomValues(new Uint8Array(16))
  const opaque = Array.from(bytes, (value) => value.toString(16).padStart(2, '0')).join('')
  return `desktop-project-remove-${opaque}`
}

function isActiveOperation(operation: Operation): boolean {
  return operation.state === 'queued'
    || operation.state === 'running'
    || operation.state === 'requires_approval'
}

function activeProjectRemovalNotice(operation: Operation): ProjectRemovalNotice {
  if (operation.state === 'requires_approval') {
    return {
      state: operation.state,
      title: 'Administrator approval required',
      message: 'Harbor paused removal until it can release this project’s local networking. Approval is not available from the desktop app yet.',
    }
  }
  return {
    state: operation.state,
    title: 'Project removal in progress',
    message: 'Harbor is releasing project-owned resources before removing the registration.',
  }
}
