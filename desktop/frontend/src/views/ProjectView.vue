<script setup lang="ts">
import { computed, ref } from 'vue'
import { RouterLink, useRoute } from 'vue-router'
import {
  ArrowLeft,
  ArrowUpRight,
  Boxes,
  Check,
  Clipboard,
  ExternalLink,
  FolderOpen,
  Globe2,
  LockKeyhole,
  Server,
  SquareTerminal,
} from '@lucide/vue'
import LogStream from '@/components/harbor/LogStream.vue'
import StatusBadge from '@/components/harbor/StatusBadge.vue'
import { copyText } from '@/bridge/clipboard'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Card, CardContent, CardHeader, CardTitle } from '@/components/ui/card'
import { Empty, EmptyContent, EmptyDescription, EmptyHeader, EmptyTitle } from '@/components/ui/empty'
import { Separator } from '@/components/ui/separator'
import { useHarborStore } from '@/stores/harbor'

const route = useRoute()
const store = useHarborStore()
const copied = ref<'domain' | 'path' | null>(null)

const projectId = computed(() => {
  const value = route.params.projectId
  return Array.isArray(value) ? value[0] : value
})
const project = computed(() => (projectId.value ? store.projectById(projectId.value) : undefined))
const primaryResource = computed(() =>
  project.value?.resources.find((resource) => resource.kind === 'application'),
)
const tlsEnabled = computed(() => project.value?.domain.startsWith('https://') ?? false)
const problemCount = computed(() => {
  if (!project.value) {
    return 0
  }

  return [...project.value.apps, ...project.value.services].filter(
    (entry) => entry.status === 'failed' || entry.status === 'degraded',
  ).length
})

async function copyValue(value: string, target: 'domain' | 'path') {
  await copyText(value)
  copied.value = target
  window.setTimeout(() => {
    if (copied.value === target) {
      copied.value = null
    }
  }, 1600)
}

async function openResource(resourceId: string) {
  await store.openResource(resourceId)
}

async function openPrimaryResource() {
  if (!primaryResource.value) {
    return
  }

  await openResource(primaryResource.value.id)
}
</script>

