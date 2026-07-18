<script setup lang="ts">
import { computed } from 'vue'
import { RouterLink } from 'vue-router'
import {
  Activity,
  ArrowUpRight,
  Boxes,
  Clock3,
  FolderKanban,
  Globe2,
  RefreshCw,
  Server,
  TriangleAlert,
} from '@lucide/vue'
import LogStream from '@/components/harbor/LogStream.vue'
import StatusBadge from '@/components/harbor/StatusBadge.vue'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Card, CardContent, CardHeader, CardTitle } from '@/components/ui/card'
import { Separator } from '@/components/ui/separator'
import type { LogLine } from '@/domain/harbor'
import { useHarborStore } from '@/stores/harbor'

const store = useHarborStore()

const daemon = computed(() => store.system.find((check) => check.id === 'daemon'))
const attentionServices = computed(() =>
  store.services.filter((service) => service.status === 'failed' || service.status === 'degraded'),
)
const recentProjects = computed(() =>
  [...store.projects]
    .sort((left, right) => Number(right.favorite) - Number(left.favorite))
    .slice(0, 4),
)
const resourceCount = computed(() => store.snapshot?.recentResources.length ?? 0)
const capturedAt = computed(() => {
  if (!store.snapshot?.capturedAt) {
    return 'Waiting for daemon'
  }

  return new Intl.DateTimeFormat(undefined, {
    hour: 'numeric',
    minute: '2-digit',
    second: '2-digit',
  }).format(new Date(store.snapshot.capturedAt))
})
const recentLogs = computed<LogLine[]>(() => {
  const entries = store.projects.flatMap((project) =>
    project.logs.map((line) => ({ line, projectName: project.name })),
  )

  return entries
    .sort((left, right) => right.line.timestamp.localeCompare(left.line.timestamp))
    .slice(0, 60)
    .map(({ line, projectName }, index) => ({
      ...line,
      id: index + 1,
      source: `${projectName}/${line.source}`,
    }))
})

async function openResource(resourceId: string) {
  await store.openResource(resourceId)
}
</script>

