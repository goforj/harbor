<script setup lang="ts">
import { computed, defineAsyncComponent, ref, watch } from 'vue'
import { Check, Clipboard, LoaderCircle, Plus, RefreshCw, Save, Trash2 } from '@lucide/vue'
import { copyText } from '@/bridge/clipboard'
import { harborBridge } from '@/bridge'
import { Button } from '@/components/ui/button'
import { Card, CardContent, CardHeader } from '@/components/ui/card'
import { Input } from '@/components/ui/input'
import type { ProjectEnvironment, ProjectEnvironmentBinding, ProjectEnvironmentFile } from '@/domain/harbor'

const DotenvEditor = defineAsyncComponent(async () => (await import('./DotenvEditor.vue')).default)

const props = defineProps<{
  active: boolean
  projectId: string
  supported: boolean
}>()

interface EnvironmentFileDraft {
  contents: string
  error: string | null
  revision: string
  savedContents: string
  saving: boolean
}

const environment = ref<ProjectEnvironment | null>(null)
const drafts = ref<Record<string, EnvironmentFileDraft>>({})
const environmentOverridesTab = 'environment-overrides'
const selectedTab = ref('')
const loading = ref(false)
const loadError = ref<string | null>(null)
const copiedOverride = ref<string | null>(null)
const bindingDrafts = ref<ProjectEnvironmentBinding[]>([])
const savedBindings = ref<ProjectEnvironmentBinding[]>([])
const bindingsRevision = ref('')
const bindingsSaving = ref(false)
const bindingsError = ref<string | null>(null)
let loadedProjectId = ''

const environmentVariablePattern = /^[A-Za-z_][A-Za-z0-9_]{0,127}$/
const selectedFileName = computed(() => selectedTab.value === environmentOverridesTab ? '' : selectedTab.value)
const selectedDraft = computed(() => drafts.value[selectedFileName.value])
const overridesSelected = computed(() => selectedTab.value === environmentOverridesTab)
const selectedDirty = computed(() => {
  const draft = selectedDraft.value
  return draft != null && draft.contents !== draft.savedContents
})
const bindingsDirty = computed(() => JSON.stringify(normalizedBindings(bindingDrafts.value)) !== JSON.stringify(savedBindings.value))
const bindingsValidationError = computed(() => validateBindings(bindingDrafts.value))
const anyDirty = computed(() => bindingsDirty.value || Object.values(drafts.value).some((draft) => draft.contents !== draft.savedContents))

watch(
  () => [props.active, props.projectId, props.supported] as const,
  ([active, projectId, supported]) => {
    if (loadedProjectId !== '' && projectId !== loadedProjectId) {
      loadedProjectId = ''
      environment.value = null
      drafts.value = {}
      bindingDrafts.value = []
      savedBindings.value = []
      bindingsRevision.value = ''
      bindingsError.value = null
      selectedTab.value = ''
      loadError.value = null
    }
    if (active && supported && projectId && loadedProjectId !== projectId) {
      void loadEnvironment()
    }
  },
  { immediate: true },
)

async function loadEnvironment(force = false) {
  if (!props.projectId || !props.supported || loading.value) return
  if (force && anyDirty.value) return
  loading.value = true
  loadError.value = null
  try {
    const result = await harborBridge.getProjectEnvironment(props.projectId)
    if (result.project_id !== props.projectId) return
    applyEnvironment(result)
    loadedProjectId = props.projectId
  }
  catch (error) {
    loadError.value = error instanceof Error ? error.message : 'Project environment is unavailable.'
  }
  finally {
    loading.value = false
  }
}

function applyEnvironment(result: ProjectEnvironment) {
  environment.value = result
  drafts.value = Object.fromEntries(result.files.map((file) => [
    file.name,
    fileDraft(file),
  ]))
  applyBindings(result)
  const selectedFileStillExists = result.files.some((file) => file.name === selectedTab.value)
  if (selectedTab.value !== environmentOverridesTab && !selectedFileStillExists) {
    selectedTab.value = result.files.find((file) => file.name === '.env')?.name
      ?? result.files[0]?.name
      ?? environmentOverridesTab
  }
}

function applyBindings(result: ProjectEnvironment) {
  const bindings = normalizedBindings(result.bindings ?? [])
  bindingDrafts.value = bindings.map((binding) => ({ ...binding }))
  savedBindings.value = bindings
  bindingsRevision.value = result.bindings_revision ?? ''
  bindingsError.value = null
}

