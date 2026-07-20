<script setup lang="ts">
import { computed, nextTick, onBeforeUnmount, ref, watch } from 'vue'
import { RouterLink, useRoute, useRouter } from 'vue-router'
import {
  ArrowLeft,
  ArrowUpRight,
  Check,
  Clipboard,
  ExternalLink,
  LoaderCircle,
  Network,
  Play,
  Search,
  Server,
  Square,
  SquareTerminal,
  Trash2,
  TriangleAlert,
} from '@lucide/vue'
import StatusBadge from '@/components/harbor/StatusBadge.vue'
import ServiceOwnership from '@/components/harbor/ServiceOwnership.vue'
import TerminalOutput from '@/components/harbor/TerminalOutput.vue'
import { copyText } from '@/bridge/clipboard'
import { Alert, AlertDescription, AlertTitle } from '@/components/ui/alert'
import {
  AlertDialog,
  AlertDialogAction,
  AlertDialogCancel,
  AlertDialogContent,
  AlertDialogDescription,
  AlertDialogFooter,
  AlertDialogHeader,
  AlertDialogTitle,
  AlertDialogTrigger,
} from '@/components/ui/alert-dialog'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Card, CardContent, CardHeader, CardTitle } from '@/components/ui/card'
import { Empty, EmptyContent, EmptyDescription, EmptyHeader, EmptyTitle } from '@/components/ui/empty'
import { useProjectActivity } from '@/composables/useProjectActivity'
import { countReadyServices } from '@/lib/servicePresentation'
import { useHarborStore } from '@/stores/harbor'

const route = useRoute()
const router = useRouter()
const store = useHarborStore()
const copiedPath = ref(false)
const removeOpen = ref(false)
const runtimeRepairOpen = ref(false)
const runtimeRepairNow = ref(Date.now())
let runtimeRepairExpiryTimer: number | undefined
const developmentOutputViewport = ref<HTMLElement | null>(null)
const followDevelopmentOutput = ref(true)
const projectId = computed(() => String(route.params.projectId ?? ''))
const project = computed(() => store.projectById(projectId.value))
const readyServiceCount = computed(() => countReadyServices(project.value?.services ?? []))
const projectActivitySupported = computed(() => store.daemonStatus?.capabilities.includes('control.project-activity.v1') === true)
const projectActivityWaitSupported = computed(() => store.daemonStatus?.capabilities.includes('control.project-activity-wait.v1') === true)
const daemonConnected = computed(() => store.connectionState === 'connected')
const snapshotSequence = computed(() => store.snapshot?.sequence)
const {
  activity: projectActivity,
  output: developmentOutput,
  outputResetKey: developmentOutputResetKey,
  error: developmentOutputError,
  truncated: developmentOutputTruncated,
} = useProjectActivity({
  projectId,
  supported: projectActivitySupported,
  waitSupported: projectActivityWaitSupported,
  connected: daemonConnected,
  snapshotSequence,
  read: (selectedProjectId, sessionId, cursor) => store.readProjectActivity(selectedProjectId, sessionId, cursor),
  wait: (selectedProjectId, sessionId, cursor, waitMilliseconds) => store.waitProjectActivity(selectedProjectId, sessionId, cursor, waitMilliseconds),
})
const projectActivitySession = computed(() => projectActivity.value?.session)
const showDevelopmentOutput = computed(() => projectActivitySupported.value && (
  projectActivitySession.value != null
  || developmentOutput.value !== ''
  || developmentOutputError.value != null
  || project.value?.state === 'starting'
  || project.value?.state === 'ready'
  || project.value?.state === 'rebuilding'
  || project.value?.state === 'degraded'
  || project.value?.state === 'stopping'
))
const developmentOutputState = computed(() => {
  if (projectActivitySession.value) return projectActivitySession.value.state.replace('_', ' ')
  if (developmentOutput.value) return 'session ended'
  return project.value?.state === 'starting' ? 'waiting for session' : 'no active session'
})
const currentProjectOperation = computed(() => {
  for (let index = store.operations.length - 1; index >= 0; index -= 1) {
    const operation = store.operations[index]
    if (operation?.project_id === projectId.value) return operation
  }
  return undefined
})
const primaryResource = computed(() => project.value?.resources.find((resource) => resource.kind === 'application'))
const removalNotice = computed(() => store.projectRemovalNotice(projectId.value))
const activeLifecycle = computed(() => store.activeProjectLifecycle(projectId.value))
const lifecycleError = computed(() => store.projectLifecycleErrors[projectId.value])
const lifecycleProblemCode = computed(() => store.projectLifecycleProblemCodes[projectId.value])
const needsNetworkSetup = computed(() => lifecycleProblemCode.value === 'project.network.setup_required')
const recoveryRequired = computed(() => lifecycleProblemCode.value === 'project.recovery.ambiguous_launch')
const runtimeRepairNotice = computed(() => store.projectRuntimeRepairNotice(projectId.value))
const runtimeRepairInspection = computed(() => {
  const inspection = store.projectRuntimeRepairInspection
  return inspection?.project_id === projectId.value && inspection.disposition === 'confirmable'
    ? inspection
    : undefined
})
const runtimeRepairCandidate = computed(() => runtimeRepairInspection.value?.confirmable.candidate)
const runtimeRepairExpired = computed(() => {
  const now = runtimeRepairNow.value
  const expiresAt = runtimeRepairInspection.value?.confirmable.expires_at
  return expiresAt ? Date.parse(expiresAt) <= now : false
})
const lifecycleInFlight = computed(() => store.projectLifecycleProjectId === projectId.value)
const starting = computed(() => project.value?.state === 'starting' || activeLifecycle.value?.kind === 'project.start')
const stopping = computed(() => project.value?.state === 'stopping' || activeLifecycle.value?.kind === 'project.stop')
const lifecycleAction = computed(() => project.value?.state === 'stopped'
  || project.value?.state === 'failed'
  || project.value?.state === 'unavailable'
  ? 'start'
  : 'stop')
