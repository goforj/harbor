<script setup lang="ts">
import { ref } from 'vue'
import { Check, Monitor, Moon, Sun } from '@lucide/vue'
import { Button } from '@/components/ui/button'
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuLabel,
  DropdownMenuSeparator,
  DropdownMenuTrigger,
} from '@/components/ui/dropdown-menu'
import {
  Tooltip,
  TooltipContent,
  TooltipTrigger,
} from '@/components/ui/tooltip'
import {
  setThemePreference,
  themePreference,
  type ThemePreference,
} from '@/lib/theme'

const preference = ref<ThemePreference>(themePreference())

const options = [
  { value: 'system' as const, label: 'System', icon: Monitor },
  { value: 'light' as const, label: 'Light', icon: Sun },
  { value: 'dark' as const, label: 'Dark', icon: Moon },
]

function chooseTheme(value: ThemePreference) {
  preference.value = value
  setThemePreference(value)
}
</script>

<template>
  <DropdownMenu>
    <Tooltip>
      <TooltipTrigger as-child>
        <DropdownMenuTrigger as-child>
          <Button
            variant="ghost"
            size="icon-sm"
            class="text-muted-foreground hover:text-foreground"
            aria-label="Change appearance"
          >
            <Sun v-if="preference === 'light'" aria-hidden="true" />
            <Moon v-else-if="preference === 'dark'" aria-hidden="true" />
            <Monitor v-else aria-hidden="true" />
          </Button>
        </DropdownMenuTrigger>
      </TooltipTrigger>
      <TooltipContent side="right">Appearance</TooltipContent>
    </Tooltip>

    <DropdownMenuContent side="right" align="end" class="w-40">
      <DropdownMenuLabel>Appearance</DropdownMenuLabel>
      <DropdownMenuSeparator />
      <DropdownMenuItem
        v-for="option in options"
        :key="option.value"
        @select="chooseTheme(option.value)"
      >
        <component :is="option.icon" aria-hidden="true" />
        <span>{{ option.label }}</span>
        <Check
          v-if="preference === option.value"
          aria-hidden="true"
          class="ml-auto text-primary"
        />
      </DropdownMenuItem>
    </DropdownMenuContent>
  </DropdownMenu>
</template>
