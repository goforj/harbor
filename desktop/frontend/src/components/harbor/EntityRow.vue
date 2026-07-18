<script setup lang="ts">
import type { RouteLocationRaw } from 'vue-router'
import { RouterLink } from 'vue-router'
import { ChevronRight } from '@lucide/vue'
import type { HarborStatus } from '@/domain/harbor'
import StatusBadge from './StatusBadge.vue'

const props = withDefaults(defineProps<{
  label: string
  description?: string
  status?: HarborStatus
  selected?: boolean
  to?: RouteLocationRaw
  disabled?: boolean
  trailingLabel?: string
}>(), {
  description: undefined,
  status: undefined,
  selected: false,
  to: undefined,
  disabled: false,
  trailingLabel: undefined,
})

const emit = defineEmits<{
  activate: []
}>()

function activate(event: MouseEvent) {
  if (props.disabled) {
    event.preventDefault()
    event.stopPropagation()
    return
  }

  emit('activate')
}
</script>

<template>
  <component
    :is="to ? RouterLink : 'button'"
    :to="to"
    :type="to ? undefined : 'button'"
    :disabled="!to && disabled ? true : undefined"
    :aria-current="selected ? 'page' : undefined"
    :aria-disabled="disabled ? 'true' : undefined"
    :data-selected="selected ? '' : undefined"
    class="group flex min-h-12 w-full items-center gap-2.5 rounded-md border border-transparent px-2.5 py-2 text-left outline-none transition-colors hover:bg-accent/70 focus-visible:border-ring focus-visible:ring-2 focus-visible:ring-ring/40 data-[selected]:border-border data-[selected]:bg-accent disabled:pointer-events-none disabled:opacity-50"
    @click="activate"
  >
    <span
      v-if="$slots.leading"
      class="flex size-7 shrink-0 items-center justify-center rounded-md border border-border bg-background text-muted-foreground [&_svg]:size-3.5"
      aria-hidden="true"
    >
      <slot name="leading" />
    </span>

    <span class="min-w-0 flex-1">
      <span class="flex items-center gap-1.5">
        <span class="truncate text-sm font-medium text-foreground">{{ label }}</span>
        <slot name="label" />
      </span>
      <span
        v-if="description"
        class="mt-0.5 block truncate text-xs text-muted-foreground"
      >
        {{ description }}
      </span>
    </span>

    <span class="flex shrink-0 items-center gap-1.5">
      <span v-if="trailingLabel" class="text-[0.6875rem] text-muted-foreground">
        {{ trailingLabel }}
      </span>
      <StatusBadge v-if="status" :status="status" compact />
      <slot name="trailing">
        <ChevronRight
          v-if="to"
          aria-hidden="true"
          class="size-3.5 text-muted-foreground/60 transition-transform group-hover:translate-x-0.5"
        />
      </slot>
    </span>
  </component>
</template>