const lifecycleLabel = computed(() => {
  if (recoveryRequired.value) return 'Recovery required'
  if (starting.value) return 'Starting…'
  if (stopping.value) return 'Stopping…'
  return lifecycleAction.value === 'start' ? 'Start project' : 'Stop project'
})
const lifecycleDisabled = computed(() => store.snapshotStale
  || store.settingUpNetwork
  || store.projectLifecycleBusy
  || store.projectRuntimeRepairBusy
  || starting.value
  || stopping.value
  || recoveryRequired.value
  || removalPending.value)
const networkSetupDisabled = computed(() => !needsNetworkSetup.value
  || project.value?.id !== projectId.value
  || store.settingUpNetwork
  || store.projectLifecycleBusy
  || store.projectRuntimeRepairBusy
  || store.snapshotStale
  || store.connectionState !== 'connected')
const removing = computed(() => store.removingProjectId === projectId.value)
const removalPending = computed(() => removalNotice.value?.state === 'queued'
  || removalNotice.value?.state === 'running'
  || removalNotice.value?.state === 'requires_approval')
const removalDisabled = computed(() => store.removingProjectId !== null
  || store.projectLifecycleProjectId !== null
  || store.projectRuntimeRepairBusy
  || activeLifecycle.value != null
  || recoveryRequired.value
  || removalPending.value)
const removalLabel = computed(() => {
  if (removing.value) return 'Removing…'
  if (store.removingProjectId) return 'Another removal is in progress'
  if (removalNotice.value?.state === 'requires_approval') return 'Awaiting approval'
  if (removalPending.value) return 'Removal in progress'
  return 'Remove project'
})
const runtimeRepairInspecting = computed(() => store.projectRuntimeRepairProjectId === projectId.value
  && store.projectRuntimeRepairAction === 'inspect')
const runtimeRepairInspectionDisabled = computed(() => !recoveryRequired.value
  || store.connectionState !== 'connected'
  || store.snapshotStale
  || store.settingUpNetwork
  || store.projectLifecycleBusy
  || store.removingProjectId !== null
  || store.projectRuntimeRepairBusy)
const runtimeRepairConfirmationDisabled = computed(() => runtimeRepairCandidate.value == null
  || runtimeRepairExpired.value
  || store.connectionState !== 'connected'
  || store.snapshotStale
  || store.settingUpNetwork
  || store.projectLifecycleBusy
  || store.removingProjectId !== null
  || store.projectRuntimeRepairBusy)
