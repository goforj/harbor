<script setup lang="ts">
import { computed, watch } from 'vue'
import { useRouter } from 'vue-router'
import { Check, Compass, ExternalLink, FolderPlus, LoaderCircle, Network, Play, X } from '@lucide/vue'
import { Button } from '@/components/ui/button'
import { useHarborStore } from '@/stores/harbor'
import { showNewProjectDialog } from '@/lib/projectCreationDialog'
import { projectGuideState, selectProjectGuideProject } from '@/lib/projectGuide'
import PrerequisitePanel from './PrerequisitePanel.vue'
import ProjectIcon from './ProjectIcon.vue'

const emit = defineEmits<{ add: []; setup: []; removeOldNetworking: [] }>()
const store = useHarborStore()
const router = useRouter()
const project = computed(() => store.projectById(projectGuideState.value.projectId) ?? store.projects[0])
const fresh = computed(() => store.connectionState === 'connected' && !store.snapshotStale)
const ready = computed(() => project.value?.state === 'ready' || project.value?.state === 'degraded')
const application = computed(() => project.value?.resources.find(resource => resource.kind === 'application'))
const working = computed(() => Boolean(project.value && (store.projectLifecycleBusyFor(project.value.id) || ['starting', 'rebuilding', 'stopping'].includes(project.value.state))))
const progressLabel = computed(() => project.value && store.projectPrerequisiteChecking[project.value.id] ? 'Checking tools…'
  : project.value?.state === 'stopping' ? 'Stopping…' : project.value?.state === 'rebuilding' ? 'Rebuilding…' : 'Starting…')
const canStart = computed(() => fresh.value && project.value && store.networkConfigured && !store.networkSetupRecovery
  && !store.settingUpNetwork && !store.projectRuntimeRepairBusy && !store.projectRemovalApprovalBusy
  && !store.projectLifecycleBlockedFor(project.value.id, 'start')
  && ['stopped', 'failed', 'unavailable'].includes(project.value.state))
const canSetup = computed(() => fresh.value && !store.settingUpNetwork && !store.removingOldNetworking
  && store.daemonStatus?.capabilities.includes('control.network-setup.v1'))
const setupSupported = computed(() => store.daemonStatus?.capabilities.includes('control.network-setup.v1') === true)
const steps = computed(() => [
  { title: 'Choose a project', done: Boolean(project.value), description: project.value ? project.value.name : 'Create a project or add an existing folder.' },
  { title: 'Secure local networking', done: store.networkConfigured && !store.networkSetupRecovery, description: store.networkSetupRecovery ? 'Finish the pending setup using its retained progress.' : store.networkConfigured ? 'Networking is configured. Start verifies the project can use it.' : !store.daemonStatus ? 'Waiting for networking capabilities.' : !setupSupported.value ? 'This background service cannot set up networking. Check its version and connection in Daemon settings.' : 'Set up local domains, HTTPS, and ingress with your approval.' },
  { title: 'Start your project', done: ready.value, description: ready.value ? project.value?.state === 'degraded' ? 'The project is running with warnings. Inspect its services in project details.' : 'The daemon reports this project ready.' : project.value && store.projectPrerequisiteChecking[project.value.id] ? 'Checking the selected tools before Start.' : working.value ? 'A project action is in progress. You can inspect its output.' : 'Check its tools, then let Harbor start its development runtime.' },
  { title: application.value ? 'Open your app' : 'Explore your project', done: projectGuideState.value.appOpened, description: application.value ? 'Open the App URL published by this project.' : 'Project details include output, services, and available resources.' },
])
const activeStep = computed(() => steps.value.findIndex(step => !step.done))

watch(() => project.value?.id, id => selectProjectGuideProject(id ?? ''), { immediate: true })

// viewProject keeps debugging and service actions in the project's existing workspace.
async function viewProject() {
  if (project.value) await router.push(`/projects/${encodeURIComponent(project.value.id)}`)
}

// startProject uses the ordinary guarded store action; the guide owns no daemon or helper authority.
async function startProject() {
  if (canStart.value && project.value) await store.startProject(project.value.id)
}

// startWithDaemon preserves the explicit advisory override available in the ordinary project workspace.
async function startWithDaemon() {
  if (canStart.value && project.value) await store.startProject(project.value.id, { checkPrerequisites: false })
}

// openApp records only a successful native link-open request for the same still-selected project.
async function openApp() {
  const id = project.value?.id
  const resourceId = application.value?.id
  if (!id || !resourceId || !fresh.value || !ready.value) return
  await store.openResource(id, resourceId)
  if (!store.actionError && project.value?.id === id && fresh.value) projectGuideState.value.appOpened = true
}
</script>

