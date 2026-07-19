<script setup lang="ts">
import { computed } from 'vue'
import { RouterLink, useRouter } from 'vue-router'
import { toast } from 'vue-sonner'
import {
  Activity,
  ArrowUpRight,
  Boxes,
  CircleCheck,
  Clock3,
  FolderKanban,
  FolderPlus,
  Network,
  RefreshCw,
  Server,
  TriangleAlert,
} from '@lucide/vue'
import StatusBadge from '@/components/harbor/StatusBadge.vue'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Card, CardContent, CardHeader, CardTitle } from '@/components/ui/card'
import { Empty, EmptyContent, EmptyDescription, EmptyHeader, EmptyMedia, EmptyTitle } from '@/components/ui/empty'
import { Spinner } from '@/components/ui/spinner'
import { useHarborStore } from '@/stores/harbor'

const store = useHarborStore()
const router = useRouter()
const recentProjects = computed(() => [...store.projects]
  .sort((left, right) => Number(right.favorite) - Number(left.favorite) || right.updated_at.localeCompare(left.updated_at))
  .slice(0, 4))
const capturedAt = computed(() => store.snapshot?.captured_at
  ? new Intl.DateTimeFormat(undefined, { dateStyle: 'medium', timeStyle: 'medium' }).format(new Date(store.snapshot.captured_at))
  : 'Waiting for a snapshot')

async function openResource(projectId: string, resourceId: string) {
  await store.openResource(projectId, resourceId)
}

async function addProject() {
  const registration = await store.addProject()
  if (!registration) {
    return
  }

  toast.success(registration.created
    ? `${registration.project.name} added`
    : `${registration.project.name} is already in Harbor`, {
    description: registration.created ? 'Stopped; routing is not configured yet.' : undefined,
  })
  await router.push(`/projects/${encodeURIComponent(registration.project.id)}`)
}

async function setupNetwork() {
  const result = await store.setupNetwork()
  if (result) {
    toast.success('Harbor networking is ready', {
      description: 'The local network foundation was verified or completed.',
    })
  }
}
</script>

