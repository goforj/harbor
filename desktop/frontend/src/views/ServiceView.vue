<script setup lang="ts">
import { computed } from 'vue'
import { RouterLink, useRoute } from 'vue-router'
import { ArrowLeft, ArrowUpRight, Database, ExternalLink, Server } from '@lucide/vue'
import StatusBadge from '@/components/harbor/StatusBadge.vue'
import { Button } from '@/components/ui/button'
import { Card, CardContent, CardHeader, CardTitle } from '@/components/ui/card'
import { Empty, EmptyContent, EmptyDescription, EmptyHeader, EmptyTitle } from '@/components/ui/empty'
import { useHarborStore } from '@/stores/harbor'

const route = useRoute()
const store = useHarborStore()
const projectId = computed(() => String(route.params.projectId ?? ''))
const serviceId = computed(() => String(route.params.serviceId ?? ''))
const service = computed(() => store.serviceById(projectId.value, serviceId.value))
const project = computed(() => store.projectById(projectId.value))
const resources = computed(() => project.value?.resources.filter((resource) =>
  resource.owner.kind === 'service' && resource.owner.service_id === serviceId.value,
) ?? [])
const siblingServices = computed(() => project.value?.services.filter((entry) => entry.id !== serviceId.value) ?? [])

async function openResource(resourceId: string) {
  await store.openResource(projectId.value, resourceId)
}
</script>

<template>
  <main class="h-full min-w-0 overflow-y-auto" :aria-labelledby="service ? 'service-title' : 'service-empty-title'">
    <template v-if="service">
      <header class="border-b px-5 py-4 lg:px-7">
        <div class="flex min-w-0 items-start gap-2">
          <Button variant="ghost" size="icon-sm" class="-ml-2 shrink-0 min-[1100px]:hidden" as-child>
            <RouterLink to="/services" aria-label="Back to services"><ArrowLeft class="size-4" /></RouterLink>
          </Button>
          <div class="min-w-0 flex-1">
            <div class="flex items-center gap-2"><h1 id="service-title" class="truncate text-base font-semibold tracking-tight">{{ service.name }}</h1><StatusBadge :status="service.state" /></div>
            <p class="mt-1 text-xs text-muted-foreground">{{ service.project_name }} · {{ service.kind }}</p>
          </div>
        </div>
      </header>

      <div class="space-y-5 p-5 lg:p-7">
        <section aria-label="Service facts" class="grid overflow-hidden rounded-lg border sm:grid-cols-4">
          <div class="p-4 sm:border-r"><p class="text-xs text-muted-foreground">Owner</p><p class="mt-1 text-sm font-medium">{{ service.owner }}</p></div>
          <div class="border-t p-4 sm:border-t-0 sm:border-r"><p class="text-xs text-muted-foreground">Selection</p><p class="mt-1 text-sm font-medium">{{ service.selection }}</p></div>
          <div class="border-t p-4 sm:border-t-0 sm:border-r"><p class="text-xs text-muted-foreground">Requirement</p><p class="mt-1 text-sm font-medium">{{ service.required ? 'Required' : 'Optional' }}</p></div>
          <div class="border-t p-4 sm:border-t-0"><p class="text-xs text-muted-foreground">Resources</p><p class="mt-1 text-sm font-medium">{{ resources.length }}</p></div>
        </section>

        <div class="grid min-w-0 gap-5 xl:grid-cols-2">
          <Card class="gap-0 rounded-lg py-0 shadow-none">
            <CardHeader class="border-b px-4 py-3"><div class="flex items-center gap-2"><Server class="size-4 text-muted-foreground" /><CardTitle class="text-sm">Service identity</CardTitle></div></CardHeader>
            <CardContent class="p-0"><dl class="divide-y text-xs"><div class="grid grid-cols-[7rem_1fr] gap-3 px-4 py-3"><dt class="text-muted-foreground">Project ID</dt><dd class="font-mono">{{ service.project_id }}</dd></div><div class="grid grid-cols-[7rem_1fr] gap-3 px-4 py-3"><dt class="text-muted-foreground">Service ID</dt><dd class="font-mono">{{ service.id }}</dd></div><div class="grid grid-cols-[7rem_1fr] gap-3 px-4 py-3"><dt class="text-muted-foreground">Kind</dt><dd>{{ service.kind }}</dd></div></dl></CardContent>
          </Card>

          <Card class="gap-0 rounded-lg py-0 shadow-none">
            <CardHeader class="border-b px-4 py-3"><CardTitle class="text-sm">Project</CardTitle></CardHeader>
            <CardContent class="p-0">
              <RouterLink v-if="project" :to="`/projects/${encodeURIComponent(project.id)}`" class="group flex items-center gap-3 px-4 py-3 hover:bg-muted/50">
                <StatusBadge :status="project.state" /><div class="min-w-0 flex-1"><p class="truncate text-sm font-medium">{{ project.name }}</p><p class="truncate text-xs text-muted-foreground">Slug: {{ project.slug }}</p></div><ArrowUpRight class="size-3.5 text-muted-foreground" />
              </RouterLink>
            </CardContent>
          </Card>
        </div>

        <Card v-if="resources.length" class="gap-0 rounded-lg py-0 shadow-none">
          <CardHeader class="border-b px-4 py-3"><CardTitle class="text-sm">Resources</CardTitle></CardHeader>
          <CardContent class="divide-y p-0">
            <button v-for="resource in resources" :key="resource.id" type="button" class="flex w-full items-center gap-3 px-4 py-3 text-left hover:bg-muted/50" @click="openResource(resource.id)"><div class="min-w-0 flex-1"><p class="text-sm font-medium">{{ resource.name }}</p><p class="truncate text-xs text-muted-foreground">{{ resource.kind }} · {{ resource.url }}</p></div><ExternalLink class="size-3.5 text-muted-foreground" /></button>
          </CardContent>
        </Card>

        <Card v-if="siblingServices.length" class="gap-0 rounded-lg py-0 shadow-none">
          <CardHeader class="border-b px-4 py-3"><div class="flex items-center gap-2"><Database class="size-4 text-muted-foreground" /><CardTitle class="text-sm">Other project services</CardTitle></div></CardHeader>
          <CardContent class="divide-y p-0">
            <RouterLink v-for="sibling in siblingServices" :key="sibling.id" :to="`/services/${encodeURIComponent(projectId)}/${encodeURIComponent(sibling.id)}`" class="flex items-center gap-3 px-4 py-3 hover:bg-muted/50"><StatusBadge :status="sibling.state" /><div class="min-w-0 flex-1"><p class="text-sm font-medium">{{ sibling.name }}</p><p class="text-xs text-muted-foreground">{{ sibling.kind }}</p></div><ArrowUpRight class="size-3.5 text-muted-foreground" /></RouterLink>
          </CardContent>
        </Card>
      </div>
    </template>

    <Empty v-else class="min-h-full border-0">
      <EmptyHeader><EmptyTitle id="service-empty-title">{{ store.loading ? 'Loading service…' : serviceId ? 'Service not found' : 'Select a service' }}</EmptyTitle><EmptyDescription>{{ serviceId ? 'The current Harbor snapshot does not contain this project-scoped service.' : 'Choose a service to inspect its reported state.' }}</EmptyDescription></EmptyHeader>
      <EmptyContent v-if="serviceId && !store.loading"><Button variant="outline" as-child><RouterLink to="/services"><ArrowLeft class="size-4" />Back to services</RouterLink></Button></EmptyContent>
    </Empty>
  </main>
</template>
