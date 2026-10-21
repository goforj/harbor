<script setup lang="ts">
import { computed, ref } from 'vue'
import { Check, ChevronDown, ExternalLink, LoaderCircle, RefreshCw, Wrench } from '@lucide/vue'
import { Button } from '@/components/ui/button'
import { harborBridge } from '@/bridge'
import type { PrerequisiteReport } from '@/domain/harbor'
import { prerequisitesReady } from '@/lib/prerequisites'

const props = defineProps<{ report?: PrerequisiteReport | null; checking?: boolean; error?: string | null; compact?: boolean; stage?: 'creation' | 'start'; continueAvailable?: boolean }>()
defineEmits<{ recheck: []; continue: [] }>()
const expanded = ref(false)
const linkError = ref('')
const ready = computed(() => prerequisitesReady(props.report, props.stage))
const runtimeReady = computed(() => prerequisitesReady(props.report))
const showChecks = computed(() => !props.compact || !runtimeReady.value || expanded.value)
const visibleChecks = computed(() => props.compact && !expanded.value ? props.report?.checks.filter(check => check.status !== 'ready') : props.report?.checks)

// openInstructions uses the native URL boundary so documentation never replaces the bridge-enabled workspace.
async function openInstructions(url: string) {
  linkError.value = ''
  try { await harborBridge.openTerminalURL(url) }
  catch { linkError.value = 'Could not open the instructions. Try opening the link in your browser.' }
}
</script>

<template>
  <section aria-label="Tool prerequisites" class="overflow-hidden rounded-lg border bg-card text-card-foreground">
    <div class="flex flex-wrap items-center justify-between gap-2 px-4 py-3">
      <div class="flex items-center gap-2 text-sm font-medium"><LoaderCircle v-if="checking" class="size-4 animate-spin text-primary" /><Check v-else-if="ready && !error" class="size-4 text-status-ready" /><Wrench v-else class="size-4 text-amber-500" />{{ checking ? 'Checking local tools' : ready && !error ? runtimeReady ? 'Required tools are ready' : 'Ready to create; runtime tools need attention' : 'Check required tools' }}</div>
      <div class="flex gap-1"><Button v-if="compact && report" variant="ghost" size="sm" class="h-7 text-xs" :aria-expanded="expanded" @click="expanded = !expanded">Details<ChevronDown class="size-3" /></Button><Button variant="ghost" size="sm" class="h-7 text-xs" :disabled="checking" @click="$emit('recheck')"><RefreshCw class="size-3" />Recheck</Button></div>
    </div>
    <p v-if="error || linkError" role="alert" class="border-t px-4 py-3 text-xs text-destructive">{{ error || linkError }}</p>
    <div v-if="showChecks && report" class="max-h-64 divide-y overflow-y-auto border-t">
      <div v-for="check in visibleChecks" :key="check.id" class="px-4 py-3">
        <div class="flex flex-wrap items-center gap-2 text-sm"><span class="size-1.5 rounded-full" :class="check.status === 'ready' ? 'bg-status-ready' : 'bg-amber-500'" /><span class="font-medium">{{ check.name }}</span><span v-if="check.version" class="font-mono text-xs text-muted-foreground">{{ check.version }}</span><span class="ml-auto text-xs capitalize" :class="check.status === 'ready' ? 'text-status-ready' : 'text-amber-600 dark:text-amber-400'">{{ check.status }}</span></div>
        <p class="mt-1 text-xs leading-relaxed text-muted-foreground">{{ check.detail }}<span v-if="stage === 'creation' && check.required_for === 'start' && check.status !== 'ready'"> Needed when you start the project.</span></p>
        <template v-if="check.status !== 'ready'"><p v-if="check.action" class="mt-2 text-xs leading-relaxed">{{ check.action }}</p><Button v-if="check.url" variant="link" size="sm" class="mt-1 h-auto px-0 text-xs" @click="openInstructions(check.url)">Installation and troubleshooting<ExternalLink class="size-3" /></Button></template>
      </div>
    </div>
    <div v-if="continueAvailable && (!ready || error)" class="flex flex-wrap items-center justify-between gap-3 border-t px-4 py-3"><p class="max-w-lg text-xs text-muted-foreground">The background service may use a different tool environment. You can let it attempt the normal Start checks.</p><Button size="sm" variant="outline" :disabled="checking" @click="$emit('continue')">Try Start</Button></div>
    <p v-if="showChecks" class="border-t px-4 py-2 text-[11px] leading-relaxed text-muted-foreground">Checks use Harbor's desktop tool environment. Secure networking is managed separately.</p>
  </section>
</template>