<template>
  <main class="h-full min-w-0 overflow-y-auto" aria-labelledby="overview-title">
    <header class="flex min-h-16 items-center justify-between gap-4 border-b px-5 py-3 lg:px-7">
      <div class="min-w-0">
        <div class="flex items-center gap-2">
          <h1 id="overview-title" class="truncate text-base font-semibold tracking-tight">Overview</h1>
          <StatusBadge v-if="store.daemonStatus" :status="store.daemonStatus.state" />
        </div>
        <p class="mt-0.5 flex items-center gap-1.5 text-xs text-muted-foreground">
          <Clock3 class="size-3" aria-hidden="true" />
          Snapshot {{ capturedAt }}
        </p>
      </div>
      <div class="flex items-center gap-2">
        <Button size="sm" :disabled="store.addingProject || store.connectionState !== 'connected'" @click="addProject">
          <Spinner v-if="store.addingProject" aria-hidden="true" />
          <FolderPlus v-else class="size-3.5" aria-hidden="true" />
          {{ store.addingProject ? 'Adding…' : 'Add project' }}
        </Button>
        <Button variant="outline" size="sm" :disabled="store.loading" @click="store.refresh">
          <RefreshCw :class="['size-3.5', store.loading && 'animate-spin']" aria-hidden="true" />
          Refresh
        </Button>
      </div>
    </header>

    <div class="space-y-5 p-5 lg:p-7">
      <div v-if="store.error && !store.connectionMessage" class="flex items-start gap-3 rounded-lg border border-destructive/35 bg-destructive/10 px-4 py-3 text-sm" role="alert">
        <TriangleAlert class="mt-0.5 size-4 shrink-0 text-destructive" aria-hidden="true" />
        <div>
          <p class="font-medium">Harbor state is unavailable</p>
          <p class="mt-0.5 text-xs text-muted-foreground">{{ store.error }}</p>
        </div>
      </div>

      <Card v-if="store.networkSetupOnboarding" class="gap-0 rounded-lg py-0 shadow-none">
        <CardHeader class="border-b px-4 py-3">
          <div class="flex items-center gap-2">
            <Network class="size-4 text-muted-foreground" aria-hidden="true" />
            <CardTitle class="text-sm">Local networking</CardTitle>
          </div>
          <p class="text-xs text-muted-foreground">Verify or complete Harbor’s local network foundation. This action is safe to run again.</p>
        </CardHeader>
        <CardContent class="flex flex-col items-start gap-3 px-4 py-4 sm:flex-row sm:items-center sm:justify-between">
          <div class="min-w-0 text-sm">
            <p v-if="store.networkSetupResult" class="flex items-center gap-2 font-medium text-emerald-700 dark:text-emerald-400">
              <CircleCheck class="size-4 shrink-0" aria-hidden="true" />
              Harbor networking is ready.
            </p>
            <template v-else>
              <p class="font-medium">Set up networking before you need project routes.</p>
              <p v-if="store.networkSetupError" class="mt-1 text-xs text-destructive" role="alert">{{ store.networkSetupError }}</p>
            </template>
          </div>
          <Button
            v-if="!store.networkSetupResult"
            class="shrink-0"
            :disabled="store.settingUpNetwork || store.connectionState !== 'connected'"
            @click="setupNetwork"
          >
            <Spinner v-if="store.settingUpNetwork" aria-hidden="true" />
            <Network v-else class="size-4" aria-hidden="true" />
            {{ store.settingUpNetwork ? 'Setting up…' : 'Set up networking' }}
          </Button>
        </CardContent>
      </Card>

      <section aria-label="Harbor snapshot summary" class="grid grid-cols-2 overflow-hidden rounded-lg border lg:grid-cols-4">
        <div class="border-b p-4 lg:border-r lg:border-b-0">
          <div class="flex items-center gap-2 text-xs font-medium text-muted-foreground"><FolderKanban class="size-3.5" />Projects</div>
          <div class="mt-2 flex items-end justify-between gap-2"><span class="text-2xl font-semibold tabular-nums">{{ store.projects.length }}</span><span class="text-xs text-muted-foreground">{{ store.runningCount }} active</span></div>
        </div>
        <div class="border-b border-l p-4 lg:border-r lg:border-b-0 lg:border-l-0">
          <div class="flex items-center gap-2 text-xs font-medium text-muted-foreground"><Server class="size-3.5" />Services</div>
          <div class="mt-2"><span class="text-2xl font-semibold tabular-nums">{{ store.services.length }}</span></div>
        </div>
        <div class="p-4 lg:border-r">
          <div class="flex items-center gap-2 text-xs font-medium text-muted-foreground"><Boxes class="size-3.5" />Resources</div>
          <div class="mt-2"><span class="text-2xl font-semibold tabular-nums">{{ store.resources.length }}</span></div>
        </div>
        <div class="border-l p-4 lg:border-l-0">
          <div class="flex items-center gap-2 text-xs font-medium text-muted-foreground"><Activity class="size-3.5" />Operations</div>
          <div class="mt-2"><span class="text-2xl font-semibold tabular-nums">{{ store.operations.length }}</span></div>
        </div>
      </section>

      <div class="grid min-w-0 gap-5 xl:grid-cols-[minmax(0,1.35fr)_minmax(18rem,0.65fr)]">
        <Card class="gap-0 rounded-lg py-0 shadow-none">
          <CardHeader class="flex items-center justify-between gap-4 border-b px-4 py-3">
            <div><CardTitle class="text-sm">Projects</CardTitle><p class="mt-0.5 text-xs text-muted-foreground">Latest authoritative project state</p></div>
            <Button variant="ghost" size="sm" as-child><RouterLink to="/projects">View all<ArrowUpRight class="size-3.5" /></RouterLink></Button>
          </CardHeader>
          <CardContent class="p-0">
            <div v-if="recentProjects.length" class="divide-y">
              <RouterLink
                v-for="project in recentProjects"
                :key="project.id"
                :to="`/projects/${encodeURIComponent(project.id)}`"
                class="group flex min-w-0 items-center gap-3 px-4 py-3 transition-colors hover:bg-muted/50 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring focus-visible:ring-inset"
              >
                <StatusBadge :status="project.state" />
                <div class="min-w-0 flex-1">
                  <div class="flex items-center gap-2"><span class="truncate text-sm font-medium">{{ project.name }}</span><Badge v-if="project.favorite" variant="secondary">Favorite</Badge></div>
                  <p class="truncate text-xs text-muted-foreground">Slug: {{ project.slug }}</p>
                </div>
                <ArrowUpRight class="size-3.5 shrink-0 text-muted-foreground" />
              </RouterLink>
            </div>
            <Empty v-else class="border-0 py-10">
              <EmptyHeader>
                <EmptyMedia variant="icon"><FolderPlus aria-hidden="true" /></EmptyMedia>
                <EmptyTitle>Add your first project</EmptyTitle>
                <EmptyDescription>Choose a GoForj project folder to keep it available in Harbor.</EmptyDescription>
              </EmptyHeader>
              <EmptyContent>
                <Button :disabled="store.addingProject || store.connectionState !== 'connected'" @click="addProject">
                  <Spinner v-if="store.addingProject" aria-hidden="true" />
                  <FolderPlus v-else class="size-4" aria-hidden="true" />
                  {{ store.addingProject ? 'Adding project…' : 'Choose a project folder' }}
                </Button>
              </EmptyContent>
            </Empty>
          </CardContent>
        </Card>

        <Card class="gap-0 rounded-lg py-0 shadow-none">
          <CardHeader class="border-b px-4 py-3"><CardTitle class="text-sm">Daemon</CardTitle><p class="text-xs text-muted-foreground">Current control connection</p></CardHeader>
          <CardContent class="p-0">
            <dl v-if="store.daemonStatus" class="divide-y text-xs">
              <div class="flex justify-between gap-4 px-4 py-3"><dt class="text-muted-foreground">State</dt><dd><StatusBadge :status="store.daemonStatus.state" /></dd></div>
              <div class="flex justify-between gap-4 px-4 py-3"><dt class="text-muted-foreground">Build</dt><dd class="font-mono">{{ store.daemonStatus.build.version }}</dd></div>
              <div class="flex justify-between gap-4 px-4 py-3"><dt class="text-muted-foreground">Protocol</dt><dd class="font-mono">{{ store.daemonStatus.protocol.major }}.{{ store.daemonStatus.protocol.minor }}</dd></div>
              <div class="flex justify-between gap-4 px-4 py-3"><dt class="text-muted-foreground">Sequence</dt><dd class="font-mono">{{ store.daemonStatus.sequence }}</dd></div>
            </dl>
            <p v-else class="px-4 py-8 text-center text-sm text-muted-foreground">Waiting for daemon status.</p>
          </CardContent>
        </Card>
      </div>

      <Card v-if="store.recentResources.length" class="gap-0 rounded-lg py-0 shadow-none">
        <CardHeader class="border-b px-4 py-3"><CardTitle class="text-sm">Recent resources</CardTitle><p class="text-xs text-muted-foreground">References resolved from the current snapshot</p></CardHeader>
        <CardContent class="grid p-0 sm:grid-cols-2 xl:grid-cols-4">
          <button
            v-for="resource in store.recentResources"
            :key="`${resource.project_id}:${resource.id}`"
            type="button"
            class="group flex min-w-0 items-center gap-3 border-b px-4 py-3 text-left transition-colors hover:bg-muted/50 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring focus-visible:ring-inset"
            :aria-label="`Open ${resource.name} for ${resource.project_name}`"
            @click="openResource(resource.project_id, resource.id)"
          >
            <span class="min-w-0 flex-1"><span class="block truncate text-sm font-medium">{{ resource.name }}</span><span class="block truncate text-xs text-muted-foreground">{{ resource.project_name }} · {{ resource.kind }}</span></span>
            <ArrowUpRight class="size-3.5 shrink-0 text-muted-foreground" />
          </button>
        </CardContent>
      </Card>

      <Card v-if="store.operations.length" class="gap-0 rounded-lg py-0 shadow-none">
        <CardHeader class="border-b px-4 py-3"><CardTitle class="text-sm">Operations</CardTitle><p class="text-xs text-muted-foreground">Durable daemon work in this snapshot</p></CardHeader>
        <CardContent class="divide-y p-0">
          <div v-for="operation in store.operations" :key="operation.id" class="flex items-center gap-3 px-4 py-3">
            <StatusBadge :status="operation.state" />
            <div class="min-w-0 flex-1"><p class="truncate text-sm font-medium">{{ operation.kind }}</p><p class="truncate text-xs text-muted-foreground">{{ operation.phase }}<template v-if="operation.project_id"> · {{ operation.project_id }}</template></p></div>
          </div>
        </CardContent>
      </Card>
    </div>
  </main>
</template>