const updatedAt = computed(() => project.value
  ? new Intl.DateTimeFormat(undefined, { dateStyle: 'medium', timeStyle: 'medium' }).format(new Date(project.value.updated_at))
  : '')

watch([projectId, project], ([nextProjectId, nextProject], [previousProjectId, previousProject]) => {
  if (nextProjectId !== previousProjectId) {
    followDevelopmentOutput.value = true
    runtimeRepairOpen.value = false
    if (store.projectRuntimeRepairAction !== 'confirm') store.discardProjectRuntimeRepair()
  }
  if (nextProjectId && nextProjectId === previousProjectId && previousProject && !nextProject) {
    void router.replace('/projects')
  }
})

watch(() => runtimeRepairInspection.value?.confirmable.expires_at, (expiresAt) => {
  if (runtimeRepairExpiryTimer !== undefined) window.clearTimeout(runtimeRepairExpiryTimer)
  runtimeRepairNow.value = Date.now()
  if (expiresAt) scheduleRuntimeRepairExpiry(expiresAt)
}, { immediate: true })

watch(runtimeRepairInspection, (inspection) => {
  if (!inspection) runtimeRepairOpen.value = false
})

onBeforeUnmount(() => {
  if (runtimeRepairExpiryTimer !== undefined) window.clearTimeout(runtimeRepairExpiryTimer)
  if (store.projectRuntimeRepairAction !== 'confirm') store.discardProjectRuntimeRepair()
})

async function scrollDevelopmentOutput() {
  if (!followDevelopmentOutput.value) return
  await nextTick()
  const viewport = developmentOutputViewport.value
  if (viewport) viewport.scrollTop = viewport.scrollHeight
}

// updateDevelopmentOutputFollow pauses automatic tailing while the user inspects earlier output.
function updateDevelopmentOutputFollow() {
  const viewport = developmentOutputViewport.value
  if (!viewport) return
  followDevelopmentOutput.value = viewport.scrollHeight - viewport.scrollTop - viewport.clientHeight <= 32
}

async function copyPath() {
  if (!project.value) return
  await copyText(project.value.path)
  copiedPath.value = true
  window.setTimeout(() => { copiedPath.value = false }, 1600)
}

async function openResource(resourceId: string) {
  if (project.value) {
    await store.openResource(project.value.id, resourceId)
  }
}

async function removeProject() {
  if (!project.value) return
  const result = await store.removeProject(project.value.id)
  if (result?.operation.state === 'succeeded') {
    await router.replace('/projects')
  }
}

async function changeProjectLifecycle() {
  if (!project.value) return
  if (lifecycleAction.value === 'start') {
    await store.startProject(project.value.id)
    return
  }
  await store.stopProject(project.value.id)
}

async function setupNetworkAndStartProject() {
  const requestedProjectId = projectId.value
  if (networkSetupDisabled.value || project.value?.id !== requestedProjectId) return
  const result = await store.setupNetwork()
  if (!result
    || projectId.value !== requestedProjectId
    || store.projectById(requestedProjectId)?.id !== requestedProjectId
    || store.snapshotStale
    || store.connectionState !== 'connected'
    || store.projectLifecycleBusy) return
  await store.startProject(requestedProjectId)
}

async function inspectStaleRuntime() {
  const requestedProjectId = projectId.value
  if (runtimeRepairInspectionDisabled.value) return
  const inspection = await store.inspectProjectRuntimeRepair(requestedProjectId)
  if (projectId.value === requestedProjectId && inspection?.disposition === 'confirmable') {
    runtimeRepairOpen.value = true
  }
}

async function confirmStaleRuntime() {
  if (runtimeRepairConfirmationDisabled.value) return
  await store.confirmProjectRuntimeRepair(projectId.value)
}

function updateRuntimeRepairOpen(open: boolean) {
  runtimeRepairOpen.value = open
  if (open) return

  queueMicrotask(() => {
    if (!runtimeRepairOpen.value && store.projectRuntimeRepairAction !== 'confirm') {
      store.discardProjectRuntimeRepair()
    }
  })
}

