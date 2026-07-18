<script setup lang="ts">
import type { Component } from 'vue'
import { computed, ref, watch } from 'vue'
import { useRoute } from 'vue-router'
import {
  Activity,
  AppWindow,
  BookOpen,
  Box,
  Circle,
  Database,
  ExternalLink,
  Folder,
  Gauge,
  Mail,
  RefreshCw,
  Search,
  Server,
  Star,
  TriangleAlert,
} from '@lucide/vue'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { ScrollArea } from '@/components/ui/scroll-area'
import { Separator } from '@/components/ui/separator'
import type { ResourceSummary, ServiceSummary } from '@/domain/harbor'
import { useHarborStore } from '@/stores/harbor'
import EntityRow from './EntityRow.vue'
import { destinationFromPath, harborNavigation } from './navigation'

const route = useRoute()
const store = useHarborStore()
const query = ref('')

const destination = computed(() => destinationFromPath(route.path))
const navigationItem = computed(() => harborNavigation.find((item) => item.destination === destination.value)!)
const selectedId = computed(() => {
  if (destination.value === 'projects') {
    return route.params.projectId
  }
  if (destination.value === 'services') {
    return route.params.serviceId
  }
  if (destination.value === 'system') {
    return route.params.section ?? route.params.checkId
  }
  return undefined
})
const normalizedSelectedId = computed(() => Array.isArray(selectedId.value) ? selectedId.value[0] : selectedId.value)
const normalizedQuery = computed(() => query.value.trim().toLocaleLowerCase())

const projects = computed(() => {
  const needle = normalizedQuery.value
  return [...store.projects]
    .filter((project) => !needle || [project.name, project.path, project.domain].some((value) => value.toLocaleLowerCase().includes(needle)))
    .sort((left, right) => Number(right.favorite) - Number(left.favorite) || left.name.localeCompare(right.name))
})
const favoriteProjects = computed(() => projects.value.filter((project) => project.favorite))
const attentionProjects = computed(() => projects.value.filter((project) => ['degraded', 'failed', 'unavailable'].includes(project.status)))
const runningProjects = computed(() => projects.value.filter((project) => ['ready', 'working'].includes(project.status)))
const stoppedProjects = computed(() => projects.value.filter((project) => project.status === 'stopped'))

const services = computed(() => {
  const needle = normalizedQuery.value
  return [...store.services]
    .filter((service) => !needle || [service.name, service.projectName, service.endpoint, service.kind].some((value) => value.toLocaleLowerCase().includes(needle)))
    .sort((left, right) => left.projectName.localeCompare(right.projectName) || left.name.localeCompare(right.name))
})

const checks = computed(() => {
  const needle = normalizedQuery.value
  return store.system.filter((check) => !needle || [check.name, check.detail].some((value) => value.toLocaleLowerCase().includes(needle)))
})

const recentResources = computed(() => {
  const needle = normalizedQuery.value
  return (store.snapshot?.recentResources ?? []).filter((resource) => !needle || [resource.name, resource.projectName, resource.url].some((value) => value.toLocaleLowerCase().includes(needle)))
})

const overviewProjects = computed(() => {
  if (normalizedQuery.value) {
    return projects.value
  }
  return favoriteProjects.value.length > 0 ? favoriteProjects.value : projects.value.slice(0, 4)
})

const resultCount = computed(() => {
  switch (destination.value) {
    case 'projects':
      return projects.value.length
    case 'services':
      return services.value.length
    case 'system':
      return checks.value.length
    case 'overview':
      return overviewProjects.value.length + recentResources.value.length
  }
})

const emptyMessage = computed(() => normalizedQuery.value ? 'No matching items' : `No ${destination.value} yet`)

watch(destination, () => {
  query.value = ''
})

function serviceIcon(service: ServiceSummary): Component {
  switch (service.kind) {
    case 'database':
      return Database
    case 'mail':
      return Mail
    case 'observability':
      return Activity
    case 'cache':
      return Box
  }
}

