import { computed, ref } from 'vue'
import { defineStore } from 'pinia'
import { harborBridge } from '@/bridge'
import type {
  ConnectionEvent,
  ConnectionState,
  DaemonStatus,
  HarborSnapshot,
  NetworkResolverPolicyMigrationOperation,
  NetworkSetupOperation,
  Operation,
  OperationState,
  Problem,
  ProjectActivity,
  ProjectLifecycleOperation,
  ProjectRegistration,
  ProjectResource,
  ProjectRuntimeRepairConfirmation,
  ProjectRuntimeRepairInspection,
  ProjectRuntimeRepairNotActionableReason,
  ProjectService,
  ProjectUnregistration,
  ServiceLogs,
} from '@/domain/harbor'

export interface ProjectRemovalNotice {
  state: OperationState | 'busy' | 'incomplete' | 'request_failed'
  title: string
  message: string
}

export interface ProjectRuntimeRepairNotice {
  state: 'blocked' | 'expired' | 'failed' | 'not_actionable' | 'succeeded' | 'unsupported'
  title: string
  message: string
}

interface TrackedProjectRemovalIntent {
  id: string
  revision?: number
  state: 'active' | 'new' | 'uncertain'
}

type ProjectLifecycleAction = 'start' | 'stop' | 'restart'
type ProjectRuntimeRepairAction = 'inspect' | 'confirm'

interface TrackedProjectLifecycleIntent {
  action: ProjectLifecycleAction
  id: string
}

interface TrackedProjectLifecycleProblem {
  id: string
  observed: boolean
  revision?: number
}

