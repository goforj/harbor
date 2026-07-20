<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { harborBridge } from '@/bridge'

const props = defineProps<{
  name: string
  url: string
  projectId: string
  resourceId: string
}>()

const failed = ref(false)
const candidateIndex = ref(0)
const declaredFaviconURL = ref('')
const faviconURLs = computed(() => {
  try {
    const origin = new URL(props.url).origin
    return [
      '/logo.png',
      '/logo.svg',
      '/favicon.ico',
      '/favicon.png',
      '/favicon.svg',
      '/public/img/fav32.png',
      '/static/favicon.ico',
      '/img/favicon.ico',
    ].map((path) => new URL(path, origin).toString())
  }
  catch {
    return []
  }
})
const faviconURL = computed(() => faviconURLs.value[candidateIndex.value] ?? '')
const displayedFaviconURL = computed(() => declaredFaviconURL.value || faviconURL.value)
const fallbackLabel = computed(() => props.name.trim().slice(0, 1).toLocaleUpperCase() || '?')

watch(() => [props.url, props.projectId, props.resourceId], () => {
  failed.value = false
  candidateIndex.value = 0
  declaredFaviconURL.value = ''
  void discoverDeclaredFavicon()
}, { immediate: true })

// tryNextCandidate supports common local service icon conventions without adding a remote logo catalog.
function tryNextCandidate() {
  if (declaredFaviconURL.value) {
    declaredFaviconURL.value = ''
    return
  }
  if (candidateIndex.value + 1 < faviconURLs.value.length) {
    candidateIndex.value += 1
    return
  }
  failed.value = true
}

// discoverDeclaredFavicon asks the desktop backend so resource-page metadata works without webview CORS access.
async function discoverDeclaredFavicon() {
  try {
    declaredFaviconURL.value = await harborBridge.getResourceIconURL(props.projectId, props.resourceId)
  }
  catch {
    // The image candidates below keep local services useful while the daemon or resource is unavailable.
  }
}
</script>

<template>
  <img
    v-if="displayedFaviconURL && !failed"
    :src="displayedFaviconURL"
    :alt="''"
    class="size-8 shrink-0 rounded-md object-contain"
    aria-hidden="true"
    @error="tryNextCandidate"
  >
  <span
    v-else
    class="inline-flex size-8 shrink-0 items-center justify-center rounded-md border bg-muted/40 text-xs font-semibold text-muted-foreground"
    aria-hidden="true"
  >{{ fallbackLabel }}</span>
</template>