function resourceIcon(resource: ResourceSummary): Component {
  switch (resource.kind) {
    case 'application':
      return AppWindow
    case 'api-reference':
      return BookOpen
    case 'lighthouse':
      return Gauge
    case 'mail':
      return Mail
    case 'observability':
      return Activity
  }
}

function openResource(resourceId: string) {
  void store.openResource(resourceId)
}

function refresh() {
  void store.refresh()
}
</script>

<template>
  <aside class="flex h-full w-full min-w-0 flex-col bg-card/35" :aria-label="`${navigationItem.label} browser`">
    <header class="flex h-12 shrink-0 items-center gap-2 px-3">
      <component :is="navigationItem.icon" aria-hidden="true" class="size-4 text-muted-foreground" />
      <h1 class="min-w-0 flex-1 truncate text-sm font-semibold">{{ navigationItem.label }}</h1>
      <Badge variant="outline" class="h-5 min-w-5 rounded-md px-1.5 text-[0.6875rem] text-muted-foreground shadow-none">
        {{ resultCount }}
      </Badge>
      <Button
        variant="ghost"
        size="icon-sm"
        :disabled="store.loading"
        aria-label="Refresh Harbor state"
        class="size-7 text-muted-foreground hover:text-foreground"
        @click="refresh"
      >
        <RefreshCw aria-hidden="true" :class="['size-3.5', store.loading && 'animate-spin']" />
      </Button>
    </header>

    <div class="relative px-3 pb-3">
      <Search aria-hidden="true" class="pointer-events-none absolute top-2.5 left-5.5 size-3.5 text-muted-foreground" />
      <Input
        :model-value="query"
        type="search"
        :placeholder="`Search ${navigationItem.label.toLocaleLowerCase()}`"
        :aria-label="`Search ${navigationItem.label.toLocaleLowerCase()}`"
        class="h-8 bg-background pl-8 text-xs shadow-none"
        @update:model-value="query = String($event)"
      />
    </div>

    <Separator />

    <div v-if="store.error" class="m-3 rounded-md border border-destructive/30 bg-destructive/10 p-3 text-xs text-destructive" role="alert">
      {{ store.error }}
    </div>

    <ScrollArea v-else class="min-h-0 flex-1">
      <div class="space-y-4 p-2">
        <template v-if="destination === 'overview'">
          <section v-if="overviewProjects.length" aria-labelledby="overview-projects-heading">
            <h2 id="overview-projects-heading" class="mb-1 flex items-center gap-1.5 px-2 text-[0.6875rem] font-medium tracking-wide text-muted-foreground uppercase">
              <Star aria-hidden="true" class="size-3" />
              {{ normalizedQuery ? 'Projects' : 'Favorites' }}
            </h2>
            <div class="space-y-0.5">
              <EntityRow
                v-for="project in overviewProjects"
                :key="project.id"
                :label="project.name"
                :description="project.updatedAt"
                :status="project.status"
                :to="`/projects/${project.id}`"
              >
                <template #leading><Folder /></template>
              </EntityRow>
            </div>
          </section>

          <section v-if="recentResources.length" aria-labelledby="recent-resources-heading">
            <h2 id="recent-resources-heading" class="mb-1 px-2 text-[0.6875rem] font-medium tracking-wide text-muted-foreground uppercase">
              Recent resources
            </h2>
            <div class="space-y-0.5">
              <EntityRow
                v-for="resource in recentResources"
                :key="resource.id"
                :label="resource.name"
                :description="resource.projectName"
                @activate="openResource(resource.id)"
              >
                <template #leading><component :is="resourceIcon(resource)" /></template>
                <template #trailing><ExternalLink aria-hidden="true" class="size-3.5 text-muted-foreground" /></template>
              </EntityRow>
            </div>
          </section>
        </template>

        <template v-else-if="destination === 'projects'">
          <section v-if="attentionProjects.length" aria-labelledby="attention-projects-heading">
            <h2 id="attention-projects-heading" class="mb-1 flex items-center gap-1.5 px-2 text-[0.6875rem] font-medium tracking-wide text-muted-foreground uppercase">
              <TriangleAlert aria-hidden="true" class="size-3 text-status-failed" />
              Attention · {{ attentionProjects.length }}
            </h2>
            <div class="space-y-0.5">
              <EntityRow
                v-for="project in attentionProjects"
                :key="project.id"
                :label="project.name"
                :description="project.updatedAt"
                :status="project.status"
                :selected="normalizedSelectedId === project.id"
                :to="`/projects/${project.id}`"
              >
                <template #leading><Folder /></template>
              </EntityRow>
            </div>
          </section>

          <section v-if="runningProjects.length" aria-labelledby="running-projects-heading">
            <h2 id="running-projects-heading" class="mb-1 flex items-center gap-1.5 px-2 text-[0.6875rem] font-medium tracking-wide text-muted-foreground uppercase">
              <Activity aria-hidden="true" class="size-3 text-status-ready" />
              Running · {{ runningProjects.length }}
            </h2>
            <div class="space-y-0.5">
              <EntityRow
                v-for="project in runningProjects"
                :key="project.id"
                :label="project.name"
                :description="project.updatedAt"
                :status="project.status"
                :selected="normalizedSelectedId === project.id"
                :to="`/projects/${project.id}`"
              >
                <template #leading><Folder /></template>
              </EntityRow>
            </div>
          </section>

          <section v-if="stoppedProjects.length" aria-labelledby="stopped-projects-heading">
            <h2 id="stopped-projects-heading" class="mb-1 flex items-center gap-1.5 px-2 text-[0.6875rem] font-medium tracking-wide text-muted-foreground uppercase">
              <Circle aria-hidden="true" class="size-3 text-status-stopped" />
              Stopped · {{ stoppedProjects.length }}
            </h2>
            <div class="space-y-0.5">
              <EntityRow
                v-for="project in stoppedProjects"
                :key="project.id"
                :label="project.name"
                :description="project.updatedAt"
                :status="project.status"
                :selected="normalizedSelectedId === project.id"
                :to="`/projects/${project.id}`"
              >
                <template #leading><Folder /></template>
              </EntityRow>
            </div>
          </section>
        </template>

        <section v-else-if="destination === 'services' && services.length" aria-labelledby="services-heading">
          <h2 id="services-heading" class="sr-only">Services</h2>
          <div class="space-y-0.5">
            <EntityRow
              v-for="service in services"
              :key="service.id"
              :label="service.name"
              :description="`${service.projectName} · ${service.endpoint}`"
              :status="service.status"
              :selected="normalizedSelectedId === service.id"
              :to="`/services/${service.id}`"
            >
              <template #leading><component :is="serviceIcon(service)" /></template>
            </EntityRow>
          </div>
        </section>

        <section v-else-if="destination === 'system' && checks.length" aria-labelledby="system-heading">
          <h2 id="system-heading" class="sr-only">System checks</h2>
          <div class="space-y-0.5">
            <EntityRow
              v-for="check in checks"
              :key="check.id"
              :label="check.name"
              :description="check.detail"
              :status="check.status"
              :selected="normalizedSelectedId === check.id"
              :to="`/system/${check.id}`"
            >
              <template #leading><Server /></template>
            </EntityRow>
          </div>
        </section>

        <div
          v-if="resultCount === 0"
          class="flex min-h-40 flex-col items-center justify-center gap-2 px-6 text-center"
        >
          <Search aria-hidden="true" class="size-5 text-muted-foreground" />
          <p class="text-sm font-medium">{{ emptyMessage }}</p>
          <p class="text-xs text-muted-foreground">
            {{ normalizedQuery ? 'Try a different search.' : 'Harbor will show it here when available.' }}
          </p>
        </div>
      </div>
    </ScrollArea>
  </aside>
</template>
