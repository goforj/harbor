import { computed, ref } from 'vue'
import { defineStore } from 'pinia'
import { harborBridge } from '@/bridge'
import type { HarborSnapshot } from '@/domain/harbor'

export const useHarborStore = defineStore('harbor', () => {
  const snapshot = ref<HarborSnapshot | null>(null)
  const loading = ref(false)
  const error = ref<string | null>(null)
  let unsubscribe: (() => void) | null = null

  const projects = computed(() => snapshot.value?.projects ?? [])
  const services = computed(() => snapshot.value?.services ?? [])
  const system = computed(() => snapshot.value?.system ?? [])
  const attentionCount = computed(() => projects.value.filter((project) => project.status === 'failed' || project.status === 'degraded').length)
  const runningCount = computed(() => projects.value.filter((project) => project.status === 'ready' || project.status === 'working' || project.status === 'degraded').length)

  function acceptSnapshot(nextSnapshot: HarborSnapshot) {
    if (nextSnapshot.sequence < (snapshot.value?.sequence ?? 0)) {
      return
    }

    snapshot.value = nextSnapshot
    error.value = null
  }

  async function refresh() {
    const sequenceBeforeRefresh = snapshot.value?.sequence ?? 0
    loading.value = true
    error.value = null
    try {
      acceptSnapshot(await harborBridge.getSnapshot())
    } catch (cause) {
      if ((snapshot.value?.sequence ?? 0) <= sequenceBeforeRefresh) {
        error.value = cause instanceof Error ? cause.message : 'Unable to load Harbor state.'
      }
    } finally {
      loading.value = false
    }
  }

  async function initialize() {
    unsubscribe?.()
    unsubscribe = harborBridge.subscribe((event) => {
      acceptSnapshot(event.snapshot)
    })
    await refresh()
  }

  function dispose() {
    unsubscribe?.()
    unsubscribe = null
  }

  function projectById(projectId: string) {
    return projects.value.find((project) => project.id === projectId)
  }

  function serviceById(serviceId: string) {
    return services.value.find((service) => service.id === serviceId)
  }

  async function openResource(resourceId: string) {
    await harborBridge.openResource(resourceId)
  }

  return {
    snapshot,
    loading,
    error,
    projects,
    services,
    system,
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
