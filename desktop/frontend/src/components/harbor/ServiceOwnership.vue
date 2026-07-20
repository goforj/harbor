<script setup lang="ts">
import { Cloud, Container } from '@lucide/vue'
import { computed } from 'vue'
import type { ServiceSnapshot } from '@/domain/harbor'
import { serviceOwnerLabel } from '@/lib/servicePresentation'

const props = withDefaults(defineProps<{
  owner: ServiceSnapshot['owner']
  iconOnly?: boolean
}>(), {
  iconOnly: false,
})

const label = computed(() => serviceOwnerLabel(props.owner))
</script>

<template>
  <span
    class="inline-flex min-w-0 items-center gap-1.5"
    :aria-label="iconOnly ? label : undefined"
    :title="iconOnly ? label : undefined"
  >
    <Container v-if="owner === 'compose'" aria-hidden="true" class="size-3.5 shrink-0" />
    <Cloud v-else aria-hidden="true" class="size-3.5 shrink-0" />
    <span v-if="!iconOnly" class="truncate">{{ label }}</span>
  </span>
</template>