function normalizedBindings(bindings: ProjectEnvironmentBinding[]) {
  return bindings
    .map((binding) => ({ name: binding.name.trim(), source: binding.source }))
    .sort((left, right) => left.name.localeCompare(right.name))
}

function validateBindings(bindings: ProjectEnvironmentBinding[]) {
  const names = new Set<string>()
  for (const binding of bindings) {
    const name = binding.name.trim()
    if (!environmentVariablePattern.test(name)) {
      return 'Variable names must begin with a letter or underscore and contain only letters, numbers, and underscores.'
    }
    if (names.has(name)) return `Variable ${name} is mapped more than once.`
    if (binding.source !== 'project.address') return `Source ${binding.source} is not supported by this Harbor build.`
    names.add(name)
  }
  return null
}

function addBinding() {
  bindingDrafts.value.push({ name: '', source: 'project.address' })
  bindingsError.value = null
}

function removeBinding(index: number) {
  bindingDrafts.value.splice(index, 1)
  bindingsError.value = null
}

function serializeBindings(bindings: ProjectEnvironmentBinding[]) {
  if (bindings.length === 0) return 'version: 1\n\nenvironment: {}\n'
  const entries = bindings.map((binding) => `  ${binding.name}:\n    from: ${binding.source}`).join('\n')
  return `version: 1\n\nenvironment:\n${entries}\n`
}

async function saveBindings() {
  if (bindingsSaving.value || !bindingsDirty.value) return
  const validationError = bindingsValidationError.value
  if (validationError) {
    bindingsError.value = validationError
    return
  }
  const bindings = normalizedBindings(bindingDrafts.value)
  bindingsSaving.value = true
  bindingsError.value = null
  try {
    const saved = await harborBridge.saveProjectEnvironmentFile(
      props.projectId,
      '.harbor.yml',
      serializeBindings(bindings),
      bindingsRevision.value,
    )
    bindingDrafts.value = bindings.map((binding) => ({ ...binding }))
    savedBindings.value = bindings
    bindingsRevision.value = saved.revision
    await refreshEnvironment()
  }
  catch (error) {
    bindingsError.value = error instanceof Error ? error.message : 'Harbor could not save these project mappings.'
  }
  finally {
    bindingsSaving.value = false
  }
}

async function reloadBindings() {
  if (loading.value) return
  await refreshEnvironment()
}

async function refreshEnvironment() {
  loading.value = true
  loadError.value = null
  try {
    const result = await harborBridge.getProjectEnvironment(props.projectId)
    if (result.project_id !== props.projectId) return
    environment.value = result
    applyBindings(result)
    loadedProjectId = props.projectId
  }
  catch (error) {
    bindingsError.value = error instanceof Error ? error.message : 'Harbor could not reload the project mappings.'
  }
  finally {
    loading.value = false
  }
}

function fileDraft(file: ProjectEnvironmentFile): EnvironmentFileDraft {
  return {
    contents: file.contents,
    error: null,
    revision: file.revision,
    savedContents: file.contents,
    saving: false,
  }
}

async function saveSelectedFile() {
  const name = selectedFileName.value
  const draft = drafts.value[name]
  if (!name || !draft || draft.saving || draft.contents === draft.savedContents) return
  draft.saving = true
  draft.error = null
  try {
    const saved = await harborBridge.saveProjectEnvironmentFile(
      props.projectId,
      name,
      draft.contents,
      draft.revision,
    )
    drafts.value[name] = fileDraft(saved)
    if (environment.value) {
      environment.value.files = environment.value.files.map((file) => file.name === name ? saved : file)
    }
  }
  catch (error) {
    draft.error = error instanceof Error ? error.message : 'Harbor could not save this environment file.'
  }
  finally {
    drafts.value[name]!.saving = false
  }
}

async function reloadSelectedFile() {
  const name = selectedFileName.value
  if (!name || loading.value) return
  loading.value = true
  loadError.value = null
  try {
    const result = await harborBridge.getProjectEnvironment(props.projectId)
    const file = result.files.find((candidate) => candidate.name === name)
    if (!file) {
      applyEnvironment(result)
      return
    }
    environment.value = result
    drafts.value[name] = fileDraft(file)
    loadedProjectId = props.projectId
  }
  catch (error) {
    const draft = drafts.value[name]
    if (draft) draft.error = error instanceof Error ? error.message : 'Harbor could not reload this environment file.'
  }
  finally {
    loading.value = false
  }
}

