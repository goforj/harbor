<script setup lang="ts">
import { computed } from 'vue'
import { presentationIcon } from '@/lib/presentationIcons'

const props = defineProps<{ kind: string; compact?: boolean }>()
const presentation = computed(() => presentationIcon(props.kind))
</script>

<template>
  <span class="kind-icon inline-flex shrink-0 items-center justify-center rounded-lg border" :class="compact ? 'size-7' : 'size-9'" :data-tone="presentation.tone" aria-hidden="true">
    <component :is="presentation.icon" :class="compact ? 'size-3.5' : 'size-4.5'" />
  </span>
</template>
