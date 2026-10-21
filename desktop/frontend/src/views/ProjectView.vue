<script setup lang="ts">
import { computed, onBeforeUnmount, ref, shallowReactive, watch } from 'vue'
import { RouterLink, useRoute, useRouter } from 'vue-router'
import {
  ArrowLeft,
  LayoutDashboard,
  FileText,
  FileKey,
  Plug,
  Blocks,
  Check,
  Clipboard,
  ExternalLink,
  Globe2,
  Folder,
  Clock3,
  Server,
  ArrowUpRight,
  LockKeyhole,
  LoaderCircle,
  Network,
  Play,
  Plus,
  RefreshCw,
  Square,
  SquareTerminal,
  Trash2,
  TriangleAlert,
  X,
} from '@lucide/vue'
import StatusBadge from '@/components/harbor/StatusBadge.vue'
import StartupTimeline from '@/components/harbor/StartupTimeline.vue'
import InteractiveTerminal from '@/components/harbor/InteractiveTerminal.vue'
import ProjectConnectPanel from '@/components/harbor/ProjectConnectPanel.vue'
import ProjectEnvironmentPanel from '@/components/harbor/ProjectEnvironmentPanel.vue'
import ProjectFailurePanel from '@/components/harbor/ProjectFailurePanel.vue'
import PrerequisitePanel from '@/components/harbor/PrerequisitePanel.vue'
import ResourceFavicon from '@/components/harbor/ResourceFavicon.vue'
import ProjectIcon from '@/components/harbor/ProjectIcon.vue'
import KindIcon from '@/components/harbor/KindIcon.vue'
import ServiceIcon from '@/components/harbor/ServiceIcon.vue'
import ServiceLogsPanel from '@/components/harbor/ServiceLogsPanel.vue'
import LogViewer from '@/components/harbor/LogViewer.vue'
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
import { Checkbox } from '@/components/ui/checkbox'
import { Empty, EmptyContent, EmptyDescription, EmptyHeader, EmptyTitle } from '@/components/ui/empty'
import { Tabs, TabsContent, TabsList, TabsTrigger } from '@/components/ui/tabs'
import {
  Tooltip,
  TooltipContent,
  TooltipProvider,
  TooltipTrigger,
} from '@/components/ui/tooltip'
import { useProjectActivity } from '@/composables/useProjectActivity'
import { countReadyServices } from '@/lib/servicePresentation'
import { projectTerminalCleanup } from '@/lib/projectTerminalCleanup'
import { ProjectTerminalSession } from '@/lib/projectTerminalSession'
import { useHarborStore } from '@/stores/harbor'
import { harborBridge } from '@/bridge'
import type { ServicePort } from '@/domain/harbor'

const route = useRoute()
const router = useRouter()
const store = useHarborStore()
const copiedPath = ref(false)
const copiedProjectAddress = ref(false)
const removeOpen = ref(false)
const selectedDetailTab = ref('overview')
const projectId = computed(() => String(route.params.projectId ?? ''))
const projectTerminalLimit = 8
interface ProjectTerminalTab {
  error: string | null
  id: number
  name: string
  session: ProjectTerminalSession
}

interface ProjectTerminalWorkspace {
  nextTabID: number
  projectID: string
  projectName: string
  selectedTabID: number | null
  tabs: ProjectTerminalTab[]
  useEnvironmentOverrides: boolean
}

const projectTerminalWorkspaces = shallowReactive(new Map<string, ProjectTerminalWorkspace>())
const currentProjectTerminalWorkspace = computed(() => projectTerminalWorkspaces.get(projectId.value))
const renderedProjectTerminalWorkspaces = computed(() => [...projectTerminalWorkspaces.values()])
const projectTerminalTabs = computed(() => currentProjectTerminalWorkspace.value?.tabs ?? [])
const projectTerminalCount = computed(() => renderedProjectTerminalWorkspaces.value.reduce(
  (count, workspace) => count + workspace.tabs.length,
  0,
))
const selectedProjectTerminalTabID = computed<number | null>({
  get: () => currentProjectTerminalWorkspace.value?.selectedTabID ?? null,
  set: (id) => {
    const workspace = currentProjectTerminalWorkspace.value
    if (workspace) workspace.selectedTabID = id
  },
})
const selectedProjectTerminalTab = computed(() => projectTerminalTabs.value.find(
  (tab) => tab.id === selectedProjectTerminalTabID.value,
))
const projectTerminalUsesEnvironmentOverrides = computed({
  get: () => currentProjectTerminalWorkspace.value?.useEnvironmentOverrides ?? true,
  set: (enabled: boolean) => {
    projectTerminalWorkspace(projectId.value).useEnvironmentOverrides = enabled
  },
})
const closingProjectTerminalCount = projectTerminalCleanup.pendingCount
const closingProjectTerminalInFlight = projectTerminalCleanup.inFlightCount
const failedProjectTerminalCloseCount = projectTerminalCleanup.failedCount
const projectTerminalCleanupError = projectTerminalCleanup.error
let pendingInitialProjectTerminalID = ''
const selectedServiceId = ref('')
const selectedServiceSurface = ref('logs')
const project = computed(() => store.projectById(projectId.value))
const readyServiceCount = computed(() => countReadyServices(project.value?.services ?? []))
const projectActivitySupported = computed(() => store.daemonStatus?.capabilities.includes('control.project-activity.v1') === true)
const projectActivityWaitSupported = computed(() => store.daemonStatus?.capabilities.includes('control.project-activity-wait.v1') === true)
const projectEnvironmentSupported = computed(() => store.daemonStatus?.capabilities.includes('control.project-environment.v1') === true)
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
const showDevelopmentOutput = computed(() => (
  project.value?.state === 'failed'
  && store.projectLifecycleProblemCodes[projectId.value] === 'project.process.exited'
) || (
  projectActivitySupported.value && (
    projectActivitySession.value != null
    || developmentOutput.value !== ''
    || developmentOutputError.value != null
    || project.value?.state === 'starting'
    || project.value?.state === 'ready'
    || project.value?.state === 'rebuilding'
    || project.value?.state === 'degraded'
    || project.value?.state === 'stopping'
  )
))
const currentProjectOperation = computed(() => {
  for (let index = store.operations.length - 1; index >= 0; index -= 1) {
    const operation = store.operations[index]
    if (operation?.project_id === projectId.value) return operation
  }
  return undefined
})
const currentOperationLabel = computed(() => {
  const labels: Record<string, string> = {
    'project.start': 'Start project',
    'project.stop': 'Stop project',
    'project.restart': 'Restart project',
    'project.remove': 'Remove project',
    'project.reconcile': 'Project reconciliation',
  }
  return labels[currentProjectOperation.value?.kind ?? ''] ?? 'Project operation'
})
const updatedTime = computed(() => project.value
  ? new Intl.DateTimeFormat(undefined, { timeStyle: 'short' }).format(new Date(project.value.updated_at))
  : '')
