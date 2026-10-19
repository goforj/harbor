<script setup lang="ts">
import { computed } from 'vue'
import { Check, Monitor, Moon, SquareTerminal, Sun } from '@lucide/vue'
import { Card, CardContent, CardHeader, CardTitle } from '@/components/ui/card'
import { activeTerminalTheme, setTerminalAppearance, terminalAppearance } from '@/lib/terminalAppearance'
import { terminalThemes } from '@/lib/terminalTheme'
import { appliedTheme } from '@/lib/theme'
import TerminalOutput from './TerminalOutput.vue'

const options = [
  { value: 'app', label: 'Follow app', description: 'Match Harbor appearance', icon: Monitor },
  { value: 'dark', label: 'Dark', description: 'Deep GoForj code surface', icon: Moon },
  { value: 'light', label: 'Light', description: 'Clean GoForj code surface', icon: Sun },
] as const

const choices = computed(() => options.map(option => ({
  ...option, palette: terminalThemes[option.value === 'app' ? appliedTheme.value : option.value],
})))

const preview = '\u001b[32m✔ Build complete\u001b[0m · 482 ms\n\u001b[34mHTTP\u001b[0m  GET /tasks  \u001b[32m200\u001b[0m\n\u001b[90mWatching for changes…\u001b[0m'
</script>

<template>
  <Card class="gap-0 rounded-lg py-0 shadow-none">
    <CardHeader class="border-b px-4 py-3">
      <div class="flex items-center gap-2"><SquareTerminal class="size-4 text-muted-foreground" aria-hidden="true" /><CardTitle id="terminal-appearance-title" class="text-sm">Terminal appearance</CardTitle></div>
      <p class="mt-1 text-xs text-muted-foreground">Applies to development output, service logs, and interactive terminals.</p>
    </CardHeader>
    <CardContent class="space-y-4 p-4">
      <div role="radiogroup" aria-labelledby="terminal-appearance-title" class="grid gap-3 sm:grid-cols-3">
        <label v-for="option in choices" :key="option.value" class="relative cursor-pointer">
          <input type="radio" name="terminal-appearance" :value="option.value" :aria-label="`${option.label} terminal appearance`" :checked="terminalAppearance === option.value" class="peer absolute inset-0 z-10 size-full cursor-pointer opacity-0" @change="setTerminalAppearance(option.value)">
          <div class="overflow-hidden rounded-lg border transition-colors peer-focus-visible:ring-2 peer-focus-visible:ring-ring peer-focus-visible:ring-offset-2 peer-focus-visible:ring-offset-background" :class="terminalAppearance === option.value ? 'border-primary ring-1 ring-primary' : 'border-border hover:border-muted-foreground/50'">
            <div class="space-y-2 border-b px-4 py-4" :style="{ backgroundColor: option.palette.background }" aria-hidden="true">
              <div class="h-1.5 w-2/3 rounded-full" :style="{ backgroundColor: option.palette.foreground, opacity: 0.8 }" />
              <div class="h-1.5 w-1/2 rounded-full" :style="{ backgroundColor: option.palette.green }" />
              <div class="h-1.5 w-4/5 rounded-full" :style="{ backgroundColor: option.palette.blue }" />
            </div>
            <div class="px-3 py-3">
              <span class="flex items-center gap-2 text-sm font-medium"><component :is="option.icon" class="size-3.5 text-muted-foreground" aria-hidden="true" />{{ option.label }}<Check v-if="terminalAppearance === option.value" class="ml-auto size-3.5 text-primary" aria-hidden="true" /></span>
              <span class="mt-1 block text-xs text-muted-foreground">{{ option.description }}</span>
            </div>
          </div>
        </label>
      </div>
      <div class="overflow-hidden rounded-lg border" :style="{ borderColor: activeTerminalTheme.border }">
        <div class="border-b px-4 py-2 text-[11px]" :style="{ backgroundColor: activeTerminalTheme.chrome, borderColor: activeTerminalTheme.border, color: activeTerminalTheme.brightBlack }">Preview</div>
        <div class="harbor-log-viewport p-4" aria-label="Terminal appearance preview"><TerminalOutput :output="preview" :reset-key="0" /></div>
      </div>
    </CardContent>
  </Card>
</template>
