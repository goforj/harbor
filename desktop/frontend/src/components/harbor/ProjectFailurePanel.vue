<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { Check, ChevronDown, Clipboard, FileText, RefreshCw, TriangleAlert } from '@lucide/vue'
import { Button } from '@/components/ui/button'
import { copyText } from '@/bridge/clipboard'
import type { ProjectSessionActivity } from '@/domain/harbor'
import { projectFailureDetails, redactFailureText } from '@/lib/projectFailure'

const props = defineProps<{
  projectName: string
  message: string
  code: string
  session?: ProjectSessionActivity
  output: string
  truncated: boolean
  outputError?: string | null
  retryAvailable: boolean
  retryDisabled: boolean
}>()
const emit = defineEmits<{ retry: []; viewOutput: [] }>()
const failure = computed(() => projectFailureDetails(props.message, props.code, props.session, props.output))
const expanded = ref(false)
const copied = ref(false)
const copyError = ref<string | null>(null)
watch([() => props.session?.id, () => props.message, () => props.code], () => {
  expanded.value = false
  copied.value = false
  copyError.value = null
})

// copyFailure keeps support text bounded and uses the same redacted excerpt shown locally.
async function copyFailure() {
  copyError.value = null
  const selectedSession = props.session?.id
  const selectedMessage = props.message
  const selectedCode = props.code
  try {
    const details = failure.value
    const report = [
      `Project: ${redactFailureText(props.projectName)}`,
      details.title,
      details.message,
      props.code ? `Problem: ${props.code}` : '',
      details.step ? `${details.stepLabel}: ${details.step}` : '',
      ...details.conflicts.map(conflict => `${conflict.service}: ${conflict.address}:${conflict.port}/${conflict.protocol}; ${conflict.listener} publishes ${conflict.bound}`),
      details.guidance,
      details.excerpt ? `\n${props.session?.output.historical ? 'Retained' : 'Latest'} launch output:\n${details.excerpt}` : '\nLaunch output is unavailable.',
      props.truncated || details.clipped ? '\nEarlier output is omitted.' : '',
    ].filter(Boolean).join('\n')
    await copyText(report)
    if (props.session?.id === selectedSession && props.message === selectedMessage && props.code === selectedCode) copied.value = true
  }
  catch (error) {
    copyError.value = error instanceof Error ? error.message : 'Could not copy failure details.'
  }
}
</script>

<template>
  <section class="overflow-hidden rounded-lg border border-destructive/30 bg-destructive/[0.04] text-foreground" role="alert" aria-label="Project failure details" data-testid="project-failure-panel">
    <div class="flex items-start gap-3 p-4">
      <span class="flex size-8 shrink-0 items-center justify-center rounded-lg bg-destructive/10 text-destructive"><TriangleAlert class="size-4" aria-hidden="true" /></span>
      <div class="min-w-0 flex-1 space-y-2">
        <h2 class="text-sm font-semibold text-foreground">{{ failure.title }}</h2>
        <p class="break-words text-sm text-foreground/85">{{ failure.message }}</p>
        <p v-if="failure.step" class="text-xs text-muted-foreground">{{ failure.stepLabel }}: <strong class="font-medium text-foreground">{{ failure.step }}</strong></p>
        <div v-if="failure.conflicts.length" class="space-y-1 text-xs text-muted-foreground">
          <p v-for="conflict in failure.conflicts" :key="`${conflict.service}:${conflict.address}:${conflict.port}:${conflict.listener}`" class="break-words">
            <strong class="text-foreground">{{ conflict.service }}</strong> requests <code>{{ conflict.address }}:{{ conflict.port }}/{{ conflict.protocol }}</code>.
            <strong class="text-foreground">{{ conflict.listener }}</strong> publishes that port on <code>{{ conflict.bound }}</code>.
          </p>
          <p>A wildcard bind such as <code>0.0.0.0</code> can block a project's loopback address.</p>
        </div>
        <p class="text-xs leading-relaxed text-muted-foreground">{{ failure.guidance }}</p>
        <div class="flex flex-wrap items-center gap-2 pt-1">
          <Button v-if="retryAvailable" size="sm" variant="outline" :disabled="retryDisabled" @click="emit('retry')"><RefreshCw class="size-3.5" aria-hidden="true" />Retry start</Button>
          <Button size="sm" variant="ghost" :aria-expanded="expanded" @click="expanded = !expanded"><FileText class="size-3.5" aria-hidden="true" />{{ expanded ? 'Hide details' : 'Show details' }}<ChevronDown class="size-3 transition-transform" :class="expanded && 'rotate-180'" aria-hidden="true" /></Button>
          <Button size="sm" variant="ghost" @click="copyFailure"><Check v-if="copied" class="size-3.5" aria-hidden="true" /><Clipboard v-else class="size-3.5" aria-hidden="true" />{{ copied ? 'Copied' : 'Copy failure details' }}</Button>
        </div>
        <p v-if="copyError" class="text-xs text-destructive">{{ copyError }}</p>
      </div>
    </div>
    <div v-if="expanded" class="border-t border-destructive/15" data-testid="failure-excerpt">
      <div class="flex flex-wrap items-center justify-between gap-2 bg-muted/30 px-4 py-2 text-xs text-muted-foreground">
        <span>{{ session?.output.historical ? 'Retained launch output' : 'Latest launch output' }}<span v-if="truncated || failure.clipped"> · Earlier output omitted</span></span>
        <Button v-if="failure.excerpt" size="sm" variant="ghost" class="h-7 text-xs" @click="emit('viewOutput')">View development output</Button>
      </div>
      <pre v-if="failure.excerpt" class="max-h-64 overflow-auto whitespace-pre-wrap break-words bg-background px-4 py-3 font-mono text-xs leading-relaxed text-foreground">{{ failure.excerpt }}</pre>
      <p v-else class="px-4 py-4 text-sm text-muted-foreground">{{ outputError ? 'Launch output could not be loaded. Reconnect to Harbor and try again.' : 'No launch output is available for this session. The failure may have happened before the development process started.' }}</p>
      <p class="border-t border-border/60 px-4 py-2 text-[11px] text-muted-foreground">Common credential fields are redacted. Review before sharing.<span v-if="code" class="ml-2 break-all font-mono">{{ code }}</span></p>
    </div>
  </section>
</template>