const updatedDate = computed(() => project.value
  ? new Intl.DateTimeFormat(undefined, { dateStyle: 'medium' }).format(new Date(project.value.updated_at))
  : '')
const primaryResource = computed(() => project.value?.resources.find((resource) => resource.kind === 'application'))
const projectAddress = computed(() => primaryResource.value?.url ?? (project.value ? `https://${project.value.slug}.test` : ''))
const projectAddressPublished = computed(() => primaryResource.value != null)
const projectAddressSecure = computed(() => projectAddress.value.startsWith('https://'))
const selectedServicePorts = ref<ServicePort[]>([])
const selectedServicePortsError = ref<string | null>(null)

async function refreshSelectedServicePorts() {
  const serviceID = selectedServiceId.value
  if (!project.value || !serviceID) {
    selectedServicePorts.value = []
    return
  }
  selectedServicePortsError.value = null
  try {
    const logs = await harborBridge.getServiceLogs(project.value.id, '', serviceID, 0)
    selectedServicePorts.value = logs.ports ?? []
  }
  catch (error) {
    selectedServicePorts.value = []
    selectedServicePortsError.value = error instanceof Error ? error.message : 'Ports are unavailable.'
  }
}

watch([selectedServiceId, selectedServiceSurface], ([, surface]) => {
  if (surface === 'ports') void refreshSelectedServicePorts()
})
watch([selectedDetailTab, projectId], ([tab, selectedProjectID], [, previousProjectID]) => {
  if (!selectedProjectID && previousProjectID) closeProjectTerminalWorkspaces()
  if (selectedProjectID && tab === 'terminal' && projectTerminalTabs.value.length === 0) {
    void createInitialProjectTerminalTab(selectedProjectID)
  }
  if (tab !== 'terminal') pendingInitialProjectTerminalID = ''
}, { flush: 'sync' })
watch(closingProjectTerminalCount, (count) => {
  const selectedProjectID = pendingInitialProjectTerminalID
  if (count === 0 && selectedProjectID) void createInitialProjectTerminalTab(selectedProjectID)
})

// createInitialProjectTerminalTab waits for shells from the previous surface to release their bounded manager slots.
async function createInitialProjectTerminalTab(selectedProjectID: string) {
  await projectTerminalCleanup.waitForInFlight()
  if (
    selectedDetailTab.value !== 'terminal'
    || projectId.value !== selectedProjectID
    || projectTerminalTabs.value.length > 0
  ) return
  if (closingProjectTerminalCount.value > 0) {
    pendingInitialProjectTerminalID = selectedProjectID
    return
  }
  pendingInitialProjectTerminalID = ''
  createProjectTerminalTab(selectedProjectID)
}

// projectTerminalWorkspace retains one project's tabs while another project is selected.
function projectTerminalWorkspace(selectedProjectID: string) {
  let workspace = projectTerminalWorkspaces.get(selectedProjectID)
  if (workspace) return workspace
  workspace = shallowReactive({
    nextTabID: 1,
    projectID: selectedProjectID,
    projectName: store.projectById(selectedProjectID)?.name ?? selectedProjectID,
    selectedTabID: null,
    tabs: [],
    useEnvironmentOverrides: true,
  })
  projectTerminalWorkspaces.set(selectedProjectID, workspace)
  return workspace
}

// createProjectTerminalTab gives every tab its own PTY adapter and activates the new shell.
function createProjectTerminalTab(selectedProjectID = projectId.value) {
  if (closingProjectTerminalCount.value > 0 || projectTerminalCount.value >= projectTerminalLimit) return
  const workspace = projectTerminalWorkspace(selectedProjectID)
  const id = workspace.nextTabID
  workspace.nextTabID += 1
  const tab = {
    error: null,
    id,
    name: `Terminal ${id}`,
    session: new ProjectTerminalSession(
      harborBridge,
      selectedProjectID,
      workspace.useEnvironmentOverrides,
    ),
  }
  workspace.tabs = [...workspace.tabs, tab]
  workspace.selectedTabID = id
}

// closeProjectTerminalTab terminates only the selected tab's desktop-owned shell.
function closeProjectTerminalTab(id: number) {
  const workspace = currentProjectTerminalWorkspace.value
  if (!workspace) return
  const tabs = [...workspace.tabs]
  const index = tabs.findIndex((tab) => tab.id === id)
  if (index === -1) return
  const [closed] = tabs.splice(index, 1)
  workspace.tabs = tabs
  if (workspace.selectedTabID === id) {
    workspace.selectedTabID = tabs[index]?.id ?? tabs[index - 1]?.id ?? null
  }
  if (closed) projectTerminalCleanup.close(closed.session)
}

// closeProjectTerminalWorkspace releases only the project that can no longer be revisited.
function closeProjectTerminalWorkspace(selectedProjectID: string) {
  const workspace = projectTerminalWorkspaces.get(selectedProjectID)
  if (!workspace) return
  projectTerminalWorkspaces.delete(selectedProjectID)
  for (const tab of workspace.tabs) projectTerminalCleanup.close(tab.session)
}

// closeProjectTerminalWorkspaces releases every retained PTY when the project surface is destroyed.
function closeProjectTerminalWorkspaces() {
  const workspaces = [...projectTerminalWorkspaces.values()]
  projectTerminalWorkspaces.clear()
  for (const workspace of workspaces) {
    for (const tab of workspace.tabs) projectTerminalCleanup.close(tab.session)
  }
}

// retryProjectTerminalCleanup lets the user reconcile a close whose bridge result was indeterminate.
function retryProjectTerminalCleanup() {
  projectTerminalCleanup.retryFailed()
}

// reportProjectTerminalError keeps PTY transport failures inside their owning project surface.
function reportProjectTerminalError(selectedProjectID: string, id: number, error: Error) {
  const workspace = projectTerminalWorkspaces.get(selectedProjectID)
  if (!workspace) return
  workspace.tabs = workspace.tabs.map((tab) => (
    tab.id === id ? { ...tab, error: error.message } : tab
  ))
}
const removalNotice = computed(() => store.projectRemovalNotice(projectId.value))
const activeLifecycle = computed(() => store.activeProjectLifecycle(projectId.value))
const lifecycleError = computed(() => store.projectLifecycleErrors[projectId.value])
const lifecycleProblemCode = computed(() => store.projectLifecycleProblemCodes[projectId.value])
const checkoutMissing = computed(() => lifecycleProblemCode.value === 'project.checkout.missing')
const needsNetworkSetup = computed(() => lifecycleProblemCode.value === 'project.network.setup_required'
  || lifecycleProblemCode.value === 'project.network.full_setup_required'
  || lifecycleProblemCode.value === 'project.network.identity_unavailable'
  || lifecycleProblemCode.value === 'project.network.repair_required')
const needsFullNetworkSetup = computed(() => lifecycleProblemCode.value === 'project.network.full_setup_required')
const needsNetworkRepair = computed(() => lifecycleProblemCode.value === 'project.network.identity_unavailable'
  || lifecycleProblemCode.value === 'project.network.repair_required')
