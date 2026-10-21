<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { Check, Clipboard, FileSearch, RefreshCw } from '@lucide/vue'
import { copyText } from '@/bridge/clipboard'
import { Button } from '@/components/ui/button'
import { Checkbox } from '@/components/ui/checkbox'
import { Dialog, DialogClose, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle, DialogTrigger } from '@/components/ui/dialog'
import { supportDiagnostics, type SupportDiagnosticsSource } from '@/lib/supportDiagnostics'

const props = defineProps<{ source: SupportDiagnosticsSource; outputResetKey?: number }>()
const open = ref(false)
const includeOutput = ref(false)
const preview = ref<string | null>(null)
const copied = ref(false)
const copying = ref(false)
const error = ref('')
let revision = 0
const project = computed(() => props.source.snapshot?.projects.find(item => item.id === props.source.projectId))
const session = computed(() => props.source.activity?.session)
const identity = computed(() => JSON.stringify([
  props.source.connection, props.source.stale, props.source.projectId,
  project.value?.id, project.value?.path, project.value?.state,
  props.source.snapshot?.schema_version,
  props.source.status?.build.version, props.source.status?.build.revision,
  props.source.status?.capabilities.includes('control.project-activity.v1'),
  props.source.activity?.project_id, session.value?.id, session.value?.generation,
  session.value?.output.available, session.value?.output.historical,
  props.source.outputFresh, !!props.source.outputError, props.outputResetKey,
]))

// invalidate requires a new review after a connection, selected project, or session changes.
function invalidate() {
  revision += 1
  preview.value = null
  copied.value = false
  error.value = ''
}

// capture freezes the shown report while live logs continue arriving independently.
function capture() {
  invalidate()
  preview.value = supportDiagnostics(props.source, includeOutput.value, new Date().toISOString())
}

watch(identity, invalidate, { flush: 'sync' })
watch(open, (value) => {
  includeOutput.value = false
  if (value) capture()
  else invalidate()
})
watch(includeOutput, () => { if (open.value) capture() })

// copyPreview sends exactly the reviewed text through the existing clipboard bridge.
async function copyPreview() {
  if (!preview.value || copying.value || supportDiagnostics(props.source, false, '') === null) return
  const selectedRevision = revision
  const selectedIdentity = identity.value
  const report = preview.value
  copying.value = true
  error.value = ''
  try {
    await copyText(report)
    if (open.value && revision === selectedRevision && identity.value === selectedIdentity) copied.value = true
  }
  catch {
    if (revision === selectedRevision) error.value = 'Could not copy diagnostics. Select the preview text to copy it manually.'
  }
  finally {
    copying.value = false
  }
}
</script>

<template>
  <Dialog v-model:open="open">
    <DialogTrigger as-child>
      <Button variant="outline" size="sm"><FileSearch class="size-3.5 text-sky-500" aria-hidden="true" />Support diagnostics</Button>
    </DialogTrigger>
    <DialogContent class="flex max-h-[90dvh] flex-col gap-4 sm:max-w-3xl">
      <DialogHeader>
        <DialogTitle class="flex items-center gap-2"><FileSearch class="size-5 text-sky-500" aria-hidden="true" />Support diagnostics</DialogTitle>
        <DialogDescription>Review this local report before copying. Nothing is uploaded.</DialogDescription>
      </DialogHeader>
      <label v-if="source.projectId" class="flex cursor-pointer items-center gap-2 text-sm">
        <Checkbox :model-value="includeOutput" @update:model-value="includeOutput = $event === true" />Include recent development output
      </label>
      <p class="text-xs leading-relaxed text-muted-foreground">Includes selected daemon, networking, and project facts. Environment files are not read. Common credentials and known private paths are redacted. Logs may still contain private values, so review before sharing.</p>
      <pre v-if="preview" tabindex="0" aria-label="Support diagnostics preview" class="min-h-0 flex-1 overflow-auto whitespace-pre-wrap break-words rounded-lg border bg-muted/30 p-4 font-mono text-xs leading-relaxed">{{ preview }}</pre>
      <div v-else role="status" class="rounded-lg border border-dashed p-6 text-sm text-muted-foreground">The source changed or fresh diagnostics are unavailable. Reconnect if needed, then refresh the preview before copying.</div>
      <p v-if="error" role="alert" class="text-sm text-destructive">{{ error }}</p>
      <DialogFooter class="flex-wrap gap-2">
        <DialogClose as-child><Button variant="ghost">Close</Button></DialogClose>
        <Button variant="outline" @click="capture"><RefreshCw class="size-3.5" aria-hidden="true" />Refresh preview</Button>
        <Button :disabled="!preview || copying" @click="copyPreview"><Check v-if="copied" class="size-3.5" aria-hidden="true" /><Clipboard v-else class="size-3.5" aria-hidden="true" />{{ copied ? 'Copied' : 'Copy report' }}</Button>
      </DialogFooter>
    </DialogContent>
  </Dialog>
</template>
