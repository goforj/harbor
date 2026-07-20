<script setup lang="ts">
import { computed, ref, watch } from 'vue'

const props = defineProps<{
  name: string
  url: string
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

watch(() => props.url, () => {
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

// discoverDeclaredFavicon honors a resource's own icon metadata before falling back to common conventions.
async function discoverDeclaredFavicon() {
  try {
    const response = await fetch(props.url, { headers: { Accept: 'text/html' } })
    if (!response.ok) return
    const document = new DOMParser().parseFromString(await response.text(), 'text/html')
    const link = document.querySelector('link[rel~="icon" i], link[rel~="apple-touch-icon" i]')
    const href = link?.getAttribute('href')
    if (href) declaredFaviconURL.value = new URL(href, props.url).toString()
  }
  catch {
    // The image candidates below keep local services useful when browser CORS blocks metadata inspection.
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