const recoveryRequired = computed(() => lifecycleProblemCode.value === 'project.recovery.ambiguous_launch')
const runtimeRepairNotice = computed(() => store.projectRuntimeRepairNotice(projectId.value))
const lifecycleInFlight = computed(() => store.projectLifecycleBusyFor(projectId.value))
const starting = computed(() => project.value?.state === 'starting' || activeLifecycle.value?.kind === 'project.start')
const stopping = computed(() => project.value?.state === 'stopping' || activeLifecycle.value?.kind === 'project.stop')
const restarting = computed(() => project.value?.state === 'rebuilding' || activeLifecycle.value?.kind === 'project.restart')
const restartAvailable = computed(() => project.value?.state === 'ready'
  || project.value?.state === 'degraded'
  || restarting.value)
const resourceOpenAvailable = computed(() => (project.value?.state === 'ready' || project.value?.state === 'degraded')
  && primaryResource.value != null
  && !recoveryRequired.value)
const lifecycleAction = computed(() => project.value?.state === 'stopped'
  || project.value?.state === 'failed'
  || project.value?.state === 'unavailable'
  ? 'start'
  : 'stop')
const lifecycleLabel = computed(() => {
  if (restarting.value) return 'Restarting…'
  if (starting.value) return 'Starting…'
  if (stopping.value) return 'Stopping…'
  return lifecycleAction.value === 'start' ? 'Start project' : 'Stop project'
})
const lifecycleControlsDisabled = computed(() => store.snapshotStale
  || store.connectionState !== 'connected'
  || store.settingUpNetwork
  || store.projectRuntimeRepairBusy
  || store.projectRemovalApprovalBusy
  || removalPending.value)
const lifecycleDisabled = computed(() => lifecycleControlsDisabled.value
  || store.projectLifecycleBlockedFor(projectId.value, lifecycleAction.value))
const showFailureDetails = computed(() => !needsNetworkSetup.value && !recoveryRequired.value && !checkoutMissing.value
  && Boolean(lifecycleError.value || project.value?.state === 'failed'))
const restartDisabled = computed(() => lifecycleControlsDisabled.value
  || store.projectLifecycleBlockedFor(projectId.value, 'restart')
  || !restartAvailable.value)
const networkSetupDisabled = computed(() => !needsNetworkSetup.value
  || project.value?.id !== projectId.value
  || store.settingUpNetwork
  || store.projectLifecycleBusy
  || store.projectRuntimeRepairBusy
  || store.projectRemovalApprovalBusy
  || store.snapshotStale
  || store.connectionState !== 'connected')
const removing = computed(() => store.removingProjectId === projectId.value)
const approvingRemoval = computed(() => store.projectRemovalApprovalProjectId === projectId.value)
const removalPending = computed(() => removalNotice.value?.state === 'queued'
  || removalNotice.value?.state === 'running'
  || removalNotice.value?.state === 'requires_approval')
const removalDisabled = computed(() => store.removingProjectId !== null
  || store.projectLifecycleBusy
  || store.projectRuntimeRepairBusy
  || store.projectRemovalApprovalBusy
  || activeLifecycle.value != null
  || recoveryRequired.value
  || removalPending.value)
const removalLabel = computed(() => {
  if (removing.value) return 'Removing…'
  if (approvingRemoval.value) return 'Approving…'
  if (store.removingProjectId || store.projectRemovalApprovalProjectId) return 'Another removal is in progress'
  if (removalNotice.value?.state === 'requires_approval') return 'Awaiting approval'
  if (removalPending.value) return 'Removal in progress'
  return 'Remove project'
})
const removalApprovalDisabled = computed(() => removalNotice.value?.state !== 'requires_approval'
  || store.connectionState !== 'connected'
  || store.snapshotStale
  || store.projectRemovalApprovalBusy
  || store.removingProjectId !== null
  || store.projectLifecycleBusy
  || store.projectRuntimeRepairBusy)
const updatedAt = computed(() => project.value
  ? new Intl.DateTimeFormat(undefined, { dateStyle: 'medium', timeStyle: 'medium' }).format(new Date(project.value.updated_at))
  : '')

watch([projectId, project], ([nextProjectId, nextProject], [previousProjectId, previousProject]) => {
  if (nextProjectId !== previousProjectId) {
    selectedDetailTab.value = 'overview'
  }
  if (nextProjectId && nextProjectId === previousProjectId && previousProject && !nextProject) {
    closeProjectTerminalWorkspace(nextProjectId)
    void router.replace('/projects')
  }
})

watch(() => project.value?.services, (services) => {
  if (!services?.some((service) => service.id === selectedServiceId.value)) {
    selectedServiceId.value = services?.[0]?.id ?? ''
  }
}, { immediate: true })

watch(selectedServiceId, () => {
  selectedServiceSurface.value = 'logs'
})

onBeforeUnmount(() => {
  closeProjectTerminalWorkspaces()
})

async function copyPath() {
  if (!project.value) return
  await copyText(project.value.path)
  copiedPath.value = true
  window.setTimeout(() => { copiedPath.value = false }, 1600)
}

