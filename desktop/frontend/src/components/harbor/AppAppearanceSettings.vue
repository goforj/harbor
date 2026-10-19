<script setup lang="ts">
import { Check, Monitor, Moon, Palette, Sun } from '@lucide/vue'
import { Card, CardContent, CardHeader, CardTitle } from '@/components/ui/card'
import { currentThemePreference, setThemePreference } from '@/lib/theme'

const options = [
  { value: 'system', label: 'System', description: 'Follow your device', icon: Monitor },
  { value: 'dark', label: 'Dark', description: 'A darker workspace', icon: Moon },
  { value: 'light', label: 'Light', description: 'A lighter workspace', icon: Sun },
] as const

const preference = currentThemePreference
</script>

<template>
  <Card class="gap-0 rounded-lg py-0 shadow-none">
    <CardHeader class="border-b px-4 py-3">
      <div class="flex items-center gap-2"><Palette class="size-4 text-muted-foreground" aria-hidden="true" /><CardTitle id="app-appearance-title" class="text-sm">App appearance</CardTitle></div>
      <p class="mt-1 text-xs text-muted-foreground">Choose Harbor's workspace theme.</p>
    </CardHeader>
    <CardContent class="p-4">
      <div role="radiogroup" aria-labelledby="app-appearance-title" class="grid gap-3 sm:grid-cols-3">
        <label v-for="option in options" :key="option.value" class="relative cursor-pointer">
          <input type="radio" name="app-appearance" :value="option.value" :aria-label="`${option.label} app appearance`" :checked="preference === option.value" class="peer absolute inset-0 z-10 size-full cursor-pointer opacity-0" @change="setThemePreference(option.value)">
          <div class="rounded-lg border px-3 py-4 transition-colors peer-focus-visible:ring-2 peer-focus-visible:ring-ring peer-focus-visible:ring-offset-2 peer-focus-visible:ring-offset-background" :class="preference === option.value ? 'border-primary ring-1 ring-primary' : 'border-border hover:border-muted-foreground/50'">
            <span class="flex items-center gap-2 text-sm font-medium"><component :is="option.icon" class="size-4 text-muted-foreground" aria-hidden="true" />{{ option.label }}<Check v-if="preference === option.value" class="ml-auto size-3.5 text-primary" aria-hidden="true" /></span>
            <span class="mt-1 block text-xs text-muted-foreground">{{ option.description }}</span>
          </div>
        </label>
      </div>
    </CardContent>
  </Card>
</template>