export const useHarborStore = defineStore('harbor', () => {
  const snapshot = ref<HarborSnapshot | null>(null)
  const daemonStatus = ref<DaemonStatus | null>(null)
  const connectionState = ref<ConnectionState>('connecting')
  const snapshotStale = ref(true)
  const refreshing = ref(false)
  const addingProject = ref(false)
  const settingUpNetwork = ref(false)
  const removingOldNetworking = ref(false)
  const networkSetupResult = ref<NetworkSetupOperation | null>(null)
  const networkSetupError = ref<string | null>(null)
  const oldNetworkingRemovalError = ref<string | null>(null)
  const removingProjectId = ref<string | null>(null)
  const projectRemovalApprovalProjectId = ref<string | null>(null)
  const projectLifecycleRequestProjectIds = ref<Record<string, true>>({})
  const projectLifecycleErrors = ref<Record<string, string>>({})
  const projectLifecycleProblemCodes = ref<Record<string, string>>({})
  const projectRemovalNotices = ref<Record<string, ProjectRemovalNotice>>({})
  const projectRuntimeRepairInspection = ref<ProjectRuntimeRepairInspection | null>(null)
  const projectRuntimeRepairProjectId = ref<string | null>(null)
  const projectRuntimeRepairAction = ref<ProjectRuntimeRepairAction | null>(null)
  const projectRuntimeRepairNotices = ref<Record<string, ProjectRuntimeRepairNotice>>({})
  const error = ref<string | null>(null)
  const actionError = ref<string | null>(null)
  const projectRegistrationError = ref<string | null>(null)
  let connectionEpoch = 0
  let statusRequest = 0
  let refreshRequest = 0
  let acceptedSnapshots = 0
  let snapshotNeedsBaseline = true
  let projectRuntimeRepairGeneration = 0
  let unsubscribeSnapshot: (() => void) | null = null
  let unsubscribeConnection: (() => void) | null = null
  const projectRemovalIntents = new Map<string, TrackedProjectRemovalIntent>()
  const projectLifecycleIntents = new Map<string, TrackedProjectLifecycleIntent>()
  const projectLifecycleIntentCount = ref(0)
  const projectLifecycleProblemIntents = new Map<string, TrackedProjectLifecycleProblem>()

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
  const projectLifecycleBusy = computed(() => Object.keys(projectLifecycleRequestProjectIds.value).length > 0
    || projectLifecycleIntentCount.value > 0
    || projects.value.some((project) => project.state === 'starting'
      || project.state === 'stopping'
      || project.state === 'rebuilding')
    || operations.value.some((operation) => (operation.kind === 'project.start'
      || operation.kind === 'project.stop'
      || operation.kind === 'project.restart')
      && isActiveOperation(operation)))
  const projectRuntimeRepairBusy = computed(() => projectRuntimeRepairProjectId.value !== null)
  const projectRemovalApprovalBusy = computed(() => projectRemovalApprovalProjectId.value !== null)
  const networkSetupOnboarding = computed(() => snapshot.value !== null
    && daemonStatus.value?.capabilities.includes('control.network-setup.v1') === true)
  const oldNetworkingRemovalAvailable = computed(() => daemonStatus.value?.capabilities
    .includes('control.network-resolver-policy-migration.v1') === true)
  const oldNetworkingRemovalBlocked = computed(() => !oldNetworkingRemovalAvailable.value
    || connectionState.value !== 'connected'
    || projectLifecycleBusy.value
    || settingUpNetwork.value
    || removingOldNetworking.value)
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
        reconcileProjectLifecycles(nextSnapshot)
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
    reconcileProjectLifecycles(nextSnapshot, establishesBaseline)
    snapshotNeedsBaseline = false
    snapshotStale.value = false
    connectionState.value = 'connected'
    error.value = null
    acceptedSnapshots += 1
    return true
  }

  function transitionConnection(event: ConnectionEvent) {
    discardProjectRuntimeRepair()
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
      discardProjectRuntimeRepair()
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

  async function setupNetwork(): Promise<NetworkSetupOperation | null> {
    if (settingUpNetwork.value || removingOldNetworking.value) {
      return null
    }
    if (projectLifecycleBusy.value) {
      networkSetupError.value = 'Wait for the current project action to finish, then try network setup again.'
      return null
    }

    settingUpNetwork.value = true
    networkSetupError.value = null
    try {
      const result = await harborBridge.setupNetwork()
      if (result.operation.kind !== 'network.setup'
        || result.operation.project_id
        || result.operation.state !== 'succeeded') {
        throw new Error('Harbor returned incomplete network setup progress.')
      }
      networkSetupResult.value = result
      return result
    }
    catch (cause) {
      networkSetupError.value = cause instanceof Error
        ? cause.message
        : 'Harbor could not set up local networking.'
      return null
    }
    finally {
      await refresh()
      settingUpNetwork.value = false
    }
  }

  async function removeOldNetworking(): Promise<NetworkResolverPolicyMigrationOperation | null> {
    if (oldNetworkingRemovalBlocked.value) {
      return null
    }

    removingOldNetworking.value = true
    oldNetworkingRemovalError.value = null
    try {
      const result = await harborBridge.removeOldNetworking()
      if (result.operation.kind !== 'network.resolver.policy-migration'
        || result.operation.project_id
        || result.operation.state !== 'succeeded'
        || result.operation.phase !== 'completed') {
        throw new Error('Harbor returned incomplete old networking removal progress.')
      }
      networkSetupError.value = null
      return result
    }
    catch (cause) {
      oldNetworkingRemovalError.value = cause instanceof Error
        ? cause.message
        : 'Harbor could not remove old networking.'
      return null
    }
    finally {
      await refresh()
      removingOldNetworking.value = false
    }
  }

  function projectRemovalNotice(projectId: string) {
    return projectRemovalNotices.value[projectId]
  }

  function activeProjectLifecycle(projectId: string) {
    return operations.value.find((operation) => operation.project_id === projectId
      && (operation.kind === 'project.start'
        || operation.kind === 'project.stop'
        || operation.kind === 'project.restart')
      && isActiveOperation(operation))
  }

  function projectLifecycleBusyFor(projectId: string) {
    const projectState = projectById(projectId)?.state
    return projectLifecycleRequestProjectIds.value[projectId] === true
      || activeProjectLifecycle(projectId) !== undefined
      || projectState === 'starting'
      || projectState === 'stopping'
      || projectState === 'rebuilding'
  }

  function projectLifecycleBlockedFor(projectId: string, action: ProjectLifecycleAction) {
    if (projectLifecycleBusyFor(projectId)) {
      return true
    }
    const remembered = projectLifecycleIntents.get(projectId)
    return remembered !== undefined && remembered.action !== action
  }

  function beginProjectLifecycleRequest(projectId: string) {
    projectLifecycleRequestProjectIds.value = {
      ...projectLifecycleRequestProjectIds.value,
      [projectId]: true,
    }
  }

  function endProjectLifecycleRequest(projectId: string) {
    if (!projectLifecycleRequestProjectIds.value[projectId]) {
      return
    }
    const requests = { ...projectLifecycleRequestProjectIds.value }
    delete requests[projectId]
    projectLifecycleRequestProjectIds.value = requests
  }

  function trackProjectLifecycleIntent(projectId: string, intent: TrackedProjectLifecycleIntent) {
    projectLifecycleIntents.set(projectId, intent)
    projectLifecycleIntentCount.value = projectLifecycleIntents.size
  }

  function forgetProjectLifecycleIntent(projectId: string) {
    projectLifecycleIntents.delete(projectId)
    projectLifecycleIntentCount.value = projectLifecycleIntents.size
  }

  function reconcileProjectLifecycles(nextSnapshot: HarborSnapshot, establishesBaseline = false) {
    const projectsById = new Map(nextSnapshot.projects.map((project) => [project.id, project]))
    const latestOperations = new Map<string, Operation>()
    for (const operation of nextSnapshot.operations) {
      if (!operation.project_id
        || (operation.kind !== 'project.start'
          && operation.kind !== 'project.stop'
          && operation.kind !== 'project.restart')) {
        continue
      }
      latestOperations.set(operation.project_id, operation)
    }

    for (const [projectId, operation] of latestOperations) {
      if (projectLifecycleRequestProjectIds.value[projectId]) {
        continue
      }
      if (!projectsById.has(projectId)) {
        forgetProjectLifecycleIntent(projectId)
        setProjectLifecycleProblem(projectId, null)
        continue
      }
      if (!isActiveOperation(operation)) {
        continue
      }
      trackProjectLifecycleIntent(projectId, {
        action: lifecycleActionForOperation(operation),
        id: operation.intent_id,
      })
      setProjectLifecycleProblem(projectId, null)
    }

    for (const [projectId, tracked] of projectLifecycleIntents) {
      if (projectLifecycleRequestProjectIds.value[projectId]) {
        continue
      }
      const project = projectsById.get(projectId)
      if (!project) {
        forgetProjectLifecycleIntent(projectId)
        setProjectLifecycleProblem(projectId, null)
        continue
      }
      const trackedOperation = nextSnapshot.operations.find((operation) => operation.project_id === projectId
        && operation.intent_id === tracked.id)
      const active = trackedOperation ? isActiveOperation(trackedOperation) : false
      const matchesTrackedAction = trackedOperation?.kind === `project.${tracked.action}`
      const reachedTarget = tracked.action === 'start'
        ? project.state === 'ready' || project.state === 'degraded'
        : tracked.action === 'stop' && project.state === 'stopped'
      if (!active && (matchesTrackedAction || reachedTarget)) {
        forgetProjectLifecycleIntent(projectId)
        setProjectLifecycleProblem(
          projectId,
          matchesTrackedAction && trackedOperation ? projectLifecycleTerminalProblem(trackedOperation, tracked.action) : null,
          trackedOperation?.intent_id,
        )
      }
    }

    for (const [projectId, operation] of latestOperations) {
      const project = projectsById.get(projectId)
      if (!project) {
        continue
      }
      if (projectLifecycleIntents.has(projectId)) {
        continue
      }
      const trackedProblem = projectLifecycleProblemIntents.get(projectId)
      if (!establishesBaseline
        && trackedProblem
        && !trackedProblem.observed
        && trackedProblem.id !== operation.intent_id
        && (trackedProblem.revision === undefined || nextSnapshot.sequence < trackedProblem.revision)) {
        continue
      }
      const problem = projectLifecycleTerminalProblem(
        operation,
        lifecycleActionForOperation(operation),
      )
      setProjectLifecycleProblem(
        projectId,
        problem?.code === 'project.recovery.ambiguous_launch' && project.state !== 'unavailable'
          ? null
          : problem,
        operation.intent_id,
      )
    }

    if (establishesBaseline) {
      for (const projectId of Object.keys(projectLifecycleErrors.value)) {
        if (!latestOperations.has(projectId) && !projectLifecycleIntents.has(projectId)) {
          setProjectLifecycleProblem(projectId, null)
        }
      }
    }
  }

  function setProjectLifecycleProblem(
    projectId: string,
    problem: Problem | null,
    intentId?: string,
    observed = true,
    revision?: number,
  ) {
    const errors = { ...projectLifecycleErrors.value }
    const codes = { ...projectLifecycleProblemCodes.value }
    if (problem) {
      errors[projectId] = problem.message
      codes[projectId] = problem.code
      if (intentId) {
        projectLifecycleProblemIntents.set(projectId, { id: intentId, observed, revision })
      }
      else {
        projectLifecycleProblemIntents.delete(projectId)
      }
    }
    else {
      delete errors[projectId]
      delete codes[projectId]
      projectLifecycleProblemIntents.delete(projectId)
    }
    projectLifecycleErrors.value = errors
    projectLifecycleProblemCodes.value = codes
  }

  function setProjectLifecycleError(projectId: string, message: string) {
    setProjectLifecycleProblem(projectId, {
      code: 'project.request_failed',
      message,
      retryable: true,
    })
  }

  async function changeProjectLifecycle(
    projectId: string,
    action: ProjectLifecycleAction,
  ): Promise<ProjectLifecycleOperation | null> {
    if (settingUpNetwork.value) {
      setProjectLifecycleError(projectId, 'Wait for network setup to finish, then try the project action again.')
      return null
    }
    // A caller may retry an uncertain first request before Harbor has a baseline snapshot; once a project snapshot exists,
    // lifecycle actions must wait for an authenticated, current daemon view instead of acting on retained state.
    if (snapshot.value && connectionState.value !== 'connected') {
      setProjectLifecycleError(projectId, 'Harbor is disconnected. Reconnect, then try again.')
      return null
    }
    if (snapshot.value && snapshotStale.value) {
      setProjectLifecycleError(projectId, 'Harbor is still reconciling local state. Wait for a fresh snapshot, then try again.')
      return null
    }
    if (projectLifecycleRequestProjectIds.value[projectId]) {
      setProjectLifecycleError(projectId, 'Wait for the current project action to finish, then try again.')
      return null
    }

    const active = activeProjectLifecycle(projectId)
    if (active && active.kind !== `project.${action}`) {
      setProjectLifecycleError(projectId, `Harbor is already ${lifecycleProgressLabel(active.kind)} this project.`)
      return null
    }
    const remembered = projectLifecycleIntents.get(projectId)
    if (remembered && remembered.action !== action) {
      setProjectLifecycleError(projectId, `Harbor is already ${lifecycleProgressLabel(`project.${remembered.action}`)} this project.`)
      return null
    }
    const intent = active
      ? { action, id: active.intent_id }
      : remembered?.action === action
        ? remembered
        : { action, id: newProjectLifecycleIntent(action) }
    trackProjectLifecycleIntent(projectId, intent)
    beginProjectLifecycleRequest(projectId)
    setProjectLifecycleProblem(projectId, null)

    try {
      const result = action === 'start'
        ? await harborBridge.startProject(projectId, intent.id)
        : action === 'stop'
          ? await harborBridge.stopProject(projectId, intent.id)
          : await harborBridge.restartProject(projectId, intent.id)
      if (result.operation.project_id !== projectId
        || result.operation.intent_id !== intent.id
        || result.operation.kind !== `project.${action}`) {
        throw new Error('Harbor returned lifecycle progress for another project action.')
      }
      if (isActiveOperation(result.operation)) {
        trackProjectLifecycleIntent(projectId, { action, id: result.operation.intent_id })
      }
      else {
        forgetProjectLifecycleIntent(projectId)
        setProjectLifecycleProblem(
          projectId,
          projectLifecycleTerminalProblem(result.operation, action),
          result.operation.intent_id,
          false,
          result.revision,
        )
      }
      return result
    }
    catch (cause) {
      setProjectLifecycleError(projectId, cause instanceof Error
        ? cause.message
        : `Harbor could not ${action} the project.`)
      return null
    }
    finally {
      endProjectLifecycleRequest(projectId)
      await refresh()
    }
  }

  function startProject(projectId: string) {
    return changeProjectLifecycle(projectId, 'start')
  }

  function stopProject(projectId: string) {
    return changeProjectLifecycle(projectId, 'stop')
  }

  function restartProject(projectId: string) {
    return changeProjectLifecycle(projectId, 'restart')
  }

  function projectRuntimeRepairNotice(projectId: string) {
    return projectRuntimeRepairNotices.value[projectId]
  }

  function setProjectRuntimeRepairNotice(projectId: string, notice: ProjectRuntimeRepairNotice | null) {
    const notices = { ...projectRuntimeRepairNotices.value }
    if (notice) {
      notices[projectId] = notice
    }
    else {
      delete notices[projectId]
    }
    projectRuntimeRepairNotices.value = notices
  }

  function discardProjectRuntimeRepair() {
    projectRuntimeRepairGeneration += 1
    projectRuntimeRepairInspection.value = null
    projectRuntimeRepairProjectId.value = null
    projectRuntimeRepairAction.value = null
  }

  function projectRuntimeRepairBlocker(projectId: string): ProjectRuntimeRepairNotice | null {
    if (!projectById(projectId)) {
      return {
        state: 'blocked',
        title: 'Stale runtime inspection unavailable',
        message: 'This project is no longer present in the current Harbor snapshot.',
      }
    }
    if (connectionState.value !== 'connected') {
      return {
        state: 'blocked',
        title: 'Stale runtime inspection unavailable',
        message: 'Reconnect to Harbor before inspecting or stopping a stale runtime.',
      }
    }
    if (snapshotStale.value) {
      return {
        state: 'blocked',
        title: 'Stale runtime inspection unavailable',
        message: 'Wait for a fresh Harbor snapshot before inspecting or stopping a stale runtime.',
      }
    }
    if (projectRuntimeRepairBusy.value
      || settingUpNetwork.value
      || projectLifecycleBusy.value
      || removingProjectId.value !== null) {
      return {
        state: 'blocked',
        title: 'Another Harbor action is in progress',
        message: 'Wait for the current action to finish, then inspect the stale runtime again.',
      }
    }
    return null
  }

  async function inspectProjectRuntimeRepair(projectId: string): Promise<ProjectRuntimeRepairInspection | null> {
    discardProjectRuntimeRepair()
    const attempt = projectRuntimeRepairGeneration
    setProjectRuntimeRepairNotice(projectId, null)
    const blocker = projectRuntimeRepairBlocker(projectId)
    if (blocker) {
      setProjectRuntimeRepairNotice(projectId, blocker)
      return null
    }

    projectRuntimeRepairProjectId.value = projectId
    projectRuntimeRepairAction.value = 'inspect'
    try {
      const inspection = await harborBridge.inspectProjectRuntimeRepair(projectId)
      validateProjectRuntimeRepairInspection(projectId, inspection)
      if (attempt !== projectRuntimeRepairGeneration) {
        return null
      }

      switch (inspection.disposition) {
        case 'confirmable':
          projectRuntimeRepairInspection.value = inspection
          return inspection
        case 'not_actionable':
          setProjectRuntimeRepairNotice(projectId, projectRuntimeRepairDiagnostic(inspection.reason))
          return inspection
        case 'unsupported':
          setProjectRuntimeRepairNotice(projectId, {
            state: 'unsupported',
            title: 'Stale runtime inspection unavailable',
            message: 'Stale runtime inspection is currently available only on macOS.',
          })
          return inspection
      }
      throw new Error('Harbor returned an unsupported stale runtime inspection result.')
    }
    catch (cause) {
      if (attempt === projectRuntimeRepairGeneration) {
        setProjectRuntimeRepairNotice(projectId, {
          state: 'failed',
          title: 'Recovery check failed',
          message: 'Harbor could not verify the previous runtime. Try again.',
        })
      }
      return null
    }
    finally {
      if (attempt === projectRuntimeRepairGeneration) {
        projectRuntimeRepairProjectId.value = null
        projectRuntimeRepairAction.value = null
      }
    }
  }

  function stageProjectRuntimeRepairConfirmation(confirmation: ProjectRuntimeRepairConfirmation) {
    const current = snapshot.value
    if (!current || confirmation.revision < current.sequence) {
      return
    }
    const projectIndex = current.projects.findIndex((project) => project.id === confirmation.project.id)
    if (projectIndex < 0) {
      return
    }

    const projects = [...current.projects]
    projects[projectIndex] = confirmation.project
    snapshot.value = {
      ...current,
      sequence: confirmation.revision,
      projects,
    }
    snapshotStale.value = true
    setProjectLifecycleProblem(confirmation.project.id, null)
  }

  async function confirmProjectRuntimeRepair(projectId: string): Promise<ProjectRuntimeRepairConfirmation | null> {
    const inspection = projectRuntimeRepairInspection.value
    projectRuntimeRepairInspection.value = null
    setProjectRuntimeRepairNotice(projectId, null)

    if (projectRuntimeRepairBusy.value) {
      setProjectRuntimeRepairNotice(projectId, {
        state: 'blocked',
        title: 'Another Harbor action is in progress',
        message: 'Wait for the current action to finish, then inspect the stale runtime again.',
      })
      await refresh()
      return null
    }

    projectRuntimeRepairGeneration += 1
    const attempt = projectRuntimeRepairGeneration

    if (!inspection || inspection.project_id !== projectId || inspection.disposition !== 'confirmable') {
      setProjectRuntimeRepairNotice(projectId, {
        state: 'expired',
        title: 'Fresh inspection required',
        message: 'Inspect the stale runtime again before confirming this action.',
      })
      await refresh()
      return null
    }

    const blocker = projectRuntimeRepairBlocker(projectId)
    if (blocker) {
      setProjectRuntimeRepairNotice(projectId, blocker)
      await refresh()
      return null
    }

    projectRuntimeRepairProjectId.value = projectId
    projectRuntimeRepairAction.value = 'confirm'
    try {
      if (Date.parse(inspection.confirmable.expires_at) <= Date.now()) {
        setProjectRuntimeRepairNotice(projectId, {
          state: 'expired',
          title: 'Stale runtime inspection expired',
          message: 'Inspect the stale runtime again before confirming this action.',
        })
        return null
      }

      const confirmation = await harborBridge.confirmProjectRuntimeRepair(
        projectId,
        inspection.confirmable.inspection_id,
        inspection.confirmable.candidate_fingerprint,
      )
      validateProjectRuntimeRepairConfirmation(projectId, confirmation)
      if (attempt !== projectRuntimeRepairGeneration) {
        return null
      }
      stageProjectRuntimeRepairConfirmation(confirmation)
      setProjectRuntimeRepairNotice(projectId, {
        state: 'succeeded',
        title: 'Stale runtime stopped',
        message: confirmation.project.state === 'stopped'
          ? 'Harbor stopped the process you confirmed and left the project stopped.'
          : 'Harbor stopped the process you confirmed and left the project route-free for a later retry.',
      })
      return confirmation
    }
    catch (cause) {
      if (attempt === projectRuntimeRepairGeneration) {
        setProjectRuntimeRepairNotice(projectId, {
          state: 'failed',
          title: 'Stale runtime repair failed',
          message: cause instanceof Error ? cause.message : 'Harbor could not stop the inspected stale runtime.',
        })
      }
      return null
    }
    finally {
      await refresh()
      if (attempt === projectRuntimeRepairGeneration) {
        projectRuntimeRepairProjectId.value = null
        projectRuntimeRepairAction.value = null
      }
    }
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

  async function approveProjectRemoval(projectId: string): Promise<ProjectUnregistration | null> {
    if (removingProjectId.value || projectRemovalApprovalProjectId.value) {
      setProjectRemovalNotice(projectId, {
        state: 'busy',
        title: 'Another project removal is in progress',
        message: 'Wait for the current removal request to finish, then try again.',
      })
      return null
    }

    const pending = projectRemovalNotice(projectId)
    if (pending?.state !== 'requires_approval') {
      setProjectRemovalNotice(projectId, {
        state: 'incomplete',
        title: 'Fresh removal approval required',
        message: 'Harbor no longer has a pending administrator approval for this project. Start the removal again.',
      })
      await refresh()
      return null
    }

    const tracked = projectRemovalIntent(projectId)
    projectRemovalApprovalProjectId.value = projectId
    setProjectRemovalNotice(projectId, null)

    try {
      const result = await harborBridge.approveProjectRemoval(projectId, tracked.id)
      validateProjectRemovalResult(projectId, tracked.id, result)
      const operation = result.operation
      switch (operation.state) {
        case 'succeeded':
          projectRemovalIntents.delete(projectId)
          setProjectRemovalNotice(projectId, null)
          stageTerminalProjectRemoval(result)
          break
        case 'requires_approval':
        case 'queued':
        case 'running':
          projectRemovalIntents.set(projectId, {
            id: operation.intent_id,
            revision: result.revision,
            state: 'active',
          })
          setProjectRemovalNotice(projectId, activeProjectRemovalNotice(operation))
          break
        case 'failed':
        case 'cancelled':
          projectRemovalIntents.delete(projectId)
          stageTerminalProjectRemoval(result)
          setProjectRemovalNotice(projectId, {
            state: operation.state,
            title: operation.state === 'failed' ? 'Project removal approval failed' : 'Project removal approval cancelled',
            message: operation.problem?.message ?? 'Harbor did not complete the approved project removal.',
          })
          break
      }
      return result
    }
    catch (cause) {
      projectRemovalIntents.set(projectId, { ...tracked, state: 'active' })
      setProjectRemovalNotice(projectId, {
        state: 'requires_approval',
        title: 'Administrator approval still required',
        message: cause instanceof Error
          ? cause.message
          : 'Harbor could not complete administrator approval. You can safely try again.',
      })
      return null
    }
    finally {
      projectRemovalApprovalProjectId.value = null
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

  async function readProjectActivity(projectId: string, sessionId: string, cursor: number): Promise<ProjectActivity> {
    return harborBridge.getProjectActivity(projectId, sessionId, cursor)
  }

  async function waitProjectActivity(
    projectId: string,
    sessionId: string,
    cursor: number,
    waitMilliseconds: number,
  ): Promise<ProjectActivity> {
    return harborBridge.waitProjectActivity(projectId, sessionId, cursor, waitMilliseconds)
  }

  async function readServiceLogs(
    projectId: string,
    sessionId: string,
    serviceId: string,
    cursor: number,
  ): Promise<ServiceLogs> {
    return harborBridge.getServiceLogs(projectId, sessionId, serviceId, cursor)
  }

  async function waitServiceLogs(
    projectId: string,
    sessionId: string,
    serviceId: string,
    cursor: number,
    waitMilliseconds: number,
  ): Promise<ServiceLogs> {
    return harborBridge.waitServiceLogs(projectId, sessionId, serviceId, cursor, waitMilliseconds)
  }

  return {
    snapshot,
    daemonStatus,
    connectionState,
    connectionMessage,
    snapshotStale,
    refreshing,
    addingProject,
    settingUpNetwork,
    removingOldNetworking,
    networkSetupResult,
    networkSetupError,
    oldNetworkingRemovalError,
    removingProjectId,
    projectRemovalApprovalProjectId,
    projectRemovalApprovalBusy,
    projectLifecycleBusy,
    projectLifecycleBusyFor,
    projectLifecycleBlockedFor,
    projectLifecycleErrors,
    projectLifecycleProblemCodes,
    projectRuntimeRepairInspection,
    projectRuntimeRepairProjectId,
    projectRuntimeRepairAction,
    projectRuntimeRepairBusy,
    projectRuntimeRepairNotices,
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
    networkSetupOnboarding,
    oldNetworkingRemovalAvailable,
    oldNetworkingRemovalBlocked,
    attentionCount,
    runningCount,
    refresh,
    initialize,
    dispose,
    projectById,
    serviceById,
    addProject,
    setupNetwork,
    removeOldNetworking,
    projectRemovalNotice,
    removeProject,
    approveProjectRemoval,
    activeProjectLifecycle,
    startProject,
    stopProject,
    restartProject,
    projectRuntimeRepairNotice,
    inspectProjectRuntimeRepair,
    confirmProjectRuntimeRepair,
    discardProjectRuntimeRepair,
    readProjectActivity,
    waitProjectActivity,
    readServiceLogs,
    waitServiceLogs,
    openResource,
  }
})

function newProjectRemovalIntent(): string {
  const bytes = crypto.getRandomValues(new Uint8Array(16))
  const opaque = Array.from(bytes, (value) => value.toString(16).padStart(2, '0')).join('')
  return `desktop-project-remove-${opaque}`
}

function newProjectLifecycleIntent(action: ProjectLifecycleAction): string {
  const bytes = crypto.getRandomValues(new Uint8Array(16))
  const opaque = Array.from(bytes, (value) => value.toString(16).padStart(2, '0')).join('')
  return `desktop-project-${action}-${opaque}`
}

function lifecycleActionForOperation(operation: Operation): ProjectLifecycleAction {
  switch (operation.kind) {
    case 'project.start':
      return 'start'
    case 'project.stop':
      return 'stop'
    case 'project.restart':
      return 'restart'
    default:
      throw new Error(`Unsupported project lifecycle operation: ${operation.kind}`)
  }
}

function lifecycleProgressLabel(kind: string): string {
  switch (kind) {
    case 'project.start':
      return 'starting'
    case 'project.stop':
      return 'stopping'
    case 'project.restart':
      return 'restarting'
    default:
      return 'changing'
  }
}

function isActiveOperation(operation: Operation): boolean {
  return operation.state === 'queued'
    || operation.state === 'running'
    || operation.state === 'requires_approval'
}

function projectLifecycleTerminalProblem(operation: Operation, action: ProjectLifecycleAction): Problem | null {
  if (operation.state === 'failed') {
    return operation.problem ?? {
      code: `project.${action}_failed`,
      message: `Harbor could not ${action} the project.`,
      retryable: true,
    }
  }
  if (operation.state === 'cancelled') {
    return operation.problem ?? {
      code: `project.${action}_cancelled`,
      message: `Project ${action} was cancelled.`,
      retryable: true,
    }
  }
  return null
}

function validateProjectRuntimeRepairInspection(projectId: string, inspection: ProjectRuntimeRepairInspection) {
  if (inspection.project_id !== projectId) {
    throw new Error('Harbor returned a stale runtime inspection for another project.')
  }
  switch (inspection.disposition) {
    case 'confirmable': {
      const candidate = inspection.confirmable?.candidate
      if (!candidate
        || candidate.command !== 'forj dev'
        || !candidate.checkout
        || !candidate.endpoint
        || !Number.isSafeInteger(candidate.root_pid)
        || candidate.root_pid <= 0
        || !Number.isSafeInteger(candidate.member_count)
        || candidate.member_count <= 0
        || !inspection.confirmable.inspection_id
        || !inspection.confirmable.candidate_fingerprint
        || !Number.isFinite(Date.parse(inspection.confirmable.expires_at))) {
        throw new Error('Harbor returned an incomplete stale runtime inspection.')
      }
      return
    }
    case 'not_actionable':
      projectRuntimeRepairDiagnostic(inspection.reason)
      return
    case 'unsupported':
      return
    default:
      throw new Error('Harbor returned an unsupported stale runtime inspection result.')
  }
}

function validateProjectRuntimeRepairConfirmation(projectId: string, confirmation: ProjectRuntimeRepairConfirmation) {
  if (confirmation.project.id !== projectId) {
    throw new Error('Harbor returned a stale runtime confirmation for another project.')
  }
  if (!['stopped', 'failed', 'unavailable'].includes(confirmation.project.state)
    || confirmation.project.resources.length !== 0
    || confirmation.project.apps.some((app) => app.state !== 'stopped' || app.active)
    || confirmation.project.services.some((service) => service.state !== 'stopped')
    || !Number.isSafeInteger(confirmation.revision)
    || confirmation.revision <= 0) {
    throw new Error('Harbor returned an incomplete stale runtime confirmation; the project must remain route-free and retryable.')
  }
}

function projectRuntimeRepairDiagnostic(reason: ProjectRuntimeRepairNotActionableReason): ProjectRuntimeRepairNotice {
  switch (reason) {
    case 'none':
      return {
        state: 'not_actionable',
        title: 'No stale runtime found',
        message: 'Harbor did not find a process listening at this project’s endpoint. No process was stopped.',
      }
    case 'ambiguous':
      return {
        state: 'not_actionable',
        title: 'Stale runtime is ambiguous',
        message: 'Harbor found more than one possible process scope and cannot safely choose one. No process was stopped.',
      }
    case 'foreign':
      return {
        state: 'not_actionable',
        title: 'Stale runtime belongs to another user',
        message: 'The process at this project’s endpoint belongs to another user. Harbor will not stop it.',
      }
    case 'unreadable':
      return {
        state: 'not_actionable',
        title: 'Stale runtime details are incomplete',
        message: 'Harbor could not read all required process details. No process was stopped.',
      }
    default:
      throw new Error('Harbor returned an unsupported stale runtime diagnostic.')
  }
}

function validateProjectRemovalResult(projectId: string, intentId: string, result: ProjectUnregistration) {
  if (result.operation.kind !== 'project.unregister'
    || result.operation.project_id !== projectId
    || result.operation.intent_id !== intentId
    || !Number.isSafeInteger(result.revision)
    || result.revision <= 0) {
    throw new Error('Harbor returned project removal progress for another project or intent.')
  }
}

function activeProjectRemovalNotice(operation: Operation): ProjectRemovalNotice {
  if (operation.state === 'requires_approval') {
    return {
      state: operation.state,
      title: 'Administrator approval required',
      message: 'Harbor paused removal until it can release this project’s local networking. Approve the one-time administrator action to continue.',
    }
  }
  return {
    state: operation.state,
    title: 'Project removal in progress',
    message: 'Harbor is releasing project-owned resources before removing the registration.',
  }
}
