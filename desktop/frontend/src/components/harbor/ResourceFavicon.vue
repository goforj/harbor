<script setup lang="ts">
import { computed, ref, watch } from 'vue'

const props = defineProps<{
  name: string
  url: string
}>()

const failed = ref(false)
const candidateIndex = ref(0)
const faviconURLs = computed(() => {
  try {
    const origin = new URL(props.url).origin
    return [
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
const fallbackLabel = computed(() => props.name.trim().slice(0, 1).toLocaleUpperCase() || '?')

watch(() => props.url, () => {
  failed.value = false
  candidateIndex.value = 0
})

// tryNextCandidate supports common local service icon conventions without adding a remote logo catalog.
function tryNextCandidate() {
  if (candidateIndex.value + 1 < faviconURLs.value.length) {
    candidateIndex.value += 1
    return
  }
  failed.value = true
}
</script>

<template>
  <img
    v-if="faviconURL && !failed"
    :src="faviconURL"
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
