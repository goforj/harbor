<script setup lang="ts">
import { computed, onBeforeUnmount, ref, watch } from 'vue'
import { useRouter } from 'vue-router'
import { ArrowLeft, ArrowRight, Check, ChevronDown, FolderOpen, FolderPlus, Layers, Plus, RotateCw } from '@lucide/vue'
import goforjIcon from '@/assets/brand/goforj-ios.png'
import { Select, SelectContent, SelectItem, SelectTrigger } from '@/components/ui/select'
import { harborBridge } from '@/bridge'
import type { ProjectCreationField, ProjectCreationSchema, ProjectCreationResult, ProjectCreationProgress, ProjectCreationProvider } from '@/domain/harbor'
import { creationDefault, creationFieldAvailable, toggleCreationOption } from '@/lib/projectCreation'
import { useHarborStore } from '@/stores/harbor'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Spinner } from '@/components/ui/spinner'
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from '@/components/ui/dialog'
import CreationOptionPreview from './CreationOptionPreview.vue'

const open = defineModel<boolean>('open', { default: false })
const store = useHarborStore()
const router = useRouter()
const providers = ref<ProjectCreationProvider[]>([])
const providerId = ref('')
const providerArtwork: Record<string, string> = { goforj: goforjIcon }
const selectedProvider = computed(() => providers.value.find(provider => provider.id === providerId.value))
const progress = ref<ProjectCreationProgress | null>(null)
const output = ref('')
const observingError = ref('')
let observation = 0
const schema = ref<ProjectCreationSchema | null>(null)
const values = ref<Record<string, string>>({})
const loading = ref(false)
const creating = ref(false)
const error = ref('')
const step = ref(0)
const result = ref<ProjectCreationResult | null>(null)
const steps = computed(() => schema.value ? [...new Set(schema.value.fields.map(field => field.step)), 'Review'] : [])
const fields = computed(() => schema.value?.fields.filter(field => field.step === steps.value[step.value]) ?? [])
const reviewing = computed(() => step.value === steps.value.length - 1)
const phaseLabel = computed(() => ({ creating: 'Creating project files', registering: 'Adding project to Harbor', completed: 'Project ready', failed: 'Creation needs attention', registration_failed: 'Files created, registration needs attention' })[progress.value?.phase ?? 'creating'])

watch(open, async value => {
  if (!value) return
  loading.value = true
  schema.value = null
  result.value = null
  error.value = ''
  step.value = 0
  try {
    progress.value = null
    output.value = ''
    observingError.value = ''
    providers.value = await harborBridge.getProjectCreationProviders()
    if (!providers.value.length) throw new Error('No project creators are available.')
    providerId.value = providers.value[0]!.id
    await loadSchema()
  } catch (cause) { error.value = String(cause instanceof Error ? cause.message : cause) }
  finally { loading.value = false }
})

async function loadSchema() {
  loading.value = true
  schema.value = null
  error.value = ''
  step.value = 0
  try {
    const loaded = await harborBridge.getProjectCreationSchema(providerId.value)
    if (loaded.schema_version !== 1 || loaded.provider_id !== providerId.value) throw new Error('This project wizard needs a newer Harbor version.')
    schema.value = loaded
    values.value = Object.fromEntries(loaded.fields.map(field => [field.key, creationDefault(field)]))
  } catch (cause) { error.value = message(cause) }
  finally { loading.value = false }
}

function message(cause: unknown) { return String(cause instanceof Error ? cause.message : cause) }
function available(field: ProjectCreationField) { return creationFieldAvailable(field, values.value) }
function toggleOption(field: ProjectCreationField, value: string, selected: boolean) {
  toggleCreationOption(field, value, selected, values.value, schema.value?.fields ?? [])
}

async function chooseDirectory(field: ProjectCreationField) {
  try {
    const parent = await harborBridge.chooseProjectParent()
    if (parent) {
      const nameField = schema.value?.fields.find(candidate => candidate.role === 'name')
      const slug = (values.value[nameField?.key ?? ''] ?? '').toLowerCase().replace(/[^a-z0-9]+/g, '-').replace(/^-|-$/g, '') || 'my-project'
      const separator = parent.includes('\\') ? '\\' : '/'
      values.value[field.key] = `${parent.replace(/[\\/]$/, '')}${separator}${slug}`
    }
  } catch (cause) { error.value = String(cause instanceof Error ? cause.message : cause) }
}

