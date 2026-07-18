<script setup lang="ts">
import { computed } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import {
  Check,
  ExternalLink,
  Folder,
  Server,
} from '@lucide/vue'
import {
  CommandDialog,
  CommandEmpty,
  CommandGroup,
  CommandInput,
  CommandItem,
  CommandList,
  CommandSeparator,
  CommandShortcut,
} from '@/components/ui/command'
import { useHarborStore } from '@/stores/harbor'
import StatusBadge from './StatusBadge.vue'
import { harborNavigation } from './navigation'

const props = withDefaults(defineProps<{
  open?: boolean
}>(), {
  open: false,
})

const emit = defineEmits<{
  'update:open': [value: boolean]
}>()

const route = useRoute()
const router = useRouter()
const store = useHarborStore()

const recentResources = computed(() => store.snapshot?.recentResources ?? [])

function setOpen(value: boolean) {
  emit('update:open', value)
}

function navigate(path: string) {
  setOpen(false)
  void router.push(path)
}

async function openResource(resourceId: string) {
  setOpen(false)
  await store.openResource(resourceId)
}
</script>

<template>
  <CommandDialog
    :open="props.open"
    title="Search Harbor"
    description="Open a Harbor destination, project, service, or resource."
    @update:open="setOpen"
  >
    <CommandInput placeholder="Search projects, services, and Harbor…" />
    <CommandList class="max-h-[min(62vh,32rem)]">
      <CommandEmpty>No Harbor results found.</CommandEmpty>

      <CommandGroup heading="Navigate">
        <CommandItem
          v-for="(item, index) in harborNavigation"
          :key="item.destination"
          :value="`${item.label} ${item.destination}`"
          @select="navigate(item.path)"
        >
          <component :is="item.icon" aria-hidden="true" />
          <span>{{ item.label }}</span>
          <Check v-if="route.path === item.path" aria-hidden="true" class="ml-auto text-primary" />
          <CommandShortcut v-else>{{ index + 1 }}</CommandShortcut>
        </CommandItem>
      </CommandGroup>

      <CommandSeparator v-if="store.projects.length" />
      <CommandGroup v-if="store.projects.length" heading="Projects">
        <CommandItem
          v-for="project in store.projects"
          :key="project.id"
          :value="`${project.name} ${project.path} ${project.domain}`"
          @select="navigate(`/projects/${project.id}`)"
        >
          <Folder aria-hidden="true" />
          <span class="min-w-0 flex-1 truncate">{{ project.name }}</span>
          <StatusBadge :status="project.status" compact />
        </CommandItem>
      </CommandGroup>

      <CommandSeparator v-if="store.services.length" />
      <CommandGroup v-if="store.services.length" heading="Services">
        <CommandItem
          v-for="service in store.services"
          :key="service.id"
          :value="`${service.name} ${service.projectName} ${service.kind} ${service.endpoint}`"
          @select="navigate(`/services/${service.id}`)"
        >
          <Server aria-hidden="true" />
          <span class="min-w-0 flex-1 truncate">
            {{ service.name }}
            <span class="ml-1 text-muted-foreground">{{ service.projectName }}</span>
          </span>
          <StatusBadge :status="service.status" compact />
        </CommandItem>
      </CommandGroup>

      <CommandSeparator v-if="recentResources.length" />
      <CommandGroup v-if="recentResources.length" heading="Resources">
        <CommandItem
          v-for="resource in recentResources"
          :key="resource.id"
          :value="`${resource.name} ${resource.projectName} ${resource.url}`"
          @select="openResource(resource.id)"
        >
          <ExternalLink aria-hidden="true" />
          <span class="min-w-0 flex-1 truncate">
            {{ resource.name }}
            <span class="ml-1 text-muted-foreground">{{ resource.projectName }}</span>
          </span>
        </CommandItem>
      </CommandGroup>
    </CommandList>
  </CommandDialog>
</template>