async function copyProjectAddress() {
  if (!projectAddress.value) return
  await copyText(projectAddress.value)
  copiedProjectAddress.value = true
  window.setTimeout(() => { copiedProjectAddress.value = false }, 1600)
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

async function approveProjectRemoval() {
  if (removalApprovalDisabled.value) return
  const result = await store.approveProjectRemoval(projectId.value)
  if (result?.operation.state === 'succeeded') {
    await router.replace('/projects')
  }
}

async function changeProjectLifecycle() {
  if (!project.value) return
  if (lifecycleAction.value === 'start') {
    await startProject(project.value.id)
    return
  }
  await store.stopProject(project.value.id)
}

// retryFailedProject uses ordinary Start admission only while the selected project still accepts it.
async function retryFailedProject() {
  if (lifecycleDisabled.value || lifecycleAction.value !== 'start') return
  await startProject(projectId.value)
}

async function startProject(requestedProjectId: string, options?: { checkPrerequisites?: boolean }) {
  if (projectId.value === requestedProjectId
    && project.value?.id === requestedProjectId
    && selectedDetailTab.value === 'overview') {
    selectedDetailTab.value = 'output'
  }
  await store.startProject(requestedProjectId, options)
}

async function restartProject() {
  if (!project.value || restartDisabled.value) return
  await store.restartProject(project.value.id)
}

async function setupNetworkAndStartProject() {
  const requestedProjectId = projectId.value
  if (networkSetupDisabled.value || project.value?.id !== requestedProjectId) return
  const result = needsNetworkRepair.value
    ? await store.repairNetwork()
    : await store.setupNetwork()
  if (!result
    || projectId.value !== requestedProjectId
    || store.projectById(requestedProjectId)?.id !== requestedProjectId
    || store.snapshotStale
    || store.connectionState !== 'connected'
    || store.projectLifecycleBusyFor(requestedProjectId)) return
  await startProject(requestedProjectId)
}

</script>

<template>
  <main class="harbor-project flex h-full min-w-0 flex-col overflow-y-auto" :aria-labelledby="project ? 'project-title' : 'project-empty-title'">
    <template v-if="project">
      <header class="project-header shrink-0 border-b px-5 py-5 lg:px-7">
        <div class="project-heading flex min-w-0 flex-wrap items-center gap-4">
          <Button variant="ghost" size="icon-sm" class="-ml-2 shrink-0 min-[1100px]:hidden" as-child>
            <RouterLink to="/projects" aria-label="Back to projects"><ArrowLeft class="size-4" /></RouterLink>
          </Button>
          <div class="project-identity flex min-w-0 flex-1 items-center gap-3">
            <div class="project-emblem flex size-11 shrink-0 items-center justify-center rounded-xl border bg-card">
              <ProjectIcon :project="project" />
            </div>
            <div class="min-w-0">
              <div class="flex min-w-0 flex-wrap items-center gap-2.5">
                <h1 id="project-title" class="truncate text-xl font-semibold tracking-tight">{{ project.name }}</h1>
                <StatusBadge :status="project.state" />
              </div>
              <button type="button" class="mt-1 inline-flex max-w-full items-center gap-1.5 rounded-sm text-xs text-muted-foreground hover:text-foreground focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring" :title="project.path" @click="copyPath">
                <Check v-if="copiedPath" class="size-3 shrink-0" /><Folder v-else class="size-3 shrink-0" />
                <span class="truncate">{{ copiedPath ? 'Path copied' : project.path }}</span>
              </button>
            </div>
          </div>
          <TooltipProvider :delay-duration="300">
            <div class="project-actions flex shrink-0 items-center gap-1.5">
              <Tooltip>
                <TooltipTrigger as-child>
                  <span
                    v-if="lifecycleInFlight || starting || stopping || restarting"
                    class="flex size-8 items-center justify-center text-primary"
                    :aria-label="lifecycleLabel"
                    role="status"
                  >
                    <span
                      class="size-4 animate-spin rounded-full border-2 border-current border-r-transparent border-b-transparent [animation-duration:700ms]"
                      aria-hidden="true"
                    />
                  </span>
                  <Button
                    v-else
                    variant="outline"
                    size="sm"
                    :class="lifecycleAction === 'start' ? 'text-primary hover:text-primary' : undefined"
                    :aria-label="lifecycleLabel"
                    :disabled="lifecycleDisabled"
                    @click="changeProjectLifecycle"
                  >
                    <Play v-if="lifecycleAction === 'start'" class="fill-current" aria-hidden="true" />
                    <Square v-else class="size-3 fill-current" aria-hidden="true" />
                    {{ lifecycleAction === 'start' ? 'Start project' : 'Stop' }}
                  </Button>
                </TooltipTrigger>
                <TooltipContent>{{ lifecycleLabel }}</TooltipContent>
              </Tooltip>
              <Tooltip v-if="restartAvailable">
                <TooltipTrigger as-child>
                  <span
                    v-if="restarting || lifecycleInFlight"
                    class="flex size-8 items-center justify-center text-muted-foreground"
                    aria-label="Restarting…"
                    role="status"
                  >
                    <span
                      class="size-4 animate-spin rounded-full border-2 border-current border-r-transparent border-b-transparent [animation-duration:700ms]"
                      aria-hidden="true"
                    />
                  </span>
                  <Button
                    v-else
                    variant="ghost"
                    size="icon-sm"
                    aria-label="Restart project"
                    :disabled="restartDisabled"
                    @click="restartProject"
                  >
                    <RefreshCw aria-hidden="true" />
                  </Button>
                </TooltipTrigger>
                <TooltipContent>{{ restarting || lifecycleInFlight ? 'Restarting…' : 'Restart project' }}</TooltipContent>
              </Tooltip>
              <AlertDialog v-model:open="removeOpen">
                <Tooltip>
                  <TooltipTrigger as-child>
                    <AlertDialogTrigger as-child>
                      <Button variant="ghost" size="icon-sm" class="hover:text-destructive" :aria-label="removalLabel" :disabled="removalDisabled">
                        <Trash2 aria-hidden="true" />
                      </Button>
                    </AlertDialogTrigger>
                  </TooltipTrigger>
                  <TooltipContent>{{ removalLabel }}</TooltipContent>
                </Tooltip>
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
              <Tooltip v-if="resourceOpenAvailable">
                <TooltipTrigger as-child>
                  <Button
                    variant="default"
                    size="sm"
                    class="ml-1"
                    aria-label="Open resource"
                    @click="primaryResource && openResource(primaryResource.id)"
                  >
                    <ArrowUpRight aria-hidden="true" />
                    Open app
                  </Button>
                </TooltipTrigger>
                <TooltipContent>Open resource</TooltipContent>
              </Tooltip>
            </div>
          </TooltipProvider>
        </div>
        <div class="project-location mt-4 flex min-w-0 items-center justify-between gap-4">
          <div
            data-testid="project-address-bar"
            class="project-address flex h-8 min-w-0 items-center gap-2 text-xs"
          >
            <LockKeyhole
              v-if="projectAddressSecure"
              class="size-4 shrink-0"
              :class="projectAddressPublished ? 'text-emerald-500' : 'text-muted-foreground'"
              aria-hidden="true"
            />
            <Globe2 v-else class="size-4 shrink-0 text-muted-foreground" aria-hidden="true" />
            <span class="min-w-0 flex-1 truncate font-mono" :class="{ 'text-muted-foreground': !projectAddressPublished }">{{ projectAddress }}</span>
            <TooltipProvider :delay-duration="300">
              <Tooltip>
                <TooltipTrigger as-child>
                  <Button
                    variant="ghost"
                    size="icon-sm"
                    class="-mr-2 size-7 text-muted-foreground hover:text-foreground"
                    :aria-label="copiedProjectAddress ? 'Project URL copied' : 'Copy project URL'"
                    @click="copyProjectAddress"
                  >
                    <Check v-if="copiedProjectAddress" aria-hidden="true" />
                    <Clipboard v-else aria-hidden="true" />
                  </Button>
                </TooltipTrigger>
                <TooltipContent>{{ copiedProjectAddress ? 'Project URL copied' : 'Copy project URL' }}</TooltipContent>
              </Tooltip>
            </TooltipProvider>
          </div>
          <span class="hidden shrink-0 text-[11px] text-muted-foreground sm:block" :title="`Updated ${updatedAt}`">{{ project.slug }}</span>
        </div>
      </header>

      <div v-if="store.projectPrerequisiteChecking[projectId] || store.projectPrerequisiteErrors[projectId] || store.projectPrerequisites[projectId]" class="px-5 pt-5 lg:px-7">
        <PrerequisitePanel
          :report="store.projectPrerequisites[projectId]"
          :checking="store.projectPrerequisiteChecking[projectId]"
          :error="store.projectPrerequisiteErrors[projectId]"
          :continue-available="lifecycleAction === 'start' && !lifecycleDisabled"
          compact
          @recheck="store.checkProjectPrerequisites(projectId)"
          @continue="startProject(projectId, { checkPrerequisites: false })"
        />
      </div>

      <div v-if="showFailureDetails" class="px-5 pt-5 lg:px-7">
        <ProjectFailurePanel
          :key="project.id"
          :project-name="project.name"
          :message="lifecycleError ?? ''"
          :code="lifecycleProblemCode ?? ''"
          :session="projectActivitySession"
          :output="developmentOutput"
          :truncated="developmentOutputTruncated"
          :output-error="developmentOutputError"
          :retry-available="lifecycleAction === 'start'"
          :retry-disabled="lifecycleDisabled"
          @retry="retryFailedProject"
          @view-output="selectedDetailTab = 'output'"
        />
      </div>
      <div v-if="!showFailureDetails && (lifecycleError || (recoveryRequired && runtimeRepairNotice))" class="px-5 pt-5 lg:px-7">
        <Alert :variant="recoveryRequired || needsNetworkSetup ? 'default' : 'destructive'">
          <TriangleAlert aria-hidden="true" />
          <AlertTitle>{{ recoveryRequired ? 'Ready to start again' : needsNetworkRepair ? 'Harbor networking needs repair' : needsNetworkSetup ? 'Secure networking is not ready' : checkoutMissing ? 'Project folder is missing' : 'Project action failed' }}</AlertTitle>
          <AlertDescription class="space-y-3">
            <p v-if="recoveryRequired">Starting again will reconcile the previous runtime and launch a fresh process.</p>
            <p v-else-if="needsNetworkRepair">Harbor's required loopback addresses are missing. Approve restoring the existing Harbor-owned addresses, then start this project.</p>
            <p v-else-if="needsFullNetworkSetup">Harbor's DNS foundation is active, but secure, trusted local ingress is not ready. Set up networking to finish HTTPS and ingress, then start this project.</p>
            <p v-else-if="needsNetworkSetup">Set up Harbor's secure, trusted local DNS, HTTPS, and ingress before starting this project.</p>
            <p v-else-if="lifecycleError">{{ lifecycleError }}</p>
            <p v-if="recoveryRequired && runtimeRepairNotice" class="text-destructive">
              {{ runtimeRepairNotice.message }}
            </p>
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
              {{ store.settingUpNetwork ? (needsNetworkRepair ? 'Repairing Harbor networking…' : 'Setting up secure networking…') : (needsNetworkRepair ? 'Repair networking and start' : 'Set up secure networking and start') }}
            </Button>
          </AlertDescription>
        </Alert>
      </div>

      <Tabs v-model="selectedDetailTab" class="project-tabs min-h-0 min-w-0 flex-1 gap-0" :data-surface="selectedDetailTab">
        <TabsList class="h-11 w-full shrink-0 justify-start gap-5 overflow-x-auto rounded-none border-b bg-transparent px-5 py-0 lg:px-7">
          <TabsTrigger value="overview" class="h-11 flex-none rounded-none border-x-0 border-t-0 border-b-2 border-transparent bg-transparent px-0 text-muted-foreground shadow-none hover:text-foreground data-[state=active]:!border-primary data-[state=active]:!bg-transparent data-[state=active]:text-foreground data-[state=active]:!shadow-none dark:data-[state=active]:!bg-transparent"><LayoutDashboard class="project-tab-icon" aria-hidden="true" />Overview</TabsTrigger>
          <TabsTrigger value="output" class="h-11 flex-none rounded-none border-x-0 border-t-0 border-b-2 border-transparent bg-transparent px-0 text-muted-foreground shadow-none hover:text-foreground data-[state=active]:!border-primary data-[state=active]:!bg-transparent data-[state=active]:text-foreground data-[state=active]:!shadow-none dark:data-[state=active]:!bg-transparent"><FileText class="project-tab-icon" aria-hidden="true" />Development output</TabsTrigger>
          <TabsTrigger value="terminal" class="h-11 flex-none rounded-none border-x-0 border-t-0 border-b-2 border-transparent bg-transparent px-0 text-muted-foreground shadow-none hover:text-foreground data-[state=active]:!border-primary data-[state=active]:!bg-transparent data-[state=active]:text-foreground data-[state=active]:!shadow-none dark:data-[state=active]:!bg-transparent"><SquareTerminal class="project-tab-icon" aria-hidden="true" />Terminal</TabsTrigger>
          <TabsTrigger value="environment" class="h-11 flex-none rounded-none border-x-0 border-t-0 border-b-2 border-transparent bg-transparent px-0 text-muted-foreground shadow-none hover:text-foreground data-[state=active]:!border-primary data-[state=active]:!bg-transparent data-[state=active]:text-foreground data-[state=active]:!shadow-none dark:data-[state=active]:!bg-transparent"><FileKey class="project-tab-icon" aria-hidden="true" />Environment</TabsTrigger>
          <TabsTrigger value="connect" class="h-11 flex-none rounded-none border-x-0 border-t-0 border-b-2 border-transparent bg-transparent px-0 text-muted-foreground shadow-none hover:text-foreground data-[state=active]:!border-primary data-[state=active]:!bg-transparent data-[state=active]:text-foreground data-[state=active]:!shadow-none dark:data-[state=active]:!bg-transparent"><Plug class="project-tab-icon" aria-hidden="true" />Connect</TabsTrigger>
          <TabsTrigger value="services" class="h-11 flex-none rounded-none border-x-0 border-t-0 border-b-2 border-transparent bg-transparent px-0 text-muted-foreground shadow-none hover:text-foreground data-[state=active]:!border-primary data-[state=active]:!bg-transparent data-[state=active]:text-foreground data-[state=active]:!shadow-none dark:data-[state=active]:!bg-transparent"><Server class="project-tab-icon" aria-hidden="true" />Services <span class="text-xs tabular-nums text-muted-foreground">{{ project.services.length }}</span></TabsTrigger>
          <TabsTrigger value="resources" class="h-11 flex-none rounded-none border-x-0 border-t-0 border-b-2 border-transparent bg-transparent px-0 text-muted-foreground shadow-none hover:text-foreground data-[state=active]:!border-primary data-[state=active]:!bg-transparent data-[state=active]:text-foreground data-[state=active]:!shadow-none dark:data-[state=active]:!bg-transparent"><Blocks class="project-tab-icon" aria-hidden="true" />Resources <span class="text-xs tabular-nums text-muted-foreground">{{ project.resources.length }}</span></TabsTrigger>
        </TabsList>

        <div class="project-tab-body" :class="selectedDetailTab === 'output' || selectedDetailTab === 'terminal' || selectedDetailTab === 'environment' || selectedDetailTab === 'services' ? 'flex min-h-0 flex-1 flex-col gap-5 p-5 lg:p-7' : 'space-y-5 p-5 lg:p-7'">
        <StartupTimeline v-if="projectActivitySession?.startup && (selectedDetailTab === 'overview' || selectedDetailTab === 'output')" :key="`${project.id}:${selectedDetailTab}`" :compact="selectedDetailTab === 'output'" :progress="projectActivitySession.startup" :settled="project.state !== 'starting' && project.state !== 'rebuilding'" />
        <Alert
          v-if="runtimeRepairNotice && !recoveryRequired"
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
          <AlertDescription class="space-y-3">
            <p>{{ removalNotice.message }}</p>
            <Button
              v-if="removalNotice.state === 'requires_approval'"
              variant="outline"
              size="sm"
              :disabled="removalApprovalDisabled"
              @click="approveProjectRemoval"
            >
              <LoaderCircle v-if="approvingRemoval" class="size-3.5 animate-spin" aria-hidden="true" />
              <Check v-else class="size-3.5" aria-hidden="true" />
              {{ approvingRemoval ? 'Approving…' : 'Approve and remove' }}
            </Button>
          </AlertDescription>
        </Alert>

        <TabsContent value="overview" class="m-0 space-y-5">
          <section aria-label="Project summary" class="project-summary grid grid-cols-2 gap-3 xl:grid-cols-4">
            <div class="summary-stat"><span class="summary-label"><KindIcon kind="application" compact />Applications</span><strong>{{ project.apps.length }}</strong><span class="summary-note">{{ project.apps.filter(app => app.active).length }} active</span></div>
            <div class="summary-stat"><span class="summary-label"><KindIcon kind="service" compact />Services</span><strong>{{ readyServiceCount }}<span class="summary-total"> ready</span></strong><span class="summary-note">{{ project.services.length }} reported</span></div>
            <div class="summary-stat"><span class="summary-label"><KindIcon kind="docs" compact />Resources</span><strong>{{ project.resources.length }}</strong><span class="summary-note">Available endpoints</span></div>
            <div class="summary-stat"><span class="summary-label"><Clock3 />Last updated</span><strong class="!text-base">{{ updatedTime }}</strong><span class="summary-note">{{ updatedDate }}</span></div>
          </section>

          <section v-if="project.resources.length" aria-label="Quick access" class="project-quick-access">
            <div class="mb-3 flex items-center justify-between"><h2 class="text-sm font-semibold">Quick access</h2><button type="button" class="text-xs text-muted-foreground hover:text-foreground" @click="selectedDetailTab = 'resources'">All resources <ArrowUpRight class="ml-1 inline size-3" /></button></div>
            <div class="grid gap-2 sm:grid-cols-2 xl:grid-cols-3">
              <button v-for="resource in project.resources.slice(0, 6)" :key="resource.id" type="button" class="quick-resource group flex min-w-0 items-center gap-3 rounded-lg border bg-card px-4 py-3 text-left transition-colors hover:border-muted-foreground/40 hover:bg-accent/40 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring" @click="openResource(resource.id)">
                <ResourceFavicon :kind="resource.kind" :name="resource.name" :url="resource.url" :project-id="project.id" :resource-id="resource.id" compact />
                <div class="min-w-0 flex-1"><p class="truncate text-xs font-medium">{{ resource.name }}</p><p class="mt-1 truncate font-mono text-[11px] text-muted-foreground">{{ resource.url.replace(/^https?:\/\//, '') }}</p></div>
                <ArrowUpRight class="size-3.5 text-muted-foreground group-hover:text-foreground" />
              </button>
            </div>
          </section>

          <div class="project-runtime-grid grid gap-4 xl:grid-cols-2">
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
            <CardHeader class="border-b px-4 py-3"><div class="flex items-center gap-2"><Server class="size-4 text-muted-foreground" /><CardTitle class="text-sm">Services</CardTitle><button type="button" class="ml-auto text-xs text-muted-foreground hover:text-foreground" @click="selectedDetailTab = 'services'">View all <ArrowUpRight class="ml-1 inline size-3" /></button></div></CardHeader>
            <CardContent class="p-0">
              <div v-if="project.services.length" class="divide-y">
                <button v-for="service in project.services.slice(0, 6)" :key="service.id" type="button" class="flex w-full items-center gap-3 px-4 py-3 text-left hover:bg-muted/50" :aria-label="`View ${service.name} service`" @click="selectedServiceId = service.id; selectedDetailTab = 'services'">
                  <ServiceIcon :service="service" compact />
                  <div class="min-w-0 flex-1"><p class="truncate text-sm font-medium">{{ service.name }}</p><p class="text-xs text-muted-foreground">{{ service.kind }}</p></div>
                  <StatusBadge :status="service.state" />
                </button>
              </div>
              <p v-else class="px-4 py-8 text-center text-sm text-muted-foreground">No services are reported.</p>
            </CardContent>
          </Card>
          </div>

          <Card v-if="currentProjectOperation" class="gap-0 rounded-lg py-0 shadow-none">
            <CardHeader class="border-b px-4 py-3"><CardTitle class="text-sm">Current activity</CardTitle></CardHeader>
            <CardContent class="p-0">
              <div class="flex items-center gap-3 px-4 py-3"><StatusBadge :status="currentProjectOperation.state" /><div><p class="text-sm font-medium">{{ currentOperationLabel }}</p><p class="text-xs text-muted-foreground">{{ currentProjectOperation.phase }}</p></div></div>
            </CardContent>
          </Card>
        </TabsContent>

        <TabsContent value="terminal" force-mount class="m-0 flex min-h-0 flex-1 flex-col data-[state=inactive]:hidden">
          <Card class="flex min-h-[28rem] flex-1 flex-col gap-0 overflow-hidden rounded-lg py-0 shadow-none">
            <div class="flex h-11 w-full shrink-0 items-center rounded-none border-b bg-transparent px-5 py-0 lg:px-7">
              <div class="flex h-11 min-w-0 flex-1 items-center gap-5 overflow-x-auto" role="tablist" aria-label="Project terminal sessions">
                <div
                  v-for="tab in projectTerminalTabs"
                  :key="tab.id"
                  class="flex h-11 flex-none items-center rounded-none border-x-0 border-t-0 border-b-2 border-transparent bg-transparent px-0 text-muted-foreground shadow-none hover:text-foreground data-[state=active]:!border-primary data-[state=active]:!bg-transparent data-[state=active]:text-foreground data-[state=active]:!shadow-none dark:data-[state=active]:!bg-transparent"
                  :data-state="selectedProjectTerminalTabID === tab.id ? 'active' : 'inactive'"
                >
                  <button type="button" role="tab" class="h-full text-sm font-medium" :aria-selected="selectedProjectTerminalTabID === tab.id" @click="selectedProjectTerminalTabID = tab.id">{{ tab.name }}</button>
                  <button type="button" class="ml-1 rounded-sm p-1 text-muted-foreground hover:text-foreground" :aria-label="`Close ${tab.name}`" @click="closeProjectTerminalTab(tab.id)"><X class="size-3" /></button>
                </div>
                <button
                  type="button"
                  class="flex h-11 flex-none items-center rounded-none border-x-0 border-t-0 border-b-2 border-transparent bg-transparent px-0 text-muted-foreground shadow-none hover:text-foreground disabled:cursor-not-allowed disabled:opacity-50"
                  aria-label="New terminal"
                  :disabled="closingProjectTerminalCount > 0 || projectTerminalCount >= projectTerminalLimit"
                  :title="projectTerminalCount >= projectTerminalLimit || closingProjectTerminalCount > 0 ? 'Close a terminal tab before opening another.' : 'New terminal'"
                  @click="createProjectTerminalTab()"
                >
                  <Plus class="size-4" />
                </button>
              </div>
              <label
                class="ml-5 flex shrink-0 cursor-pointer items-center gap-2 text-xs text-muted-foreground"
                title="Apply Harbor's resolved project environment to new terminal sessions."
              >
                <Checkbox
                  :model-value="projectTerminalUsesEnvironmentOverrides"
                  aria-label="Use environment overrides for new terminals"
                  @update:model-value="projectTerminalUsesEnvironmentOverrides = $event === true"
                />
                Environment overrides
              </label>
            </div>
            <CardContent class="flex min-h-0 flex-1 flex-col p-0">
              <div v-if="selectedProjectTerminalTab?.error || projectTerminalCleanupError" class="flex items-center justify-between gap-3 border-b px-4 py-2 text-xs text-destructive">
                <p>{{ selectedProjectTerminalTab?.error ?? projectTerminalCleanupError }}</p>
                <Button
                  v-if="projectTerminalCleanupError && failedProjectTerminalCloseCount > 0"
                  type="button"
                  variant="outline"
                  size="sm"
                  :disabled="closingProjectTerminalInFlight > 0"
                  @click="retryProjectTerminalCleanup"
                >
                  Retry cleanup
                </Button>
              </div>
              <template v-for="workspace in renderedProjectTerminalWorkspaces" :key="workspace.projectID">
                <InteractiveTerminal
                  v-for="tab in workspace.tabs"
                  :key="`${workspace.projectID}:${tab.id}`"
                  v-show="project.id === workspace.projectID && selectedProjectTerminalTabID === tab.id"
                  :active="selectedDetailTab === 'terminal' && project.id === workspace.projectID && selectedProjectTerminalTabID === tab.id"
                  :session="tab.session"
                  :aria-label="`${workspace.projectName} ${tab.name.toLowerCase()}`"
                  class="min-h-0 flex-1"
                  @error="reportProjectTerminalError(workspace.projectID, tab.id, $event)"
                />
              </template>
            </CardContent>
          </Card>
        </TabsContent>

        <TabsContent value="environment" force-mount class="m-0 flex min-h-0 flex-1 flex-col data-[state=inactive]:hidden">
          <ProjectEnvironmentPanel
            :active="selectedDetailTab === 'environment'"
            :project-id="project.id"
            :supported="projectEnvironmentSupported"
            class="min-h-0 flex-1"
          />
        </TabsContent>

        <TabsContent value="connect" class="m-0">
          <ProjectConnectPanel
            :active="selectedDetailTab === 'connect'"
            :project="project"
            :sequence="snapshotSequence"
          />
        </TabsContent>

        <TabsContent value="output" class="m-0 flex min-h-0 flex-1 flex-col">
        <section v-if="showDevelopmentOutput" aria-label="Development output panel" class="project-log flex min-h-0 flex-1 flex-col overflow-hidden">
          <LogViewer
            :output="developmentOutput"
            :reset-key="developmentOutputResetKey"
            :identity="`${projectId}:${projectActivitySession?.id ?? ''}`"
            label="Current project development output"
          >
            <template #label><SquareTerminal class="size-3.5" /><span class="font-mono">forj dev</span><span class="mx-1 text-border">/</span><span>{{ project.name }}</span></template>
            <template #notices><p v-if="developmentOutputError" class="border-b px-4 py-2 text-xs text-destructive">{{ developmentOutputError }}</p></template>
            <template #viewport-notices>
              <p v-if="developmentOutputTruncated" class="mb-2 text-amber-700 dark:text-amber-300">Earlier output is no longer retained. Search covers retained output only.</p>
              <p v-if="projectActivitySession?.output.historical" class="mb-2 text-amber-700 dark:text-amber-300">Showing retained output from before Harbor reconnected; live output will resume when the process is reattached.</p>
            </template>
            <template #empty>
              <p v-if="projectActivitySession?.output.historical" class="text-muted-foreground">No retained output was recorded before Harbor reconnected.</p>
              <p v-else-if="project?.state === 'failed' && lifecycleProblemCode === 'project.process.exited'" class="text-muted-foreground">Harbor retained the launch trace at <code>_data/harbor/forj-dev.log</code>.</p>
              <p v-else-if="projectActivitySession && !projectActivitySession.output.available" class="text-muted-foreground">The current process is not available to stream output.</p>
              <p v-else class="text-muted-foreground">Waiting for <code>forj dev</code> output…</p>
            </template>
          </LogViewer>
        </section>
        <Empty v-else class="min-h-0 flex-1 rounded-lg border">
          <EmptyHeader><EmptyTitle>No development output yet</EmptyTitle><EmptyDescription>Harbor will show the current <code>forj dev</code> session here when the project starts.</EmptyDescription></EmptyHeader>
        </Empty>
        </TabsContent>

        <TabsContent value="services" class="m-0 flex min-h-0 flex-1 flex-col">
          <Tabs v-if="project.services.length" v-model="selectedServiceId" class="-mx-5 -mt-5 flex min-h-0 flex-1 flex-col gap-0 lg:-mx-7">
            <TabsList class="h-9 w-full shrink-0 justify-start gap-5 overflow-x-auto rounded-none border-b bg-transparent px-5 py-0 lg:px-7">
              <TabsTrigger
                v-for="service in project.services"
                :key="service.id"
                :value="service.id"
                class="h-9 flex-none gap-2 rounded-none border-x-0 border-t-0 border-b-2 border-transparent bg-transparent px-0 text-muted-foreground shadow-none hover:text-foreground data-[state=active]:!border-primary data-[state=active]:!bg-transparent data-[state=active]:text-foreground data-[state=active]:!shadow-none dark:data-[state=active]:!bg-transparent"
              >
                <span
                  class="size-1.5 rounded-full"
                  :class="{
                    'bg-emerald-500': service.state === 'ready',
                    'bg-amber-500': service.state === 'working' || service.state === 'degraded',
                    'bg-destructive': service.state === 'failed',
                    'bg-muted-foreground': service.state === 'stopped' || service.state === 'unavailable',
                  }"
                  aria-hidden="true"
                />
                <ServiceIcon :service="service" inline />{{ service.name }}
              </TabsTrigger>
            </TabsList>

            <TabsContent
              v-for="service in project.services"
              :key="service.id"
              :value="service.id"
              class="m-0 flex min-h-0 flex-1 flex-col"
            >
              <Tabs v-model="selectedServiceSurface" class="flex min-h-0 flex-1 flex-col gap-2">
                <TabsList class="h-11 w-full shrink-0 justify-start gap-5 overflow-x-auto rounded-none border-b bg-transparent px-5 py-0 lg:px-7">
                  <TabsTrigger value="logs" class="h-11 flex-none rounded-none border-x-0 border-t-0 border-b-2 border-transparent bg-transparent px-0 text-muted-foreground shadow-none hover:text-foreground data-[state=active]:!border-primary data-[state=active]:!bg-transparent data-[state=active]:text-foreground data-[state=active]:!shadow-none dark:data-[state=active]:!bg-transparent"><FileText class="size-3.5" aria-hidden="true" />Logs</TabsTrigger>
                  <TabsTrigger value="environment" class="h-11 flex-none rounded-none border-x-0 border-t-0 border-b-2 border-transparent bg-transparent px-0 text-muted-foreground shadow-none hover:text-foreground data-[state=active]:!border-primary data-[state=active]:!bg-transparent data-[state=active]:text-foreground data-[state=active]:!shadow-none dark:data-[state=active]:!bg-transparent"><FileKey class="size-3.5" aria-hidden="true" />Environment</TabsTrigger>
                  <TabsTrigger value="ports" class="h-11 flex-none rounded-none border-x-0 border-t-0 border-b-2 border-transparent bg-transparent px-0 text-muted-foreground shadow-none hover:text-foreground data-[state=active]:!border-primary data-[state=active]:!bg-transparent data-[state=active]:text-foreground data-[state=active]:!shadow-none dark:data-[state=active]:!bg-transparent"><Network class="size-3.5" aria-hidden="true" />Ports <span class="text-xs tabular-nums text-muted-foreground">{{ selectedServicePorts.length }}</span></TabsTrigger>
                </TabsList>

                <TabsContent value="logs" class="m-0 flex min-h-0 flex-1 flex-col px-5 lg:px-7">
                  <ServiceLogsPanel
                    v-if="service.owner === 'compose'"
                    :project-id="project.id"
                    :service-id="service.id"
                    :service-name="service.name"
                    fill
                  />
                  <Empty v-else class="min-h-0 flex-1 rounded-lg border">
                    <EmptyHeader><EmptyTitle>External service</EmptyTitle><EmptyDescription>Logs for this service are managed outside Harbor.</EmptyDescription></EmptyHeader>
                  </Empty>
                </TabsContent>

                <TabsContent value="environment" class="m-0 flex min-h-0 flex-1 flex-col px-5 lg:px-7">
                  <Empty class="min-h-0 flex-1 rounded-lg border">
                    <EmptyHeader><EmptyTitle>Environment is not reported</EmptyTitle><EmptyDescription>Harbor will only show a redacted environment after it has a reviewed container-inspection contract.</EmptyDescription></EmptyHeader>
                  </Empty>
                </TabsContent>

                <TabsContent value="ports" class="m-0 flex min-h-0 flex-1 flex-col px-5 lg:px-7">
                  <Card v-if="selectedServicePorts.length" class="gap-0 rounded-lg py-0 shadow-none">
                    <CardHeader class="border-b px-4 py-3"><CardTitle class="text-sm">Exposed ports</CardTitle><p class="text-xs text-muted-foreground">Current Docker port mappings for this service</p></CardHeader>
                    <CardContent class="divide-y p-0">
                      <div v-for="port in selectedServicePorts" :key="`${port.replica}-${port.address ?? ''}-${port.private}-${port.public ?? 0}-${port.protocol}`" class="flex items-center gap-3 px-4 py-3"><div class="min-w-0 flex-1"><p class="text-sm font-medium">{{ port.protocol.toUpperCase() }} {{ port.private }}<span v-if="port.public"> → {{ port.public }}</span></p><p class="truncate text-xs text-muted-foreground">{{ port.address || 'container network' }} · replica {{ port.replica }}</p></div></div>
                    </CardContent>
                  </Card>
                  <Empty v-else class="min-h-0 flex-1 rounded-lg border">
                    <EmptyHeader><EmptyTitle>No exposed ports</EmptyTitle><EmptyDescription>{{ selectedServicePortsError || 'This service does not currently report any Docker port mappings.' }}</EmptyDescription></EmptyHeader>
                  </Empty>
                </TabsContent>
              </Tabs>
            </TabsContent>
          </Tabs>
          <Empty v-else class="min-h-64 rounded-lg border">
            <EmptyHeader><EmptyTitle>No services are reported</EmptyTitle><EmptyDescription>Harbor will show project services here when the development environment starts.</EmptyDescription></EmptyHeader>
          </Empty>
        </TabsContent>

        <TabsContent value="resources" class="m-0">
          <Card class="gap-0 rounded-lg py-0 shadow-none">
          <CardHeader class="border-b px-4 py-3"><CardTitle class="text-sm">Resources</CardTitle><p class="text-xs text-muted-foreground">Launchable HTTP resources reported by the daemon</p></CardHeader>
          <CardContent class="p-0">
            <div v-if="project.resources.length" class="divide-y">
              <button v-for="resource in project.resources" :key="resource.id" type="button" class="group flex w-full min-w-0 items-center gap-3 px-4 py-3 text-left hover:bg-muted/50" @click="openResource(resource.id)">
                <ResourceFavicon :kind="resource.kind" :name="resource.name" :url="resource.url" :project-id="project.id" :resource-id="resource.id" />
                <div class="min-w-0 flex-1"><p class="truncate text-sm font-medium">{{ resource.name }}</p><p class="truncate text-xs text-muted-foreground">{{ resource.kind }} · {{ resource.owner.kind }} · {{ resource.url }}</p></div>
                <ExternalLink class="size-3.5 text-muted-foreground" />
              </button>
            </div>
            <p v-else class="px-4 py-8 text-center text-sm text-muted-foreground">No resources are reported.</p>
          </CardContent>
          </Card>
        </TabsContent>
        </div>
      </Tabs>
    </template>

    <Empty v-else class="min-h-full border-0">
      <EmptyHeader><EmptyTitle id="project-empty-title">{{ store.loading ? 'Loading project…' : projectId ? 'Project not found' : 'Select a project' }}</EmptyTitle><EmptyDescription>{{ projectId ? 'The current Harbor snapshot does not contain this project.' : 'Choose a registered project to inspect its reported state.' }}</EmptyDescription></EmptyHeader>
      <EmptyContent v-if="projectId && !store.loading"><Button variant="outline" as-child><RouterLink to="/projects"><ArrowLeft class="size-4" />Back to projects</RouterLink></Button></EmptyContent>
    </Empty>
  </main>
</template>