function next() {
  const missing = fields.value.find(field => available(field) && field.required && !values.value[field.key]?.trim())
  if (missing) { error.value = `${missing.label} is required.`; return }
  error.value = ''
  step.value += 1
}

function summary(field: ProjectCreationField) {
  const value = values.value[field.key] ?? ''
  if (field.kind === 'boolean') return value === 'true' ? 'Included' : 'Off'
  if (field.kind === 'multi') return value.split(',').filter(Boolean).map(key => field.options?.find(option => option.value === key)?.label ?? key).join(', ') || 'None'
  return field.options?.find(option => option.value === value)?.label ?? value
}

async function acceptProgress(update: ProjectCreationProgress) {
  progress.value = update
  output.value = ((update.truncated ? '' : output.value) + update.output).slice(-256 * 1024)
  result.value = update.result ?? null
  creating.value = update.phase === 'creating' || update.phase === 'registering'
  if (update.error) error.value = update.error
  if (update.result?.registration) {
    await store.refresh()
    open.value = false
    await router.push(`/projects/${encodeURIComponent(update.result.registration.project.id)}`)
  }
}

async function observe() {
  const token = ++observation
  observingError.value = ''
  try {
    while (creating.value && progress.value && token === observation) {
      await new Promise(resolve => setTimeout(resolve, 400))
      if (token !== observation) return
      const update = await harborBridge.getProjectCreationProgress(progress.value.job_id, progress.value.cursor)
      if (token !== observation) return
      await acceptProgress(update)
    }
  } catch (cause) {
    if (token === observation) observingError.value = `Output updates paused: ${message(cause)}. The creation job may still be running.`
  }
}

async function create() {
  if (!schema.value || creating.value) return
  creating.value = true
  error.value = ''
  output.value = ''
  try {
    await acceptProgress(await harborBridge.startProjectCreation({ provider_id: providerId.value, schema_version: schema.value.schema_version, options: { ...values.value } }))
    await observe()
  } catch (cause) { error.value = message(cause); creating.value = false }
}

async function retryRegistration() {
  if (!progress.value || creating.value) return
  creating.value = true
  try {
    // Retry returns a snapshot from cursor zero; keep one copy of the retained output.
    output.value = ''
    await acceptProgress(await harborBridge.retryProjectRegistration(progress.value.job_id))
    await observe()
  } catch (cause) { error.value = message(cause); creating.value = false }
}

function reviewChoices() {
  progress.value = null
  result.value = null
  error.value = ''
  step.value = 0
}

onBeforeUnmount(() => { observation++ })
</script>