<template>
  <section aria-label="Getting started guide" class="overflow-hidden rounded-xl border bg-card text-card-foreground">
    <header class="flex items-start justify-between gap-4 border-b px-5 py-4">
      <div class="flex items-start gap-3"><span class="flex size-9 shrink-0 items-center justify-center rounded-lg bg-primary/10 text-primary"><Compass class="size-5" /></span><div><h2 class="text-sm font-semibold">Get a project running</h2><p class="mt-1 text-xs text-muted-foreground">A clear path from a project folder to your running app.</p></div></div>
      <Button variant="ghost" size="icon-sm" aria-label="Dismiss getting started guide" @click="projectGuideState.visible = false"><X class="size-4" /></Button>
    </header>
    <div v-if="project" class="flex flex-wrap items-center gap-2 border-b px-5 py-3 text-sm"><ProjectIcon :project="project" compact /><span v-if="store.projects.length === 1" class="min-w-0 max-w-full truncate font-medium">{{ project.name }}</span><select v-else :value="project.id" aria-label="Guide project" class="min-w-0 max-w-full rounded-md border bg-background px-2 py-1" @change="selectProjectGuideProject(($event.target as HTMLSelectElement).value)"><option v-for="candidate in store.projects" :key="candidate.id" :value="candidate.id">{{ candidate.name }}</option></select><span class="text-xs text-muted-foreground">{{ steps.filter(step => step.done).length }} of 4 steps complete</span></div>
    <ol class="grid gap-3 p-5 md:grid-cols-2 xl:grid-cols-4">
      <li v-for="(step, index) in steps" :key="step.title" class="flex min-w-0 flex-col items-start rounded-lg border p-4" :class="index === activeStep ? 'border-primary/35 bg-primary/[0.035]' : 'border-border'" :aria-current="index === activeStep ? 'step' : undefined">
        <div class="flex items-center gap-2"><span class="flex size-6 shrink-0 items-center justify-center rounded-full text-xs" :class="step.done ? 'bg-status-ready/10 text-status-ready' : 'bg-muted text-muted-foreground'"><Check v-if="step.done" class="size-3.5" /><span v-else>{{ index + 1 }}</span></span><h3 class="text-xs font-semibold">{{ step.title }}</h3></div>
        <p class="mt-3 flex-1 break-words text-xs leading-relaxed text-muted-foreground">{{ step.description }}</p>
        <div class="mt-4 flex flex-wrap gap-2">
          <template v-if="index === 0"><template v-if="!project"><Button size="sm" class="h-8 text-xs" :disabled="!fresh" @click="showNewProjectDialog"><FolderPlus class="size-3.5" />New project</Button><Button variant="outline" size="sm" class="h-8 text-xs" :disabled="!fresh || store.addingProject" @click="emit('add')">Add existing</Button></template><Button v-else variant="outline" size="sm" class="h-8 text-xs" @click="viewProject">Project details</Button></template>
          <template v-if="index === 1"><Button v-if="!step.done && setupSupported" size="sm" class="h-8 max-w-full px-2 text-xs" :disabled="!canSetup" @click="emit('setup')"><LoaderCircle v-if="store.settingUpNetwork" class="size-3.5 animate-spin" /><Network v-else class="size-3.5" />{{ store.settingUpNetwork ? 'Setting up…' : store.networkSetupRecovery ? 'Resume setup' : 'Set up secure networking' }}</Button><Button v-else-if="!step.done" variant="outline" size="sm" class="h-8 text-xs" @click="router.push('/settings/daemon')">Daemon settings</Button><span v-else class="text-xs text-status-ready">Configured</span></template>
          <template v-if="index === 2"><Button v-if="!ready" size="sm" class="h-8 text-xs" :disabled="!canStart" @click="startProject"><LoaderCircle v-if="working" class="size-3.5 animate-spin" /><Play v-else class="size-3.5" />{{ working ? progressLabel : 'Start project' }}</Button><Button v-if="project" variant="ghost" size="sm" class="h-8 text-xs" @click="viewProject">{{ ready ? 'View project' : 'View output' }}</Button></template>
          <template v-if="index === 3"><Button v-if="application" size="sm" class="h-8 text-xs" :disabled="!fresh || !ready" @click="openApp"><ExternalLink class="size-3.5" />Open app</Button><Button v-else variant="outline" size="sm" class="h-8 text-xs" :disabled="!project" @click="viewProject">View project</Button></template>
        </div>
      </li>
    </ol>
    <div v-if="project && (store.projectPrerequisiteChecking[project.id] || store.projectPrerequisiteErrors[project.id] || store.projectPrerequisites[project.id])" class="px-5 pb-4"><PrerequisitePanel :report="store.projectPrerequisites[project.id]" :checking="store.projectPrerequisiteChecking[project.id]" :error="store.projectPrerequisiteErrors[project.id]" :continue-available="Boolean(canStart)" compact @recheck="store.checkProjectPrerequisites(project!.id)" @continue="startWithDaemon" /></div>
    <div v-if="store.networkSetupError || store.oldNetworkingRemovalError" class="space-y-2 border-t px-5 py-3 text-xs"><p role="alert" class="text-destructive">{{ store.networkSetupError || store.oldNetworkingRemovalError }}</p><Button v-if="store.oldNetworkingRemovalAvailable" size="sm" variant="outline" :disabled="store.oldNetworkingRemovalBlocked" @click="emit('removeOldNetworking')">Remove old networking</Button></div>
    <p v-if="project && store.projectLifecycleErrors[project.id]" role="alert" class="border-t px-5 py-3 text-xs text-destructive">{{ store.projectLifecycleErrors[project.id] }} Open project details for recovery and launch output.</p>
    <p v-if="store.actionError" role="alert" class="border-t px-5 py-3 text-xs text-destructive">{{ store.actionError }}</p>
    <footer class="flex flex-wrap items-center justify-between gap-3 border-t px-5 py-3"><p class="text-xs text-muted-foreground">{{ !fresh ? 'Reconnect to Harbor and wait for a fresh snapshot to continue.' : 'Your project files and service data stay with your project.' }}</p><Button v-if="ready" variant="outline" size="sm" @click="projectGuideState.visible = false">Finish setup</Button></footer>
  </section>
</template>
