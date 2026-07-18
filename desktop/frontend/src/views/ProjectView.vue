<script setup lang="ts">
import { computed, ref } from 'vue'
import { RouterLink, useRoute } from 'vue-router'
import {
  ArrowLeft,
  ArrowUpRight,
  Check,
  Clipboard,
  ExternalLink,
  Server,
  SquareTerminal,
} from '@lucide/vue'
import StatusBadge from '@/components/harbor/StatusBadge.vue'
import { copyText } from '@/bridge/clipboard'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Card, CardContent, CardHeader, CardTitle } from '@/components/ui/card'
import { Empty, EmptyContent, EmptyDescription, EmptyHeader, EmptyTitle } from '@/components/ui/empty'
import { useHarborStore } from '@/stores/harbor'

const route = useRoute()
const store = useHarborStore()
const copiedPath = ref(false)
const projectId = computed(() => String(route.params.projectId ?? ''))
const project = computed(() => store.projectById(projectId.value))
const projectOperations = computed(() => store.operations.filter((operation) => operation.project_id === projectId.value))
const primaryResource = computed(() => project.value?.resources.find((resource) => resource.kind === 'application'))
const updatedAt = computed(() => project.value
  ? new Intl.DateTimeFormat(undefined, { dateStyle: 'medium', timeStyle: 'medium' }).format(new Date(project.value.updated_at))
  : '')

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
          <Button size="sm" :disabled="!primaryResource" @click="primaryResource && openResource(primaryResource.id)">Open resource<ExternalLink class="size-3.5" /></Button>
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
        <section aria-label="Project summary" class="grid overflow-hidden rounded-lg border sm:grid-cols-4">
          <div class="p-4 sm:border-r"><p class="text-xs font-medium text-muted-foreground">Apps</p><p class="mt-1 text-xl font-semibold">{{ project.apps.length }}</p></div>
          <div class="border-t p-4 sm:border-t-0 sm:border-r"><p class="text-xs font-medium text-muted-foreground">Services</p><p class="mt-1 text-xl font-semibold">{{ project.services.length }}</p></div>
          <div class="border-t p-4 sm:border-t-0 sm:border-r"><p class="text-xs font-medium text-muted-foreground">Resources</p><p class="mt-1 text-xl font-semibold">{{ project.resources.length }}</p></div>
          <div class="border-t p-4 sm:border-t-0"><p class="text-xs font-medium text-muted-foreground">Operations</p><p class="mt-1 text-xl font-semibold">{{ projectOperations.length }}</p></div>
        </section>

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
                  <div class="min-w-0 flex-1"><p class="truncate text-sm font-medium">{{ service.name }}</p><p class="truncate text-xs text-muted-foreground">{{ service.kind }} · {{ service.owner }} · {{ service.selection }}</p></div>
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

        <Card v-if="projectOperations.length" class="gap-0 rounded-lg py-0 shadow-none">
          <CardHeader class="border-b px-4 py-3"><CardTitle class="text-sm">Operations</CardTitle></CardHeader>
          <CardContent class="divide-y p-0">
            <div v-for="operation in projectOperations" :key="operation.id" class="flex items-center gap-3 px-4 py-3"><StatusBadge :status="operation.state" /><div><p class="text-sm font-medium">{{ operation.kind }}</p><p class="text-xs text-muted-foreground">{{ operation.phase }}</p></div></div>
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
