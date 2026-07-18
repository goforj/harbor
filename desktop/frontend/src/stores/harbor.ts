import { computed, ref } from 'vue'
import { defineStore } from 'pinia'
import { harborBridge } from '@/bridge'
import type {
  ConnectionEvent,
  ConnectionState,
  DaemonStatus,
  HarborSnapshot,
  ProjectResource,
  ProjectService,
} from '@/domain/harbor'

export const useHarborStore = defineStore('harbor', () => {
  const snapshot = ref<HarborSnapshot | null>(null)
  const daemonStatus = ref<DaemonStatus | null>(null)
  const connectionState = ref<ConnectionState>('connecting')
  const snapshotStale = ref(true)
  const refreshing = ref(false)
  const error = ref<string | null>(null)
  const actionError = ref<string | null>(null)
  let connectionEpoch = 0
  let statusRequest = 0
  let refreshRequest = 0
  let acceptedSnapshots = 0
  let snapshotNeedsBaseline = true
  let unsubscribeSnapshot: (() => void) | null = null
  let unsubscribeConnection: (() => void) | null = null

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

    snapshot.value = nextSnapshot
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
    refreshing.value = true
    const [statusResult, snapshotResult] = await Promise.allSettled([
      harborBridge.getStatus(),
      harborBridge.getSnapshot(),
    ])

    if (epoch !== connectionEpoch) {
      if (refreshRequestForCall === refreshRequest) {
        refreshing.value = false
      }
      return
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
    if (refreshRequestForCall === refreshRequest) {
      refreshing.value = false
    }
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
    loading,
    error,
    actionError,
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
    openResource,
  }
})
