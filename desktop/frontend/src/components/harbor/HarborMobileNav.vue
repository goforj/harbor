<script setup lang="ts">
import { computed, ref } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { Check, Ellipsis, Monitor, Moon, Search, Settings, Sun } from '@lucide/vue'
import { Button } from '@/components/ui/button'
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuLabel,
  DropdownMenuSeparator,
  DropdownMenuTrigger,
} from '@/components/ui/dropdown-menu'
import { setThemePreference, themePreference, type ThemePreference } from '@/lib/theme'
import { destinationFromPath, harborNavigation } from './navigation'

const emit = defineEmits<{
  command: []
}>()

const route = useRoute()
const router = useRouter()
const preference = ref<ThemePreference>(themePreference())
const activeDestination = computed(() => destinationFromPath(route.path))
const primaryDestinations = harborNavigation.filter((item) => item.destination !== 'system')

const appearanceOptions = [
  { value: 'system' as const, label: 'System appearance', icon: Monitor },
  { value: 'light' as const, label: 'Light appearance', icon: Sun },
  { value: 'dark' as const, label: 'Dark appearance', icon: Moon },
]

function navigate(path: string) {
  void router.push(path)
}

function openCommandMenu() {
  emit('command')
}

function chooseTheme(value: ThemePreference) {
  preference.value = value
  setThemePreference(value)
}
</script>

<template>
  <nav
    class="grid min-h-16 w-full grid-cols-4 border-t border-border bg-background px-1 pb-[env(safe-area-inset-bottom)]"
    aria-label="Primary"
  >
    <Button
      v-for="item in primaryDestinations"
      :key="item.destination"
      variant="ghost"
      class="h-auto min-w-0 flex-col gap-1 rounded-none px-1 py-2 text-[0.6875rem] font-medium text-muted-foreground hover:text-foreground data-[active]:text-primary"
      :aria-label="item.label"
      :aria-current="activeDestination === item.destination ? 'page' : undefined"
      :data-active="activeDestination === item.destination ? '' : undefined"
      @click="navigate(item.path)"
    >
      <component :is="item.icon" aria-hidden="true" class="size-4" />
      <span class="truncate">{{ item.label }}</span>
    </Button>

    <DropdownMenu>
      <DropdownMenuTrigger as-child>
        <Button
          variant="ghost"
          class="h-auto min-w-0 flex-col gap-1 rounded-none px-1 py-2 text-[0.6875rem] font-medium text-muted-foreground hover:text-foreground data-[active]:text-primary"
          aria-label="More Harbor actions"
          :aria-current="activeDestination === 'system' ? 'page' : undefined"
          :data-active="activeDestination === 'system' ? '' : undefined"
        >
          <Ellipsis aria-hidden="true" class="size-4" />
          <span>More</span>
        </Button>
      </DropdownMenuTrigger>

      <DropdownMenuContent side="top" align="end" :side-offset="8" class="w-52">
        <DropdownMenuItem @select="navigate('/system')">
          <Settings aria-hidden="true" />
          <span>System</span>
          <Check v-if="activeDestination === 'system'" aria-hidden="true" class="ml-auto text-primary" />
        </DropdownMenuItem>
        <DropdownMenuItem @select="openCommandMenu">
          <Search aria-hidden="true" />
          <span>Search Harbor</span>
        </DropdownMenuItem>

        <DropdownMenuSeparator />
        <DropdownMenuLabel>Appearance</DropdownMenuLabel>
        <DropdownMenuItem
          v-for="option in appearanceOptions"
          :key="option.value"
          @select="chooseTheme(option.value)"
        >
          <component :is="option.icon" aria-hidden="true" />
          <span>{{ option.label }}</span>
          <Check v-if="preference === option.value" aria-hidden="true" class="ml-auto text-primary" />
        </DropdownMenuItem>
      </DropdownMenuContent>
    </DropdownMenu>
  </nav>
</template>
