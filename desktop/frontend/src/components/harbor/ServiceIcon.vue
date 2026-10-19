<script setup lang="ts">
import { computed } from 'vue'
import type { ServiceSnapshot } from '@/domain/harbor'
import { serviceArtwork } from '@/lib/serviceArtwork'
import { presentationIcon } from '@/lib/presentationIcons'

const props = defineProps<{ service: Pick<ServiceSnapshot, 'id' | 'name' | 'kind'> & { image?: string }; compact?: boolean; inline?: boolean }>()
const artwork = computed(() => serviceArtwork(props.service))
const fallback = computed(() => presentationIcon(props.service.kind))
</script>

<template>
  <span class="inline-flex shrink-0 items-center justify-center" :class="inline ? 'size-4 rounded-[3px]' : ['rounded-lg border', compact ? 'size-8' : 'size-10', artwork ? 'service-brand-icon' : 'kind-icon']" :style="artwork ? { '--service-brand': artwork.color, backgroundColor: artwork.background } : undefined" :data-service-brand="artwork?.id" :data-tone="artwork ? undefined : fallback.tone" aria-hidden="true">
    <img v-if="artwork" :src="artwork.logo" alt="" class="object-contain" :class="inline ? 'size-4' : compact ? 'size-5' : 'size-6'" />
    <component :is="fallback.icon" v-else class="text-inherit" :class="inline ? 'size-3.5' : compact ? 'size-4' : 'size-5'" />
  </span>
</template>

<style scoped>
.service-brand-icon {
  background: color-mix(in srgb, var(--service-brand) 7%, var(--card));
  border-color: color-mix(in srgb, var(--service-brand) 22%, var(--border));
}
</style>
