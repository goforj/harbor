<script setup lang="ts">
import { SquareTerminal } from '@lucide/vue'
import type { ProjectCreationOption } from '@/domain/harbor'
import { activeTerminalTheme, terminalCSSVariables } from '@/lib/terminalAppearance'
import TerminalOutput from './TerminalOutput.vue'

defineProps<{ label: string; option?: ProjectCreationOption }>()
</script>

<template>
  <section v-if="option?.preview" :aria-label="`${label} preview`" class="mt-3 overflow-hidden rounded-lg border" :style="{ ...terminalCSSVariables, borderColor: activeTerminalTheme.border }">
    <div class="flex items-center gap-2 border-b px-3 py-2 text-[11px]" :style="{ backgroundColor: activeTerminalTheme.chrome, borderColor: activeTerminalTheme.border, color: activeTerminalTheme.foreground }">
      <SquareTerminal class="size-3.5 text-muted-foreground" aria-hidden="true" />
      <span class="font-medium">Preview</span>
      <span class="ml-auto truncate text-muted-foreground">{{ option.label }}</span>
    </div>
    <div class="harbor-log-viewport max-h-64 overflow-auto p-3">
      <TerminalOutput :key="option.value" :output="option.preview" :reset-key="0" class="text-xs!" />
    </div>
    <p v-if="option.description" class="border-t px-3 py-2 text-xs leading-relaxed" :style="{ backgroundColor: activeTerminalTheme.chrome, borderColor: activeTerminalTheme.border, color: activeTerminalTheme.foreground }">{{ option.description }}</p>
  </section>
</template>