<template>
  <main class="h-full min-w-0 overflow-y-auto" aria-labelledby="overview-title">
    <header class="flex min-h-16 items-center justify-between gap-4 border-b px-5 py-3 lg:px-7">
      <div class="min-w-0">
        <div class="flex items-center gap-2">
          <h1 id="overview-title" class="truncate text-base font-semibold tracking-tight">
            Overview
          </h1>
          <StatusBadge
            v-if="daemon"
            :status="daemon.status"
          />
        </div>
        <p class="mt-0.5 flex items-center gap-1.5 text-xs text-muted-foreground">
          <Clock3 class="size-3" aria-hidden="true" />
          Snapshot {{ capturedAt }}
        </p>
      </div>
      <Button
        variant="outline"
        size="sm"
        :disabled="store.loading"
        aria-label="Refresh Harbor status"
        @click="store.refresh"
      >
        <RefreshCw :class="['size-3.5', store.loading && 'animate-spin']" aria-hidden="true" />
        Recheck
      </Button>
    </header>

    <div class="space-y-5 p-5 lg:p-7">
      <div
        v-if="store.error"
        class="flex items-start gap-3 rounded-lg border border-destructive/35 bg-destructive/10 px-4 py-3 text-sm"
        role="alert"
      >
        <TriangleAlert class="mt-0.5 size-4 shrink-0 text-destructive" aria-hidden="true" />
        <div>
          <p class="font-medium">Harbor state is unavailable</p>
          <p class="mt-0.5 text-xs text-muted-foreground">{{ store.error }}</p>
        </div>
      </div>

      <section aria-label="Harbor summary" class="grid grid-cols-2 overflow-hidden rounded-lg border lg:grid-cols-4">
        <div class="border-b p-4 lg:border-r lg:border-b-0">
          <div class="flex items-center gap-2 text-xs font-medium text-muted-foreground">
            <FolderKanban class="size-3.5" aria-hidden="true" />
            Projects
          </div>
          <div class="mt-2 flex items-end justify-between gap-2">
            <span class="text-2xl font-semibold tabular-nums">{{ store.projects.length }}</span>
            <span class="text-xs text-muted-foreground">{{ store.runningCount }} active</span>
          </div>
        </div>
        <div class="border-b border-l p-4 lg:border-r lg:border-b-0 lg:border-l-0">
          <div class="flex items-center gap-2 text-xs font-medium text-muted-foreground">
            <Server class="size-3.5" aria-hidden="true" />
            Services
          </div>
          <div class="mt-2 flex items-end justify-between gap-2">
            <span class="text-2xl font-semibold tabular-nums">{{ store.services.length }}</span>
            <span class="text-xs text-muted-foreground">{{ attentionServices.length }} need attention</span>
          </div>
        </div>
        <div class="p-4 lg:border-r">
          <div class="flex items-center gap-2 text-xs font-medium text-muted-foreground">
            <Boxes class="size-3.5" aria-hidden="true" />
            Resources
          </div>
          <div class="mt-2 flex items-end justify-between gap-2">
            <span class="text-2xl font-semibold tabular-nums">{{ resourceCount }}</span>
            <span class="text-xs text-muted-foreground">quick links</span>
          </div>
        </div>
        <div class="border-l p-4 lg:border-l-0">
          <div class="flex items-center gap-2 text-xs font-medium text-muted-foreground">
            <Activity class="size-3.5" aria-hidden="true" />
            System
          </div>
          <div class="mt-2 flex items-end justify-between gap-2">
            <span class="text-2xl font-semibold tabular-nums">{{ store.system.length }}</span>
            <span class="text-xs text-muted-foreground">checks observed</span>
          </div>
        </div>
      </section>

      <div class="grid min-w-0 gap-5 xl:grid-cols-[minmax(0,1.35fr)_minmax(18rem,0.65fr)]">
        <Card class="gap-0 rounded-lg py-0 shadow-none">
          <CardHeader class="flex-row items-center justify-between gap-4 border-b px-4 py-3">
            <div>
              <CardTitle class="text-sm">Projects</CardTitle>
              <p class="mt-0.5 text-xs text-muted-foreground">Current project health and activity</p>
            </div>
            <Button variant="ghost" size="sm" as-child>
              <RouterLink to="/projects">
                View all
                <ArrowUpRight class="size-3.5" aria-hidden="true" />
              </RouterLink>
            </Button>
          </CardHeader>
          <CardContent class="p-0">
            <div v-if="recentProjects.length" class="divide-y">
              <RouterLink
                v-for="project in recentProjects"
                :key="project.id"
                :to="`/projects/${encodeURIComponent(project.id)}`"
                class="group flex min-w-0 items-center gap-3 px-4 py-3 transition-colors hover:bg-muted/50 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring focus-visible:ring-inset"
              >
                <StatusBadge :status="project.status" />
                <div class="min-w-0 flex-1">
                  <div class="flex min-w-0 items-center gap-2">
                    <span class="truncate text-sm font-medium">{{ project.name }}</span>
                    <Badge v-if="project.favorite" variant="secondary" class="h-5 px-1.5 text-[10px]">Pinned</Badge>
                  </div>
                  <p class="truncate text-xs text-muted-foreground">{{ project.domain }}</p>
                </div>
                <span class="hidden shrink-0 text-xs text-muted-foreground sm:block">{{ project.updatedAt }}</span>
                <ArrowUpRight class="size-3.5 shrink-0 text-muted-foreground transition-colors group-hover:text-foreground" aria-hidden="true" />
              </RouterLink>
            </div>
            <p v-else class="px-4 py-8 text-center text-sm text-muted-foreground">
              No projects are registered yet.
            </p>
          </CardContent>
        </Card>

        <Card class="gap-0 rounded-lg py-0 shadow-none">
          <CardHeader class="border-b px-4 py-3">
            <CardTitle class="text-sm">System</CardTitle>
            <p class="text-xs text-muted-foreground">Host capabilities Harbor depends on</p>
          </CardHeader>
          <CardContent class="p-0">
            <div v-if="store.system.length" class="divide-y">
              <div v-for="check in store.system" :key="check.id" class="flex items-start gap-3 px-4 py-3">
                <StatusBadge :status="check.status" />
                <div class="min-w-0 flex-1">
                  <p class="text-sm font-medium">{{ check.name }}</p>
                  <p class="truncate text-xs text-muted-foreground">{{ check.detail }}</p>
                </div>
              </div>
            </div>
            <p v-else class="px-4 py-8 text-center text-sm text-muted-foreground">
              Waiting for system checks.
            </p>
            <Separator />
            <div class="p-2">
              <Button variant="ghost" size="sm" class="w-full justify-between" as-child>
                <RouterLink to="/system">
                  Open system details
                  <ArrowUpRight class="size-3.5" aria-hidden="true" />
                </RouterLink>
              </Button>
            </div>
          </CardContent>
        </Card>
      </div>

      <Card v-if="store.snapshot?.recentResources.length" class="gap-0 rounded-lg py-0 shadow-none">
        <CardHeader class="border-b px-4 py-3">
          <CardTitle class="text-sm">Recent resources</CardTitle>
          <p class="text-xs text-muted-foreground">Open project tools without hunting for ports</p>
        </CardHeader>
        <CardContent class="grid p-0 sm:grid-cols-2 xl:grid-cols-4">
          <button
            v-for="(resource, index) in store.snapshot.recentResources"
            :key="resource.id"
            type="button"
            :class="[
              'group flex min-w-0 items-center gap-3 px-4 py-3 text-left transition-colors hover:bg-muted/50 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring focus-visible:ring-inset',
              index > 0 && 'border-t sm:border-t-0 sm:border-l',
              index > 1 && 'sm:border-t xl:border-t-0',
            ]"
            :aria-label="`Open ${resource.name} for ${resource.projectName}`"
            @click="openResource(resource.id)"
          >
            <span class="flex size-8 shrink-0 items-center justify-center rounded-md border bg-background">
              <Globe2 class="size-3.5 text-muted-foreground" aria-hidden="true" />
            </span>
            <span class="min-w-0 flex-1">
              <span class="block truncate text-sm font-medium">{{ resource.name }}</span>
              <span class="block truncate text-xs text-muted-foreground">{{ resource.projectName }}</span>
            </span>
            <ArrowUpRight class="size-3.5 shrink-0 text-muted-foreground group-hover:text-foreground" aria-hidden="true" />
          </button>
        </CardContent>
      </Card>

      <Card class="min-w-0 gap-0 rounded-lg py-0 shadow-none">
        <CardHeader class="border-b px-4 py-3">
          <CardTitle class="text-sm">Recent output</CardTitle>
          <p class="text-xs text-muted-foreground">Latest lines across active projects</p>
        </CardHeader>
        <CardContent class="h-64 min-w-0 p-0">
          <LogStream
            :lines="recentLogs"
            :follow="false"
            empty-message="No project output is available."
            aria-label="Recent output across Harbor projects"
          />
        </CardContent>
      </Card>
    </div>
  </main>
</template>
