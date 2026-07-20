<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { useRoute } from 'vue-router'
import {
  Activity,
  ExternalLink,
  Folder,
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
import { serviceOwnerLabel } from '@/lib/servicePresentation'
import { useHarborStore } from '@/stores/harbor'
import EntityRow from './EntityRow.vue'
import ServiceOwnership from './ServiceOwnership.vue'
import { destinationFromPath, harborNavigation } from './navigation'

const route = useRoute()
const store = useHarborStore()
const query = ref('')

const destination = computed(() => destinationFromPath(route.path))
const navigationItem = computed(() => harborNavigation.find((item) => item.destination === destination.value)!)
const normalizedQuery = computed(() => query.value.trim().toLocaleLowerCase())
const selectedProjectId = computed(() => String(route.params.projectId ?? ''))
const selectedServiceId = computed(() => String(route.params.serviceId ?? ''))

const projects = computed(() => {
  const needle = normalizedQuery.value
  return [...store.projects]
    .filter((project) => !needle || [project.name, project.path, project.slug].some((value) => value.toLocaleLowerCase().includes(needle)))
    .sort((left, right) => Number(right.favorite) - Number(left.favorite) || left.name.localeCompare(right.name))
})
const favoriteProjects = computed(() => projects.value.filter((project) => project.favorite))
const attentionProjects = computed(() => projects.value.filter((project) => ['degraded', 'failed', 'unavailable'].includes(project.state)))
const activeProjects = computed(() => projects.value.filter((project) => ['ready', 'starting', 'rebuilding'].includes(project.state)))
const inactiveProjects = computed(() => projects.value.filter((project) => ['stopped', 'stopping'].includes(project.state)))

const services = computed(() => {
  const needle = normalizedQuery.value
  return [...store.services]
    .filter((service) => !needle || [service.name, service.project_name, service.kind].some((value) => value.toLocaleLowerCase().includes(needle)))
    .sort((left, right) => left.project_name.localeCompare(right.project_name) || left.name.localeCompare(right.name))
})

const recentResources = computed(() => {
  const needle = normalizedQuery.value
  return store.recentResources.filter((resource) => !needle || [resource.name, resource.project_name, resource.kind].some((value) => value.toLocaleLowerCase().includes(needle)))
})

const overviewProjects = computed(() => {
  if (normalizedQuery.value) {
    return projects.value
  }
  return favoriteProjects.value.length > 0 ? favoriteProjects.value : projects.value.slice(0, 4)
})

const resultCount = computed(() => {
  switch (destination.value) {
    case 'projects': return projects.value.length
    case 'services': return services.value.length
    case 'system': return store.daemonStatus ? 1 : 0
    case 'overview': return overviewProjects.value.length + recentResources.value.length
  }
})

const emptyMessage = computed(() => normalizedQuery.value ? 'No matching items' : `No ${destination.value} yet`)

watch(destination, () => {
  query.value = ''
})

function openResource(projectId: string, resourceId: string) {
  void store.openResource(projectId, resourceId)
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
      <Badge v-if="store.snapshot" variant="outline" class="h-5 min-w-5 rounded-md px-1.5 text-[0.6875rem] text-muted-foreground shadow-none">
        {{ resultCount }}
      </Badge>
      <Button
        variant="ghost"
        size="icon-sm"
        :disabled="store.refreshing"
        aria-label="Refresh Harbor state"
        class="size-7 text-muted-foreground hover:text-foreground"
        @click="refresh"
      >
        <RefreshCw aria-hidden="true" :class="['size-3.5', store.refreshing && 'animate-spin']" />
      </Button>
    </header>

    <div class="relative px-3 pb-3">
      <Search aria-hidden="true" class="pointer-events-none absolute top-2.5 left-5.5 size-3.5 text-muted-foreground" />
      <Input
        :model-value="query"
        type="search"
        :placeholder="`Search ${navigationItem.label.toLocaleLowerCase()}`"
        :aria-label="`Search ${navigationItem.label.toLocaleLowerCase()}`"
        :disabled="!store.snapshot"
        class="h-8 bg-background pl-8 text-xs shadow-none"
        @update:model-value="query = String($event)"
      />
    </div>

    <Separator />

    <div v-if="!store.snapshot" class="flex min-h-0 flex-1 flex-col items-center justify-center gap-2 px-5 text-center">
      <RefreshCw v-if="store.loading" aria-hidden="true" class="size-4 animate-spin text-muted-foreground" />
      <p class="text-xs font-medium">{{ store.connectionMessage }}</p>
      <p v-if="store.error && store.error !== store.connectionMessage" class="text-xs text-muted-foreground">{{ store.error }}</p>
    </div>

    <template v-else>
      <div v-if="store.connectionMessage" class="m-3 rounded-md border border-amber-500/30 bg-amber-500/10 p-3 text-xs text-amber-800 dark:text-amber-300">
        <p>{{ store.connectionMessage }}</p>
        <p v-if="store.error && store.error !== store.connectionMessage" class="mt-1 opacity-80">{{ store.error }}</p>
      </div>

      <div v-else-if="store.error" class="m-3 rounded-md border border-destructive/30 bg-destructive/10 p-3 text-xs text-destructive">
        {{ store.error }}
      </div>

      <ScrollArea class="min-h-0 flex-1">
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
                :description="project.slug"
                :status="project.state"
                :to="`/projects/${encodeURIComponent(project.id)}`"
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
                :key="`${resource.project_id}:${resource.id}`"
                :label="resource.name"
                :description="resource.project_name"
                @activate="openResource(resource.project_id, resource.id)"
              >
                <template #leading><ExternalLink /></template>
              </EntityRow>
            </div>
          </section>
        </template>

        <template v-else-if="destination === 'projects'">
          <section
            v-for="group in [
              { id: 'attention', label: 'Attention', icon: TriangleAlert, projects: attentionProjects },
              { id: 'active', label: 'Active', icon: Activity, projects: activeProjects },
              { id: 'inactive', label: 'Inactive', icon: Folder, projects: inactiveProjects },
            ]"
            v-show="group.projects.length"
            :key="group.id"
            :aria-labelledby="`${group.id}-projects-heading`"
          >
            <h2 :id="`${group.id}-projects-heading`" class="mb-1 flex items-center gap-1.5 px-2 text-[0.6875rem] font-medium tracking-wide text-muted-foreground uppercase">
              <component :is="group.icon" aria-hidden="true" class="size-3" />
              {{ group.label }} · {{ group.projects.length }}
            </h2>
            <div class="space-y-0.5">
              <EntityRow
                v-for="project in group.projects"
                :key="project.id"
                :label="project.name"
                :description="project.slug"
                :status="project.state"
                :selected="selectedProjectId === project.id"
                :to="`/projects/${encodeURIComponent(project.id)}`"
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
              :key="`${service.project_id}:${service.id}`"
              :label="service.name"
              :description="`${service.project_name} · ${service.kind} · ${serviceOwnerLabel(service.owner)}`"
              :status="service.state"
              :selected="selectedProjectId === service.project_id && selectedServiceId === service.id"
              :to="`/services/${encodeURIComponent(service.project_id)}/${encodeURIComponent(service.id)}`"
            >
              <template #leading><ServiceOwnership :owner="service.owner" icon-only /></template>
            </EntityRow>
          </div>
        </section>

        <section v-else-if="destination === 'system' && store.daemonStatus" aria-labelledby="daemon-heading">
          <h2 id="daemon-heading" class="sr-only">Daemon</h2>
          <EntityRow
            label="Harbor daemon"
            :description="`Version ${store.daemonStatus.build.version} · sequence ${store.daemonStatus.sequence}`"
            :status="store.daemonStatus.state"
            selected
            to="/system"
          >
            <template #leading><Server /></template>
          </EntityRow>
        </section>

        <div v-if="resultCount === 0" class="flex min-h-40 flex-col items-center justify-center gap-2 px-6 text-center">
          <Search aria-hidden="true" class="size-5 text-muted-foreground" />
          <p class="text-sm font-medium">{{ emptyMessage }}</p>
          <p class="text-xs text-muted-foreground">
            {{ normalizedQuery ? 'Try a different search.' : 'Harbor will show it here when the daemon reports it.' }}
          </p>
        </div>
      </div>
      </ScrollArea>
    </template>
  </aside>
</template>