<template>
  <Dialog :open="open" @update:open="value => { if (!creating) open = value }">
    <DialogContent class="flex max-h-[85dvh] flex-col gap-0 overflow-hidden p-0 sm:max-w-2xl" :show-close-button="!creating">
      <DialogHeader class="border-b px-6 py-5 text-left">
        <DialogTitle class="flex items-center gap-2.5 text-lg"><span class="flex size-8 items-center justify-center rounded-lg bg-primary/10 text-primary"><FolderPlus class="size-4" aria-hidden="true" /></span>New project</DialogTitle>
        <DialogDescription>Create a project and add it to Harbor.</DialogDescription>
      </DialogHeader>
      <div v-if="selectedProvider && !progress" class="px-6 pt-5 pb-4">
        <p id="project-stack-label" class="mb-2 text-xs font-medium text-muted-foreground">Technology stack</p>
        <Select v-if="providers.length > 1" v-model="providerId" :disabled="creating || loading" @update:model-value="loadSchema">
          <SelectTrigger aria-labelledby="project-stack-label" class="h-auto! w-full rounded-xl border-border bg-muted/20 px-4 py-3 text-left whitespace-normal">
            <span class="flex min-w-0 items-center gap-3">
              <img v-if="providerArtwork[providerId]" :src="providerArtwork[providerId]" alt="" class="size-10 shrink-0 rounded-[22%]" />
              <Layers v-else class="size-8 shrink-0 text-primary" aria-hidden="true" />
              <span class="min-w-0"><span class="block font-semibold">{{ selectedProvider.name }}</span><span class="mt-0.5 block text-xs leading-relaxed text-muted-foreground">{{ selectedProvider.description }}</span></span>
            </span>
          </SelectTrigger>
          <SelectContent>
            <SelectItem v-for="provider in providers" :key="provider.id" :value="provider.id" class="py-3">
              <img v-if="providerArtwork[provider.id]" :src="providerArtwork[provider.id]" alt="" class="size-7 rounded-[22%]" />
              <Layers v-else class="size-5 text-primary" aria-hidden="true" />{{ provider.name }}
            </SelectItem>
          </SelectContent>
        </Select>
        <div v-else class="flex items-center gap-3.5 rounded-xl border border-primary/25 bg-primary/[0.035] px-4 py-3.5" aria-labelledby="project-stack-label" data-testid="selected-project-stack">
          <img v-if="providerArtwork[providerId]" :src="providerArtwork[providerId]" :alt="selectedProvider.name" class="size-11 shrink-0 rounded-[22%]" />
          <Layers v-else class="size-9 shrink-0 text-primary" aria-hidden="true" />
          <div class="min-w-0 flex-1"><p class="text-sm font-semibold">{{ selectedProvider.name }}</p><p class="mt-0.5 text-xs leading-relaxed text-muted-foreground">{{ selectedProvider.description }}</p></div>
          <Check class="size-4 shrink-0 text-primary" aria-hidden="true" />
        </div>
      </div>
      <div v-if="loading" class="flex items-center gap-3 px-6 py-12 text-sm text-muted-foreground"><Spinner />Loading project options…</div>
      <template v-else-if="schema">
        <ol class="flex items-center gap-2 border-b px-6 pb-4 text-xs" :class="progress ? 'pt-4' : ''" aria-label="Creation steps">
          <li v-for="(title, index) in steps" :key="title" class="flex min-w-0 flex-1 items-center gap-2 last:flex-none" :aria-current="index === step ? 'step' : undefined">
            <span class="flex size-5 shrink-0 items-center justify-center rounded-full border text-[10px] font-medium" :class="index === step ? 'border-primary bg-primary/10 text-primary' : index < step ? 'border-status-ready/25 bg-status-ready/10 text-status-ready' : 'border-border text-muted-foreground'">
              <Check v-if="index < step" class="size-3" aria-hidden="true" /><span v-else>{{ index + 1 }}</span>
            </span>
            <span :class="index === step ? 'font-medium text-foreground' : 'text-muted-foreground'">{{ title }}</span>
            <span v-if="index < steps.length - 1" class="ml-1 hidden h-px min-w-2 flex-1 bg-border sm:block" aria-hidden="true" />
          </li>
        </ol>
        <div v-if="progress" class="min-h-0 space-y-4 overflow-y-auto px-6 py-5">
          <div class="flex items-center gap-3"><Spinner v-if="creating" /><Check v-else-if="result" class="size-5 text-status-ready" /><div><p class="text-sm font-medium" role="status">{{ phaseLabel }}</p><p class="mt-1 break-all text-xs text-muted-foreground">{{ progress.path }}</p></div></div>
          <ol class="grid grid-cols-2 gap-3 text-xs" aria-label="Creation progress">
            <li class="rounded-md border px-3 py-2" :class="progress.phase !== 'creating' && progress.phase !== 'failed' ? 'text-status-ready' : 'text-foreground'">1. Create files</li>
            <li class="rounded-md border px-3 py-2" :class="progress.phase === 'registering' ? 'text-foreground' : 'text-muted-foreground'">2. Add to Harbor</li>
          </ol>
          <p v-if="result?.registration_error" class="text-sm text-destructive" role="alert">{{ result.registration_error }}</p>
          <details :open="progress.phase === 'failed'" class="rounded-md border bg-muted/30">
            <summary class="flex cursor-pointer items-center justify-between px-3 py-2 text-xs text-muted-foreground">Command output<ChevronDown class="size-3.5" /></summary>
            <pre class="max-h-64 overflow-auto whitespace-pre-wrap break-words border-t p-3 font-mono text-xs leading-5" aria-label="Creation output">{{ output || 'Waiting for command output…' }}</pre>
          </details>
          <p v-if="creating" class="text-xs text-muted-foreground">Dependency installation can take a few minutes. Output updates as the command runs.</p>
          <div v-if="observingError" class="space-y-2 text-xs"><p role="alert">{{ observingError }}</p><Button size="sm" variant="outline" @click="observe"><RotateCw class="size-3" />Resume updates</Button></div>
        </div>
        <div v-else class="min-h-0 space-y-5 overflow-y-auto px-6 py-5">
          <dl v-if="reviewing" class="space-y-4 text-sm">
            <div v-for="field in schema.fields.filter(available)" :key="field.key" class="grid grid-cols-[8rem_minmax(0,1fr)] gap-3"><dt class="text-muted-foreground">{{ field.label }}</dt><dd class="break-words">{{ summary(field) }}</dd></div>
            <p class="border-t pt-4 text-xs text-muted-foreground">The project will be added in a stopped state. Start it when you're ready.</p>
          </dl>
          <template v-else v-for="field in fields" :key="field.key">
            <fieldset v-if="available(field)" class="min-w-0 space-y-2">
              <legend class="text-sm font-medium">{{ field.label }}</legend>
              <p v-if="field.description" class="text-xs leading-relaxed text-muted-foreground">{{ field.description }}</p>
              <div v-if="field.kind === 'text' || field.kind === 'path'" class="flex gap-2">
                <Input v-model="values[field.key]" :aria-label="field.label" :required="field.required" :placeholder="field.role === 'name' ? 'My application' : field.role === 'directory' ? 'Choose a project location' : undefined" class="h-10 min-w-0" :class="field.kind === 'path' ? 'font-mono text-xs' : ''" />
                <Button v-if="field.kind === 'path'" variant="outline" class="size-10 shrink-0" aria-label="Choose project location" @click="chooseDirectory(field)"><FolderOpen class="size-4" /></Button>
              </div>
              <div v-else-if="field.kind === 'select'">
                <select v-model="values[field.key]" :aria-label="field.label" class="h-9 w-full rounded-md border border-input bg-background px-3 text-sm">
                  <option v-for="option in field.options" :key="option.value" :value="option.value">{{ option.label }}</option>
                </select>
                <CreationOptionPreview :label="field.label" :option="field.options?.find(option => option.value === values[field.key])" />
              </div>
              <label v-else-if="field.kind === 'boolean'" class="flex items-center gap-2 text-sm"><input type="checkbox" :aria-label="field.label" :checked="values[field.key] === 'true'" class="size-4 accent-primary" @change="values[field.key] = ($event.target as HTMLInputElement).checked ? 'true' : 'false'">Include</label>
              <div v-else-if="field.kind === 'multi'" class="grid gap-2 sm:grid-cols-2">
                <label v-for="option in field.options" :key="option.value" class="flex cursor-pointer items-start gap-2 rounded-md border px-3 py-2" :class="values[field.key]?.split(',').includes(option.value) ? 'border-primary/50 bg-primary/5' : 'border-border'">
                  <input type="checkbox" :aria-label="option.label" :checked="values[field.key]?.split(',').includes(option.value)" class="mt-0.5 size-4 shrink-0 accent-primary" @change="toggleOption(field, option.value, ($event.target as HTMLInputElement).checked)">
                  <span><span class="text-sm font-medium">{{ option.label }}</span><span class="mt-0.5 block text-xs text-muted-foreground">{{ option.description }}</span></span>
                </label>
              </div>
            </fieldset>
          </template>
        </div>
      </template>
      <pre v-if="error" class="max-h-40 overflow-auto whitespace-pre-wrap break-words border-t px-6 py-3 font-sans text-xs text-destructive" role="alert">{{ error }}</pre>
      <DialogFooter v-if="!creating" class="border-t bg-muted/15 px-6 py-4">
        <Button variant="ghost" @click="open = false">{{ result ? 'Close' : 'Cancel' }}</Button>
        <Button v-if="progress?.phase === 'registration_failed'" @click="retryRegistration"><RotateCw class="size-3.5" />Retry registration</Button>
        <Button v-if="progress?.phase === 'failed'" variant="outline" @click="reviewChoices">Review choices</Button>
        <Button v-if="schema && !progress && step > 0" variant="outline" @click="step--; error = ''"><ArrowLeft class="size-3.5" />Back</Button>
        <Button v-if="schema && !progress && !reviewing" @click="next">Continue<ArrowRight class="size-3.5" /></Button>
        <Button v-if="schema && !progress && reviewing" @click="create"><Plus class="size-3.5" />Create project</Button>
      </DialogFooter>
    </DialogContent>
  </Dialog>
</template>