function scheduleRuntimeRepairExpiry(expiresAt: string) {
  const remaining = Date.parse(expiresAt) - Date.now()
  if (remaining <= 0) {
    runtimeRepairNow.value = Date.now()
    return
  }
  runtimeRepairExpiryTimer = window.setTimeout(() => {
    runtimeRepairNow.value = Date.now()
    scheduleRuntimeRepairExpiry(expiresAt)
  }, Math.min(remaining, 2_147_483_647))
}
</script>

<template>
  <main class="h-full min-w-0 overflow-y-auto" :aria-labelledby="project ? 'project-title' : 'project-empty-title'">
    <template v-if="project">
      <header class="border-b px-5 py-4 lg:px-7">
        <div class="flex min-w-0 flex-wrap items-start justify-between gap-3">
          <div class="flex min-w-0 items-start gap-2">
            <Button variant="ghost" size="icon-sm" class="-ml-2 shrink-0 min-[1100px]:hidden" as-child>
              <RouterLink to="/projects" aria-label="Back to projects"><ArrowLeft class="size-4" /></RouterLink>
            </Button>
            <div class="min-w-0">
              <div class="flex min-w-0 items-center gap-2"><h1 id="project-title" class="truncate text-base font-semibold tracking-tight">{{ project.name }}</h1><StatusBadge :status="project.state" /></div>
              <p class="mt-1 truncate text-xs text-muted-foreground">{{ project.path }}</p>
            </div>
          </div>
          <div class="flex items-center gap-2">
            <Button
              :variant="lifecycleAction === 'start' ? 'default' : 'outline'"
              size="sm"
              :disabled="lifecycleDisabled"
              @click="changeProjectLifecycle"
            >
              <LoaderCircle v-if="lifecycleInFlight || starting || stopping" class="size-3.5 animate-spin" />
              <Play v-else-if="lifecycleAction === 'start'" class="size-3.5 fill-current" />
              <Square v-else class="size-3.5 fill-current" />
              {{ lifecycleLabel }}
            </Button>
            <AlertDialog v-model:open="removeOpen">
              <AlertDialogTrigger as-child>
                <Button variant="outline" size="sm" :disabled="removalDisabled">
                  <Trash2 class="size-3.5" />{{ removalLabel }}
                </Button>
              </AlertDialogTrigger>
              <AlertDialogContent>
                <AlertDialogHeader>
                  <AlertDialogTitle>Remove {{ project.name }}?</AlertDialogTitle>
                  <AlertDialogDescription>
                    Harbor will remove this project from its local registry and release any Harbor-owned networking. The project files at {{ project.path }} will stay on disk.
                  </AlertDialogDescription>
                </AlertDialogHeader>
                <AlertDialogFooter>
                  <AlertDialogCancel>Keep project</AlertDialogCancel>
                  <AlertDialogAction class="bg-destructive text-white hover:bg-destructive/90" :disabled="removalDisabled" @click="removeProject">
                    Remove project
                  </AlertDialogAction>
                </AlertDialogFooter>
              </AlertDialogContent>
            </AlertDialog>
            <AlertDialog :open="runtimeRepairOpen" @update:open="updateRuntimeRepairOpen">
              <AlertDialogContent v-if="runtimeRepairCandidate">
                <AlertDialogHeader>
                  <AlertDialogTitle>Stop this stale runtime?</AlertDialogTitle>
                  <AlertDialogDescription>
                    Harbor no longer has its launch receipt. This process is a candidate, not proven Harbor-owned. Continue only if you recognize it as this project.
                  </AlertDialogDescription>
                </AlertDialogHeader>
                <dl class="grid gap-3 rounded-md border bg-muted/30 p-4 text-sm sm:grid-cols-[7rem_minmax(0,1fr)]">
                  <dt class="text-muted-foreground">Command</dt><dd><code>{{ runtimeRepairCandidate.command }}</code></dd>
                  <dt class="text-muted-foreground">Checkout</dt><dd class="min-w-0 break-all"><code>{{ runtimeRepairCandidate.checkout }}</code></dd>
                  <dt class="text-muted-foreground">Endpoint</dt><dd><code>{{ runtimeRepairCandidate.endpoint }}</code></dd>
                  <dt class="text-muted-foreground">Root PID</dt><dd>{{ runtimeRepairCandidate.root_pid }}</dd>
                  <dt class="text-muted-foreground">Member count</dt><dd>{{ runtimeRepairCandidate.member_count }}</dd>
                </dl>
                <p v-if="runtimeRepairExpired" class="text-sm text-destructive">This inspection has expired. Close this dialog and inspect the stale runtime again.</p>
                <AlertDialogFooter>
                  <AlertDialogCancel>Cancel</AlertDialogCancel>
                  <AlertDialogAction
                    class="bg-destructive text-white hover:bg-destructive/90"
                    :disabled="runtimeRepairConfirmationDisabled"
                    @click="confirmStaleRuntime"
                  >
                    Stop this process and reset project
                  </AlertDialogAction>
                </AlertDialogFooter>
              </AlertDialogContent>
            </AlertDialog>
            <Button size="sm" :disabled="!primaryResource" @click="primaryResource && openResource(primaryResource.id)">Open resource<ExternalLink class="size-3.5" /></Button>
          </div>
        </div>

        <div class="mt-4 flex flex-wrap items-center gap-3 text-xs text-muted-foreground">
          <Badge variant="outline">Slug: {{ project.slug }}</Badge>
          <button type="button" class="inline-flex min-w-0 items-center gap-1.5 rounded-sm hover:text-foreground focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring" @click="copyPath">
            <Check v-if="copiedPath" class="size-3.5" /><Clipboard v-else class="size-3.5" />{{ copiedPath ? 'Path copied' : 'Copy path' }}
          </button>
          <span>Updated {{ updatedAt }}</span>
        </div>
      </header>

      <div class="space-y-5 p-5 lg:p-7">
        <Alert v-if="lifecycleError" variant="destructive">
          <TriangleAlert aria-hidden="true" />
          <AlertTitle>{{ recoveryRequired ? 'Project recovery required' : 'Project action failed' }}</AlertTitle>
          <AlertDescription class="space-y-3">
            <p>{{ lifecycleError }}</p>
            <p v-if="needsNetworkSetup && store.networkSetupError" class="text-destructive">{{ store.networkSetupError }}</p>
            <Button
              v-if="needsNetworkSetup"
              variant="outline"
              size="sm"
              :disabled="networkSetupDisabled"
              @click="setupNetworkAndStartProject"
            >
              <LoaderCircle v-if="store.settingUpNetwork" class="size-3.5 animate-spin" aria-hidden="true" />
              <Network v-else class="size-3.5" aria-hidden="true" />
              {{ store.settingUpNetwork ? 'Setting up networking…' : 'Set up networking and start' }}
            </Button>
            <Button
              v-if="recoveryRequired"
              variant="outline"
              size="sm"
              :disabled="runtimeRepairInspectionDisabled"
              @click="inspectStaleRuntime"
            >
              <LoaderCircle v-if="runtimeRepairInspecting" class="size-3.5 animate-spin" aria-hidden="true" />
              <Search v-else class="size-3.5" aria-hidden="true" />
              {{ runtimeRepairInspecting ? 'Inspecting stale runtime…' : 'Inspect stale runtime' }}
            </Button>
          </AlertDescription>
        </Alert>

        <Alert
          v-if="runtimeRepairNotice"
          :variant="runtimeRepairNotice.state === 'failed' ? 'destructive' : 'default'"
          :class="runtimeRepairNotice.state !== 'failed' && runtimeRepairNotice.state !== 'succeeded' ? 'border-amber-500/30 bg-amber-500/10 text-amber-900 dark:text-amber-200' : ''"
        >
          <Check v-if="runtimeRepairNotice.state === 'succeeded'" aria-hidden="true" />
          <TriangleAlert v-else aria-hidden="true" />
          <AlertTitle>{{ runtimeRepairNotice.title }}</AlertTitle>
          <AlertDescription>{{ runtimeRepairNotice.message }}</AlertDescription>
        </Alert>

        <Alert
          v-if="removalNotice"
          :variant="removalNotice.state === 'failed' || removalNotice.state === 'incomplete' || removalNotice.state === 'request_failed' ? 'destructive' : 'default'"
          :class="removalNotice.state === 'requires_approval' ? 'border-amber-500/30 bg-amber-500/10 text-amber-900 dark:text-amber-200' : ''"
        >
          <TriangleAlert aria-hidden="true" />
          <AlertTitle>{{ removalNotice.title }}</AlertTitle>
          <AlertDescription>{{ removalNotice.message }}</AlertDescription>
        </Alert>

        <section aria-label="Project summary" class="grid overflow-hidden rounded-lg border sm:grid-cols-4">
          <div class="p-4 sm:border-r"><p class="text-xs font-medium text-muted-foreground">Apps</p><p class="mt-1 text-xl font-semibold">{{ project.apps.length }}</p></div>
          <div class="border-t p-4 sm:border-t-0 sm:border-r"><p class="text-xs font-medium text-muted-foreground">Services</p><p class="mt-1 text-xl font-semibold">{{ readyServiceCount }} ready</p><p class="mt-0.5 text-xs text-muted-foreground">{{ project.services.length }} reported</p></div>
          <div class="border-t p-4 sm:border-t-0 sm:border-r"><p class="text-xs font-medium text-muted-foreground">Resources</p><p class="mt-1 text-xl font-semibold">{{ project.resources.length }}</p></div>
          <div class="border-t p-4 sm:border-t-0"><p class="text-xs font-medium text-muted-foreground">Activity</p><p class="mt-1 truncate text-sm font-semibold">{{ currentProjectOperation?.phase ?? 'Idle' }}</p></div>
        </section>

        <Card v-if="showDevelopmentOutput" class="gap-0 rounded-lg py-0 shadow-none">
          <CardHeader class="flex-row items-start justify-between gap-3 border-b px-4 py-3">
            <div class="min-w-0">
              <div class="flex items-center gap-2"><SquareTerminal class="size-4 text-muted-foreground" /><CardTitle class="text-sm">Development output</CardTitle></div>
              <p class="mt-1 text-xs text-muted-foreground">Live output from the current <code>forj dev</code> session</p>
            </div>
            <Badge variant="outline" class="shrink-0 capitalize">{{ developmentOutputState }}</Badge>
          </CardHeader>
          <CardContent class="p-0">
            <p v-if="developmentOutputError" class="border-b px-4 py-2 text-xs text-destructive">{{ developmentOutputError }}</p>
            <div
              ref="developmentOutputViewport"
              class="max-h-80 min-h-36 overflow-auto bg-zinc-950 px-4 py-3 font-mono text-xs leading-5 text-zinc-200 outline-none"
              tabindex="0"
              aria-label="Current project development output"
              @scroll="updateDevelopmentOutputFollow"
            >
              <p v-if="developmentOutputTruncated" class="mb-2 text-amber-300">Earlier output is no longer retained.</p>
              <TerminalOutput
                v-if="developmentOutput"
                :output="developmentOutput"
                :reset-key="developmentOutputResetKey"
                @rendered="scrollDevelopmentOutput"
              />
              <p v-else-if="projectActivitySession && !projectActivitySession.output.available" class="text-zinc-500">The current process is not available to stream output.</p>
              <p v-else class="text-zinc-500">Waiting for <code>forj dev</code> output…</p>
            </div>
          </CardContent>
        </Card>

        <div class="grid min-w-0 gap-5 xl:grid-cols-2">
          <Card class="gap-0 rounded-lg py-0 shadow-none">
            <CardHeader class="border-b px-4 py-3"><div class="flex items-center gap-2"><SquareTerminal class="size-4 text-muted-foreground" /><CardTitle class="text-sm">Apps</CardTitle></div></CardHeader>
            <CardContent class="p-0">
              <div v-if="project.apps.length" class="divide-y">
                <div v-for="app in project.apps" :key="app.id" class="flex items-center gap-3 px-4 py-3">
                  <StatusBadge :status="app.state" />
                  <div class="min-w-0 flex-1"><p class="text-sm font-medium">{{ app.name }}</p><p class="text-xs text-muted-foreground">{{ app.active ? 'Active' : 'Inactive' }} · {{ app.required ? 'Required' : 'Optional' }}</p></div>
                </div>
              </div>
              <p v-else class="px-4 py-8 text-center text-sm text-muted-foreground">No Apps are reported.</p>
            </CardContent>
          </Card>

          <Card class="gap-0 rounded-lg py-0 shadow-none">
            <CardHeader class="border-b px-4 py-3"><div class="flex items-center gap-2"><Server class="size-4 text-muted-foreground" /><CardTitle class="text-sm">Services</CardTitle></div></CardHeader>
            <CardContent class="p-0">
              <div v-if="project.services.length" class="divide-y">
                <RouterLink
                  v-for="service in project.services"
                  :key="service.id"
                  :to="`/services/${encodeURIComponent(project.id)}/${encodeURIComponent(service.id)}`"
                  class="group flex min-w-0 items-center gap-3 px-4 py-3 transition-colors hover:bg-muted/50 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring focus-visible:ring-inset"
                >
                  <StatusBadge :status="service.state" />
                  <div class="min-w-0 flex-1">
                    <p class="truncate text-sm font-medium">{{ service.name }}</p>
                    <p class="flex min-w-0 items-center gap-1.5 text-xs text-muted-foreground">
                      <span class="truncate">{{ service.kind }}</span>
                      <span aria-hidden="true">·</span>
                      <ServiceOwnership :owner="service.owner" />
                      <span aria-hidden="true">·</span>
                      <span class="capitalize">{{ service.selection }}</span>
                    </p>
                  </div>
                  <ArrowUpRight class="size-3.5 text-muted-foreground" />
                </RouterLink>
              </div>
              <p v-else class="px-4 py-8 text-center text-sm text-muted-foreground">No services are reported.</p>
            </CardContent>
          </Card>
        </div>

        <Card class="gap-0 rounded-lg py-0 shadow-none">
          <CardHeader class="border-b px-4 py-3"><CardTitle class="text-sm">Resources</CardTitle><p class="text-xs text-muted-foreground">Launchable HTTP resources reported by the daemon</p></CardHeader>
          <CardContent class="p-0">
            <div v-if="project.resources.length" class="divide-y">
              <button v-for="resource in project.resources" :key="resource.id" type="button" class="group flex w-full min-w-0 items-center gap-3 px-4 py-3 text-left hover:bg-muted/50" @click="openResource(resource.id)">
                <div class="min-w-0 flex-1"><p class="truncate text-sm font-medium">{{ resource.name }}</p><p class="truncate text-xs text-muted-foreground">{{ resource.kind }} · {{ resource.owner.kind }} · {{ resource.url }}</p></div>
                <ExternalLink class="size-3.5 text-muted-foreground" />
              </button>
            </div>
            <p v-else class="px-4 py-8 text-center text-sm text-muted-foreground">No resources are reported.</p>
          </CardContent>
        </Card>

        <Card v-if="currentProjectOperation" class="gap-0 rounded-lg py-0 shadow-none">
          <CardHeader class="border-b px-4 py-3"><CardTitle class="text-sm">Current activity</CardTitle></CardHeader>
          <CardContent class="p-0">
            <div class="flex items-center gap-3 px-4 py-3"><StatusBadge :status="currentProjectOperation.state" /><div><p class="text-sm font-medium">{{ currentProjectOperation.kind }}</p><p class="text-xs text-muted-foreground">{{ currentProjectOperation.phase }}</p></div></div>
          </CardContent>
        </Card>
      </div>
    </template>

    <Empty v-else class="min-h-full border-0">
      <EmptyHeader><EmptyTitle id="project-empty-title">{{ store.loading ? 'Loading project…' : projectId ? 'Project not found' : 'Select a project' }}</EmptyTitle><EmptyDescription>{{ projectId ? 'The current Harbor snapshot does not contain this project.' : 'Choose a registered project to inspect its reported state.' }}</EmptyDescription></EmptyHeader>
      <EmptyContent v-if="projectId && !store.loading"><Button variant="outline" as-child><RouterLink to="/projects"><ArrowLeft class="size-4" />Back to projects</RouterLink></Button></EmptyContent>
    </Empty>
  </main>
</template>
