<script setup lang="ts">
import { computed, nextTick } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { Check } from '@lucide/vue'
import { CommandDialog, CommandEmpty, CommandGroup, CommandInput, CommandItem, CommandList, CommandSeparator } from '@/components/ui/command'
import { useHarborStore } from '@/stores/harbor'
import { showNewProjectDialog } from '@/lib/projectCreationDialog'
import { showProjectGuide } from '@/lib/projectGuide'
import { currentThemePreference, setThemePreference, type ThemePreference } from '@/lib/theme'
import StatusBadge from './StatusBadge.vue'
import ProjectIcon from './ProjectIcon.vue'
import ServiceIcon from './ServiceIcon.vue'
import KindIcon from './KindIcon.vue'
import ResourceFavicon from './ResourceFavicon.vue'
import { destinationFromPath, harborNavigation, harborSettingsNavigation } from './navigation'

const props = withDefaults(defineProps<{ open?: boolean }>(), { open: false })
const emit = defineEmits<{ 'update:open': [value: boolean] }>()
const route = useRoute()
const router = useRouter()
const store = useHarborStore()
const recentResources = computed(() => store.recentResources)
const otherResources = computed(() => {
  const recent = new Set(recentResources.value.map(resource => `${resource.project_id}:${resource.id}`))
  return store.resources.filter(resource => !recent.has(`${resource.project_id}:${resource.id}`))
})
const resourceGroups = computed(() => [
  { label: 'Recent resources', resources: recentResources.value },
  { label: 'Resources', resources: otherResources.value },
].filter(group => group.resources.length))
const connected = computed(() => store.connectionState === 'connected' && !store.snapshotStale)
const themeOptions = [
  { value: 'system', label: 'Use system appearance', kind: 'theme-system' },
  { value: 'dark', label: 'Use dark appearance', kind: 'theme-dark' },
  { value: 'light', label: 'Use light appearance', kind: 'theme-light' },
] as const

function setOpen(value: boolean) {
  emit('update:open', value)
}

function navigate(path: string) {
  setOpen(false)
  void router.push(path)
}

async function createProject() {
  if (!connected.value) return
  setOpen(false)
  await nextTick()
  showNewProjectDialog()
}

async function addProject() {
  if (!connected.value || store.addingProject) return
  setOpen(false)
  await nextTick()
  const registration = await store.addProject()
  if (registration) await router.push(`/projects/${encodeURIComponent(registration.project.id)}`)
}

function openProjectGuide() {
  showProjectGuide()
  navigate('/overview')
}

function refresh() {
  setOpen(false)
  void store.refresh()
}

function chooseTheme(value: ThemePreference) {
  setOpen(false)
  setThemePreference(value)
}

async function openResource(projectId: string, resourceId: string) {
  if (!connected.value) return
  setOpen(false)
  await store.openResource(projectId, resourceId)
}
</script>

<template>
  <CommandDialog :open="props.open" title="Search Harbor" description="Find an action, setting, project, service, or resource." @update:open="setOpen">
    <CommandInput placeholder="Search projects, services, and Harbor…" />
    <CommandList class="max-h-[min(62vh,32rem)]">
      <CommandEmpty>No Harbor results found.</CommandEmpty>

      <CommandGroup heading="Actions">
        <CommandItem value="Getting started first project onboarding setup create start help" @select="openProjectGuide"><KindIcon kind="overview" compact /><span>Getting started</span></CommandItem>
        <CommandItem value="New project create scaffold starter kit" :disabled="!connected" @select="createProject"><KindIcon kind="new-project" compact /><span>New project</span></CommandItem>
        <CommandItem value="Add existing project import folder" :disabled="!connected || store.addingProject" @select="addProject"><KindIcon kind="import-project" compact /><span>Add existing project</span></CommandItem>
        <CommandItem value="Refresh Harbor state reload snapshot reconnect" :disabled="store.refreshing" @select="refresh"><KindIcon kind="refresh" compact /><span>Refresh Harbor state</span></CommandItem>
      </CommandGroup>
      <CommandSeparator />
      <CommandGroup heading="Navigate">
        <CommandItem v-for="item in harborNavigation" :key="item.destination" :value="`${item.label} ${item.destination}`" @select="navigate(item.path)">
          <KindIcon :kind="item.destination" compact /><span>{{ item.label }}</span>
          <Check v-if="destinationFromPath(route.path) === item.destination" aria-hidden="true" class="ml-auto text-primary" />
        </CommandItem>
      </CommandGroup>
      <CommandSeparator />
      <CommandGroup heading="Settings">
        <CommandItem v-for="item in harborSettingsNavigation" :key="item.section" :value="`${item.label} ${item.description} settings preferences`" @select="navigate(item.path)">
          <KindIcon :kind="item.section" compact /><span>{{ item.label }}</span><span class="ml-auto text-xs text-muted-foreground">{{ item.description }}</span>
        </CommandItem>
        <CommandItem v-for="option in themeOptions" :key="option.value" :value="`${option.label} theme colors`" @select="chooseTheme(option.value)">
          <KindIcon :kind="option.kind" compact /><span>{{ option.label }}</span><Check v-if="currentThemePreference === option.value" class="ml-auto text-primary" aria-hidden="true" />
        </CommandItem>
      </CommandGroup>

      <CommandSeparator v-if="store.projects.length" />
      <CommandGroup v-if="store.projects.length" heading="Projects">
        <CommandItem v-for="project in store.projects" :key="project.id" :value="`${project.name} ${project.path} ${project.slug}`" @select="navigate(`/projects/${encodeURIComponent(project.id)}`)">
          <ProjectIcon :project="project" compact /><span class="min-w-0 flex-1 truncate">{{ project.name }}</span><StatusBadge :status="project.state" compact />
        </CommandItem>
      </CommandGroup>

      <CommandSeparator v-if="store.services.length" />
      <CommandGroup v-if="store.services.length" heading="Services">
        <CommandItem v-for="service in store.services" :key="`${service.project_id}:${service.id}`" :value="`${service.name} ${service.project_name} ${service.kind} ${service.id}`" @select="navigate(`/services/${encodeURIComponent(service.project_id)}/${encodeURIComponent(service.id)}`)">
          <ServiceIcon :service="service" compact />
          <span class="min-w-0 flex-1 truncate">{{ service.name }}<span class="ml-1 text-muted-foreground">{{ service.project_name }}</span></span>
          <StatusBadge :status="service.state" compact />
        </CommandItem>
      </CommandGroup>

      <template v-for="group in resourceGroups" :key="group.label">
        <CommandSeparator />
        <CommandGroup :heading="group.label">
          <CommandItem v-for="resource in group.resources" :key="`${resource.project_id}:${resource.id}`" :value="`${resource.name} ${resource.project_name} ${resource.kind} ${resource.url}`" :disabled="!connected" @select="openResource(resource.project_id, resource.id)">
            <ResourceFavicon :kind="resource.kind" :name="resource.name" :url="resource.url" :project-id="resource.project_id" :resource-id="resource.id" compact />
            <span class="min-w-0 flex-1 truncate">{{ resource.name }}<span class="ml-1 text-muted-foreground">{{ resource.project_name }}</span></span>
          </CommandItem>
        </CommandGroup>
      </template>
    </CommandList>
  </CommandDialog>
</template>
