<script setup lang="ts">
import { computed, defineAsyncComponent, ref, watch } from 'vue'
import { Check, Clipboard, LoaderCircle, RefreshCw, Save } from '@lucide/vue'
import { copyText } from '@/bridge/clipboard'
import { harborBridge } from '@/bridge'
import { Button } from '@/components/ui/button'
import { Card, CardContent, CardHeader } from '@/components/ui/card'
import type { ProjectEnvironment, ProjectEnvironmentFile } from '@/domain/harbor'

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
let loadedProjectId = ''

const selectedFileName = computed(() => selectedTab.value === environmentOverridesTab ? '' : selectedTab.value)
const selectedDraft = computed(() => drafts.value[selectedFileName.value])
const overridesSelected = computed(() => selectedTab.value === environmentOverridesTab)
const selectedDirty = computed(() => {
  const draft = selectedDraft.value
  return draft != null && draft.contents !== draft.savedContents
})
const anyDirty = computed(() => Object.values(drafts.value).some((draft) => draft.contents !== draft.savedContents))

watch(
  () => [props.active, props.projectId, props.supported] as const,
  ([active, projectId, supported]) => {
    if (loadedProjectId !== '' && projectId !== loadedProjectId) {
      loadedProjectId = ''
      environment.value = null
      drafts.value = {}
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
  const selectedFileStillExists = result.files.some((file) => file.name === selectedTab.value)
  if (selectedTab.value !== environmentOverridesTab && !selectedFileStillExists) {
    selectedTab.value = result.files.find((file) => file.name === '.env')?.name
      ?? result.files[0]?.name
      ?? environmentOverridesTab
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
          <div class="flex items-center justify-between gap-3 border-b px-4 py-2">
            <p class="text-xs text-muted-foreground">Read-only values Harbor supplies after project dotenv files load.</p>
            <Button
              variant="ghost"
              size="sm"
              :disabled="loading || anyDirty"
              :title="anyDirty ? 'Save or reload edited files before refreshing.' : 'Refresh environment'"
              @click="loadEnvironment(true)"
            >
              <LoaderCircle v-if="loading" class="size-3.5 animate-spin" />
              <RefreshCw v-else class="size-3.5" />
              Refresh
            </Button>
          </div>
          <div v-if="environment?.overrides_available && environment.overrides.length" class="min-h-0 flex-1 divide-y overflow-auto">
            <div v-for="override in environment.overrides" :key="override.name" class="grid items-center gap-2 px-4 py-2.5 sm:grid-cols-[minmax(12rem,0.35fr)_minmax(0,1fr)_auto]">
              <code class="truncate text-xs font-semibold">{{ override.name }}</code>
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
