<script setup lang="ts">
import { computed } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { Activity, Anchor, Command, Search } from '@lucide/vue'
import { Button } from '@/components/ui/button'
import { Separator } from '@/components/ui/separator'
import {
  Tooltip,
  TooltipContent,
  TooltipProvider,
  TooltipTrigger,
} from '@/components/ui/tooltip'
import { useHarborStore } from '@/stores/harbor'
import ThemeMenu from './ThemeMenu.vue'
import { destinationFromPath, harborNavigation } from './navigation'

const emit = defineEmits<{
  command: []
}>()

const route = useRoute()
const router = useRouter()
const store = useHarborStore()

const activeDestination = computed(() => destinationFromPath(route.path))
const daemonReady = computed(() => store.connectionState === 'connected'
  && !store.snapshotStale
  && store.daemonStatus?.state === 'ready')

function navigate(path: string) {
  void router.push(path)
}
</script>

<template>
  <TooltipProvider :delay-duration="250">
    <aside
      class="flex h-dvh w-14 shrink-0 flex-col items-center border-r border-border bg-background py-2"
      aria-label="Harbor navigation"
    >
      <Tooltip>
        <TooltipTrigger as-child>
          <Button
            variant="ghost"
            size="icon-lg"
            class="mb-2 text-primary hover:bg-primary/10 hover:text-primary"
            aria-label="Go to Harbor overview"
            @click="navigate('/overview')"
          >
            <span class="flex size-8 items-center justify-center rounded-lg bg-primary text-primary-foreground shadow-sm">
              <Anchor aria-hidden="true" class="size-4" />
            </span>
          </Button>
        </TooltipTrigger>
        <TooltipContent side="right">GoForj Harbor</TooltipContent>
      </Tooltip>

      <Separator class="mb-2 w-7" />

      <nav class="flex flex-col items-center gap-1" aria-label="Primary">
        <Tooltip v-for="item in harborNavigation" :key="item.destination">
          <TooltipTrigger as-child>
            <Button
              variant="ghost"
              size="icon-lg"
              :aria-label="item.label"
              :aria-current="activeDestination === item.destination ? 'page' : undefined"
              :data-active="activeDestination === item.destination ? '' : undefined"
              class="relative text-muted-foreground hover:text-foreground data-[active]:bg-accent data-[active]:text-foreground before:absolute before:top-2 before:bottom-2 before:-left-2 before:w-0.5 before:rounded-r before:bg-primary before:opacity-0 data-[active]:before:opacity-100"
              @click="navigate(item.path)"
            >
              <component :is="item.icon" aria-hidden="true" />
            </Button>
          </TooltipTrigger>
          <TooltipContent side="right">{{ item.label }}</TooltipContent>
        </Tooltip>
      </nav>

      <div class="mt-auto flex flex-col items-center gap-1">
        <Tooltip>
          <TooltipTrigger as-child>
            <Button
              variant="ghost"
              size="icon-sm"
              class="relative text-muted-foreground hover:text-foreground"
              aria-label="Open command menu"
              @click="emit('command')"
            >
              <Search aria-hidden="true" />
            </Button>
          </TooltipTrigger>
          <TooltipContent side="right" class="flex items-center gap-3">
            <span>Search Harbor</span>
            <span class="text-muted-foreground"><Command class="mr-0.5 inline size-3" />K</span>
          </TooltipContent>
        </Tooltip>

        <ThemeMenu />

        <Tooltip>
          <TooltipTrigger as-child>
            <Button
              variant="ghost"
              size="icon-sm"
              class="relative text-muted-foreground hover:text-foreground"
              aria-label="View daemon status"
              @click="navigate('/system/daemon')"
            >
              <Activity aria-hidden="true" />
              <span
                :class="[
                  'absolute right-1 bottom-1 size-2 rounded-full border-2 border-background',
                  daemonReady ? 'bg-status-ready' : 'bg-status-failed',
                ]"
                aria-hidden="true"
              />
            </Button>
          </TooltipTrigger>
          <TooltipContent side="right">
            Harbor daemon: {{ daemonReady ? 'connected' : 'attention needed' }}
          </TooltipContent>
        </Tooltip>
      </div>
    </aside>
  </TooltipProvider>
</template>
