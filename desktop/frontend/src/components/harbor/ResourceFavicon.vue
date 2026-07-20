<script setup lang="ts">
import { computed, ref, watch } from 'vue'

const props = defineProps<{
  name: string
  url: string
}>()

const failed = ref(false)
const faviconURL = computed(() => {
  try {
    return new URL('/favicon.ico', new URL(props.url).origin).toString()
  }
  catch {
    return ''
  }
})
const fallbackLabel = computed(() => props.name.trim().slice(0, 1).toLocaleUpperCase() || '?')

watch(() => props.url, () => {
  failed.value = false
})
</script>

<template>
  <img
    v-if="faviconURL && !failed"
    :src="faviconURL"
    :alt="''"
    class="size-8 shrink-0 rounded-md object-contain"
    aria-hidden="true"
    @error="failed = true"
  >
  <span
    v-else
    class="inline-flex size-8 shrink-0 items-center justify-center rounded-md border bg-muted/40 text-xs font-semibold text-muted-foreground"
    aria-hidden="true"
  >{{ fallbackLabel }}</span>
</template>