async function copyOverride(name: string, value: string) {
  try {
    await copyText(value)
    copiedOverride.value = name
    window.setTimeout(() => {
      if (copiedOverride.value === name) copiedOverride.value = null
    }, 1500)
  }
  catch {
    copiedOverride.value = null
  }
}
</script>

<template>
  <div class="flex min-h-0 flex-col">
    <Card class="min-h-0 flex-1 gap-0 overflow-hidden rounded-lg py-0 shadow-none">
      <CardHeader class="border-b px-4 py-0">
        <div class="flex h-11 items-center gap-5 overflow-x-auto" role="tablist" aria-label="Project environment">
          <button
            v-for="file in environment?.files ?? []"
            :key="file.name"
            type="button"
            role="tab"
            class="h-11 flex-none border-b-2 border-transparent text-sm font-medium text-muted-foreground hover:text-foreground"
            :class="selectedTab === file.name ? '!border-primary text-primary' : ''"
            :aria-selected="selectedTab === file.name"
            @click="selectedTab = file.name"
          >
            {{ file.name }}<span v-if="drafts[file.name]?.contents !== drafts[file.name]?.savedContents" class="ml-1 text-primary">●</span>
          </button>
          <button
            type="button"
            role="tab"
            class="h-11 flex-none border-b-2 border-transparent text-sm font-medium text-muted-foreground hover:text-foreground"
            :class="overridesSelected ? '!border-primary text-primary' : ''"
            :aria-selected="overridesSelected"
            @click="selectedTab = environmentOverridesTab"
          >
            Environment overrides
          </button>
        </div>
      </CardHeader>
      <CardContent class="flex min-h-0 flex-1 flex-col p-0">
        <p v-if="!supported" class="px-4 py-6 text-sm text-muted-foreground">Restart Harbor to use the project environment view.</p>
        <p v-else-if="loadError" class="px-4 py-3 text-sm text-destructive">{{ loadError }}</p>
        <div v-else-if="overridesSelected" class="flex min-h-0 flex-1 flex-col">
          <div class="min-h-0 flex-1 overflow-auto">
            <section class="border-b">
              <div class="flex items-center justify-between gap-3 border-b px-4 py-3">
                <div>
                  <h3 class="text-sm font-semibold">Project mappings</h3>
                  <p class="mt-0.5 text-xs text-muted-foreground">Map application variables to values Harbor knows about this project.</p>
                </div>
                <Button variant="outline" size="sm" @click="addBinding">
                  <Plus class="size-3.5" />
                  Add mapping
                </Button>
              </div>
              <div v-if="bindingDrafts.length" class="divide-y">
                <div
                  v-for="(binding, index) in bindingDrafts"
                  :key="index"
                  class="grid items-center gap-3 px-4 py-3 sm:grid-cols-[minmax(12rem,0.8fr)_minmax(12rem,1fr)_auto]"
                >
                  <div>
                    <label :for="`binding-name-${index}`" class="mb-1 block text-[11px] font-medium text-muted-foreground">Environment variable</label>
                    <Input
                      :id="`binding-name-${index}`"
                      v-model="binding.name"
                      placeholder="MEILISEARCH_HOST"
                      autocomplete="off"
                      autocapitalize="off"
                      spellcheck="false"
                      :aria-invalid="binding.name !== '' && !environmentVariablePattern.test(binding.name.trim())"
                    />
                  </div>
                  <div>
                    <label :for="`binding-source-${index}`" class="mb-1 block text-[11px] font-medium text-muted-foreground">Harbor value</label>
                    <select
                      :id="`binding-source-${index}`"
                      v-model="binding.source"
                      class="border-input bg-background focus-visible:border-ring focus-visible:ring-ring/50 h-9 w-full rounded-md border px-3 text-sm outline-none focus-visible:ring-[3px]"
                    >
                      <option value="project.address">Project address</option>
                    </select>
                  </div>
                  <Button
                    variant="ghost"
                    size="icon"
                    class="self-end text-muted-foreground hover:text-destructive"
                    :aria-label="`Remove ${binding.name || 'mapping'}`"
                    @click="removeBinding(index)"
                  >
                    <Trash2 class="size-4" />
                  </Button>
                </div>
              </div>
              <p v-else class="px-4 py-5 text-sm text-muted-foreground">No repository mappings yet. Add one when an application variable should use a Harbor-provided value.</p>
              <p v-if="bindingsError || bindingsValidationError" class="border-t px-4 py-2 text-xs text-destructive">{{ bindingsError || bindingsValidationError }}</p>
              <div class="flex items-center justify-between gap-3 border-t px-4 py-2">
                <p class="text-xs text-muted-foreground">
                  Stored in <code>.harbor.yml</code>
                  <span v-if="bindingsDirty"> · Unsaved changes</span>
                </p>
                <div class="flex items-center gap-2">
                  <Button variant="ghost" size="sm" :disabled="loading" @click="reloadBindings">
                    <RefreshCw class="size-3.5" />
                    Reload
                  </Button>
                  <Button size="sm" :disabled="!bindingsDirty || bindingsSaving || bindingsValidationError != null" @click="saveBindings">
                    <LoaderCircle v-if="bindingsSaving" class="size-3.5 animate-spin" />
                    <Save v-else class="size-3.5" />
                    {{ bindingsSaving ? 'Saving…' : 'Save mappings' }}
                  </Button>
                </div>
              </div>
            </section>
            <section>
              <div class="flex items-center justify-between gap-3 border-b px-4 py-3">
                <div>
                  <h3 class="text-sm font-semibold">Effective read-only values</h3>
                  <p class="mt-0.5 text-xs text-muted-foreground">Values Harbor supplies after project dotenv files load.</p>
                </div>
                <Button
                  variant="ghost"
                  size="sm"
                  :disabled="loading || anyDirty"
                  :title="anyDirty ? 'Save or reload edits before refreshing.' : 'Refresh environment'"
                  @click="loadEnvironment(true)"
                >
                  <LoaderCircle v-if="loading" class="size-3.5 animate-spin" />
                  <RefreshCw v-else class="size-3.5" />
                  Refresh
                </Button>
              </div>
              <div v-if="environment?.overrides_available && environment.overrides.length" class="divide-y">
                <div v-for="override in environment.overrides" :key="override.name" class="grid items-center gap-2 px-4 py-2.5 sm:grid-cols-[minmax(12rem,0.35fr)_minmax(0,1fr)_auto]">
                  <div class="min-w-0">
                    <code class="block truncate text-xs font-semibold">{{ override.name }}</code>
                    <span class="mt-0.5 block truncate text-[11px] text-muted-foreground">{{ override.source }}</span>
                  </div>
                  <code class="min-w-0 break-all text-xs text-muted-foreground">{{ override.value }}</code>
                  <Button variant="ghost" size="sm" class="justify-self-start" @click="copyOverride(override.name, override.value)">
                    <Check v-if="copiedOverride === override.name" class="size-3.5" />
                    <Clipboard v-else class="size-3.5" />
                    {{ copiedOverride === override.name ? 'Copied' : 'Copy' }}
                  </Button>
                </div>
              </div>
              <p v-else-if="environment?.override_error" class="px-4 py-3 text-sm text-destructive">{{ environment.override_error }}</p>
              <p v-else class="px-4 py-6 text-sm text-muted-foreground">Harbor has not assigned this project a runtime address yet. Start it once to see its overrides.</p>
            </section>
          </div>
        </div>
        <div v-else-if="selectedDraft" class="flex min-h-0 flex-1 flex-col">
          <div class="flex items-center justify-between gap-3 border-b px-4 py-2">
            <p class="text-xs text-muted-foreground">
              Editing <code>{{ selectedFileName }}</code>
              <span v-if="selectedDirty"> · Unsaved changes</span>
            </p>
            <div class="flex items-center gap-2">
              <Button variant="ghost" size="sm" :disabled="loading" @click="reloadSelectedFile">
                <RefreshCw class="size-3.5" />
                Reload
              </Button>
              <Button size="sm" :disabled="!selectedDirty || selectedDraft.saving" @click="saveSelectedFile">
                <LoaderCircle v-if="selectedDraft.saving" class="size-3.5 animate-spin" />
                <Save v-else class="size-3.5" />
                {{ selectedDraft.saving ? 'Saving…' : 'Save' }}
              </Button>
            </div>
          </div>
          <p v-if="selectedDraft.error" class="border-b px-4 py-2 text-xs text-destructive">{{ selectedDraft.error }}</p>
          <DotenvEditor
            v-model="selectedDraft.contents"
            :label="`${selectedFileName} contents`"
          />
        </div>
        <p v-else class="px-4 py-10 text-center text-sm text-muted-foreground">No direct <code>.env</code> files were found in this project.</p>
      </CardContent>
    </Card>
  </div>
</template>
