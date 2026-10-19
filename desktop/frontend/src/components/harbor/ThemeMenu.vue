<script setup lang="ts">
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
  setThemePreference,
  currentThemePreference,
  type ThemePreference,
} from '@/lib/theme'

const preference = currentThemePreference

const options = [
  { value: 'system' as const, label: 'System', icon: Monitor },
  { value: 'light' as const, label: 'Light', icon: Sun },
  { value: 'dark' as const, label: 'Dark', icon: Moon },
]

function chooseTheme(value: ThemePreference) {
  setThemePreference(value)
}
</script>

<template>
  <DropdownMenu>
    <DropdownMenuTrigger as-child>
      <Button
        variant="ghost"
        size="icon-sm"
        class="text-muted-foreground hover:text-foreground"
        aria-label="Change appearance"
        title="Appearance"
      >
        <Sun v-if="preference === 'light'" aria-hidden="true" />
        <Moon v-else-if="preference === 'dark'" aria-hidden="true" />
        <Monitor v-else aria-hidden="true" />
      </Button>
    </DropdownMenuTrigger>

    <DropdownMenuContent side="right" align="end" :collision-padding="8" class="w-40">
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
