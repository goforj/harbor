<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { Folder } from '@lucide/vue'
import { harborBridge } from '@/bridge'
import goforjIcon from '@/assets/brand/goforj-ios.png'
import KindIcon from './KindIcon.vue'

const props = withDefaults(defineProps<{
  name: string
  url: string
  projectId: string
  resourceId: string
  compact?: boolean
  fallback?: 'initial' | 'folder' | 'goforj'
  kind?: string
}>(), {
  compact: false,
  fallback: 'initial',
})

const declaredFaviconURL = ref('')
const fallbackLabel = computed(() => props.name.trim().slice(0, 1).toLocaleUpperCase() || '?')
let lookupGeneration = 0

function sameOriginIconURL(iconURL: string, resourceURL: string) {
  try {
    const resourceOrigin = new URL(resourceURL).origin
    const parsedIconURL = new URL(iconURL.trim())
    return parsedIconURL.origin === resourceOrigin ? parsedIconURL.toString() : ''
  }
  catch {
    return ''
  }
}

watch(() => [props.url, props.projectId, props.resourceId], () => {
  const generation = ++lookupGeneration
  const { projectId, resourceId, url } = props
  declaredFaviconURL.value = ''
  void discoverDeclaredFavicon(generation, projectId, resourceId, url)
}, { immediate: true })

// discoverDeclaredFavicon asks the desktop backend so resource-page metadata works without webview CORS access.
async function discoverDeclaredFavicon(generation: number, projectId: string, resourceId: string, resourceURL: string) {
  try {
    const iconURL = await harborBridge.getResourceIconURL(projectId, resourceId)
    if (generation === lookupGeneration) {
      declaredFaviconURL.value = sameOriginIconURL(iconURL, resourceURL)
    }
  }
  catch {
    if (generation === lookupGeneration) {
      declaredFaviconURL.value = ''
    }
  }
}

function clearFailedFavicon(event: Event) {
  const image = event.currentTarget
  if (image instanceof HTMLImageElement && image.src === declaredFaviconURL.value) {
    declaredFaviconURL.value = ''
  }
}
</script>

<template>
  <img
    v-if="declaredFaviconURL"
    :src="declaredFaviconURL"
    :alt="''"
    :class="[props.compact ? 'size-6' : 'size-8', 'shrink-0 rounded-md object-contain']"
    aria-hidden="true"
    @error="clearFailedFavicon"
  >
  <img
    v-else-if="props.fallback === 'goforj'"
    :src="goforjIcon"
    alt=""
    aria-hidden="true"
    :class="[props.compact ? 'size-6' : 'size-8', 'shrink-0 rounded-[22%] object-contain']"
  >
  <KindIcon v-else-if="props.kind" :kind="props.kind" :compact="props.compact" />
  <span
    v-else
    :class="[props.compact ? 'size-6' : 'size-8', 'inline-flex shrink-0 items-center justify-center rounded-md border bg-muted/40 text-xs font-semibold text-muted-foreground']"
    aria-hidden="true"
  ><Folder v-if="props.fallback === 'folder'" class="size-3.5" /><template v-else>{{ fallbackLabel }}</template></span>
</template>