<template>
  <main class="h-full min-w-0 overflow-y-auto" :aria-labelledby="project ? 'project-title' : 'project-empty-title'">
    <template v-if="project">
      <header class="border-b px-5 py-4 lg:px-7">
        <div class="flex min-w-0 flex-wrap items-start justify-between gap-3">
          <div class="flex min-w-0 items-start gap-2">
            <Button variant="ghost" size="icon-sm" class="-ml-2 shrink-0 min-[1100px]:hidden" as-child>
              <RouterLink to="/projects" aria-label="Back to projects">
                <ArrowLeft class="size-4" aria-hidden="true" />
              </RouterLink>
            </Button>
            <div class="min-w-0">
              <div class="flex min-w-0 items-center gap-2">
                <h1 id="project-title" class="truncate text-base font-semibold tracking-tight">
                  {{ project.name }}
                </h1>
                <StatusBadge :status="project.status" />
              </div>
              <p class="mt-1 truncate text-xs text-muted-foreground">{{ project.path }}</p>
            </div>
          </div>
          <div class="flex items-center gap-2">
            <Button
              size="sm"
              :disabled="!primaryResource"
              :title="primaryResource ? `Open ${primaryResource.name}` : 'No application resource is available'"
              @click="openPrimaryResource"
            >
              Open app
              <ExternalLink class="size-3.5" aria-hidden="true" />
            </Button>
          </div>
        </div>

        <div class="mt-4 flex min-w-0 items-center rounded-lg border bg-muted/35 p-1">
          <LockKeyhole
            class="ml-2 size-3.5 shrink-0 text-muted-foreground"
            aria-hidden="true"
          />
          <span class="min-w-0 flex-1 truncate px-2 font-mono text-xs">{{ project.domain }}</span>
          <Button
            variant="ghost"
            size="icon-sm"
            :aria-label="`Copy ${project.domain}`"
            @click="copyValue(project.domain, 'domain')"
          >
            <Check v-if="copied === 'domain'" class="size-3.5" aria-hidden="true" />
            <Clipboard v-else class="size-3.5" aria-hidden="true" />
          </Button>
          <Button
            variant="ghost"
            size="icon-sm"
            :disabled="!primaryResource"
            :aria-label="primaryResource ? `Open ${project.domain}` : 'No application resource is available'"
            @click="openPrimaryResource"
          >
            <ArrowUpRight class="size-3.5" aria-hidden="true" />
          </Button>
        </div>

        <div class="mt-2 flex flex-wrap items-center gap-x-4 gap-y-2 text-xs text-muted-foreground">
          <span class="inline-flex items-center gap-1.5">
            <LockKeyhole class="size-3.5" aria-hidden="true" />
            {{ tlsEnabled ? 'HTTPS endpoint' : 'HTTP endpoint' }}
          </span>
          <button
            type="button"
            class="inline-flex min-w-0 items-center gap-1.5 rounded-sm hover:text-foreground focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
            :aria-label="`Copy project path ${project.path}`"
            @click="copyValue(project.path, 'path')"
          >
            <Check v-if="copied === 'path'" class="size-3.5" aria-hidden="true" />
            <FolderOpen v-else class="size-3.5" aria-hidden="true" />
            <span class="max-w-80 truncate">{{ copied === 'path' ? 'Path copied' : project.path }}</span>
          </button>
          <span>{{ project.updatedAt }}</span>
        </div>
      </header>

      <div class="space-y-5 p-5 lg:p-7">
        <section aria-label="Project summary" class="grid overflow-hidden rounded-lg border sm:grid-cols-3">
          <div class="p-4 sm:border-r">
            <p class="text-xs font-medium text-muted-foreground">Apps</p>
            <p class="mt-1 text-xl font-semibold tabular-nums">{{ project.apps.length }}</p>
          </div>
          <div class="border-t p-4 sm:border-t-0 sm:border-r">
            <p class="text-xs font-medium text-muted-foreground">Services</p>
            <p class="mt-1 text-xl font-semibold tabular-nums">{{ project.services.length }}</p>
          </div>
          <div class="border-t p-4 sm:border-t-0">
            <p class="text-xs font-medium text-muted-foreground">Needs attention</p>
            <p :class="['mt-1 text-xl font-semibold tabular-nums', problemCount > 0 && 'text-destructive']">
              {{ problemCount }}
            </p>
          </div>
        </section>

        <div class="grid min-w-0 gap-5 xl:grid-cols-2">
          <Card class="gap-0 rounded-lg py-0 shadow-none">
            <CardHeader class="border-b px-4 py-3">
              <div class="flex items-center gap-2">
                <SquareTerminal class="size-4 text-muted-foreground" aria-hidden="true" />
                <CardTitle class="text-sm">Apps</CardTitle>
              </div>
              <p class="text-xs text-muted-foreground">Processes declared by this project</p>
            </CardHeader>
            <CardContent class="p-0">
              <div v-if="project.apps.length" class="divide-y">
                <div v-for="app in project.apps" :key="app.id" class="flex items-center gap-3 px-4 py-3">
                  <StatusBadge :status="app.status" />
                  <div class="min-w-0 flex-1">
                    <p class="text-sm font-medium">{{ app.name }}</p>
                    <code class="mt-1 block w-fit max-w-full truncate text-[11px]">{{ app.command }}</code>
                  </div>
                </div>
              </div>
              <p v-else class="px-4 py-8 text-center text-sm text-muted-foreground">No apps are declared.</p>
            </CardContent>
          </Card>

          <Card class="gap-0 rounded-lg py-0 shadow-none">
            <CardHeader class="border-b px-4 py-3">
              <div class="flex items-center gap-2">
                <Server class="size-4 text-muted-foreground" aria-hidden="true" />
                <CardTitle class="text-sm">Services</CardTitle>
              </div>
              <p class="text-xs text-muted-foreground">Project-owned infrastructure and native endpoints</p>
            </CardHeader>
            <CardContent class="p-0">
              <div v-if="project.services.length" class="divide-y">
                <RouterLink
                  v-for="service in project.services"
                  :key="service.id"
                  :to="`/services/${encodeURIComponent(service.id)}`"
                  class="group flex min-w-0 items-center gap-3 px-4 py-3 transition-colors hover:bg-muted/50 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring focus-visible:ring-inset"
                >
                  <StatusBadge :status="service.status" />
                  <div class="min-w-0 flex-1">
                    <div class="flex items-center gap-2">
                      <span class="truncate text-sm font-medium">{{ service.name }}</span>
                      <Badge variant="secondary" class="h-5 px-1.5 text-[10px]">{{ service.owner }}</Badge>
                    </div>
                    <p class="truncate font-mono text-[11px] text-muted-foreground">{{ service.endpoint }}</p>
                  </div>
                  <ArrowUpRight class="size-3.5 shrink-0 text-muted-foreground group-hover:text-foreground" aria-hidden="true" />
                </RouterLink>
              </div>
              <p v-else class="px-4 py-8 text-center text-sm text-muted-foreground">No services are attached.</p>
            </CardContent>
          </Card>
        </div>

        <Card v-if="project.resources.length" class="gap-0 rounded-lg py-0 shadow-none">
          <CardHeader class="border-b px-4 py-3">
            <div class="flex items-center gap-2">
              <Globe2 class="size-4 text-muted-foreground" aria-hidden="true" />
              <CardTitle class="text-sm">Resources</CardTitle>
            </div>
            <p class="text-xs text-muted-foreground">Application and development-tool entry points</p>
          </CardHeader>
          <CardContent class="grid p-0 md:grid-cols-2">
            <button
              v-for="(resource, index) in project.resources"
              :key="resource.id"
              type="button"
              :class="[
                'group flex min-w-0 items-center gap-3 px-4 py-3 text-left transition-colors hover:bg-muted/50 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring focus-visible:ring-inset',
                index > 0 && 'border-t md:border-t-0 md:border-l',
                index > 1 && 'md:border-t',
              ]"
              :aria-label="`Open ${resource.name}`"
              @click="openResource(resource.id)"
            >
              <span class="flex size-8 shrink-0 items-center justify-center rounded-md border bg-background">
                <Boxes class="size-3.5 text-muted-foreground" aria-hidden="true" />
              </span>
              <span class="min-w-0 flex-1">
                <span class="block truncate text-sm font-medium">{{ resource.name }}</span>
                <span class="block truncate text-xs text-muted-foreground">{{ resource.url }}</span>
              </span>
              <ExternalLink class="size-3.5 shrink-0 text-muted-foreground group-hover:text-foreground" aria-hidden="true" />
            </button>
          </CardContent>
        </Card>

        <Card class="min-w-0 gap-0 rounded-lg py-0 shadow-none">
          <CardHeader class="flex-row items-center justify-between gap-4 border-b px-4 py-3">
            <div>
              <CardTitle class="text-sm">Project output</CardTitle>
              <p class="mt-0.5 text-xs text-muted-foreground">Combined App, watcher, and service stream</p>
            </div>
            <Badge variant="outline" class="font-mono text-[10px]">{{ project.logs.length }} lines</Badge>
          </CardHeader>
          <CardContent class="h-80 min-w-0 p-0">
            <LogStream
              :lines="project.logs"
              empty-message="This project has not emitted any output."
              :aria-label="`${project.name} project output`"
            />
          </CardContent>
        </Card>

        <Separator />
        <p class="flex items-center gap-2 text-xs text-muted-foreground">
          <StatusBadge :status="project.status" />
          Harbor derives this view from the project's resolved GoForj runtime state.
        </p>
      </div>
    </template>

    <Empty v-else class="min-h-full border-0">
      <EmptyHeader>
        <EmptyTitle id="project-empty-title">
          {{ store.loading ? 'Loading project…' : projectId ? 'Project not found' : 'Select a project' }}
        </EmptyTitle>
        <EmptyDescription>
          {{ projectId ? 'Harbor does not have a registered project with this identifier.' : 'Choose a project from the list to inspect its Apps, services, resources, and output.' }}
        </EmptyDescription>
      </EmptyHeader>
      <EmptyContent v-if="projectId && !store.loading">
        <Button variant="outline" as-child>
          <RouterLink to="/projects">
            <ArrowLeft class="size-4" aria-hidden="true" />
            Back to projects
          </RouterLink>
        </Button>
      </EmptyContent>
    </Empty>
  </main>
</template>
