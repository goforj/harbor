<script setup lang="ts">
import type { Component } from 'vue'
import { computed } from 'vue'
import {
  Check,
  Circle,
  HelpCircle,
  Loader2,
  TriangleAlert,
  X,
} from '@lucide/vue'
import { Badge } from '@/components/ui/badge'
import type { HarborStatus } from '@/domain/harbor'

interface StatusPresentation {
  label: string
  icon: Component
  classes: string
  iconClasses?: string
}

const props = withDefaults(defineProps<{
  status: HarborStatus
  compact?: boolean
  label?: string
}>(), {
  compact: false,
  label: undefined,
})

const statusPresentation = computed<StatusPresentation>(() => {
  switch (props.status) {
    case 'ready':
    case 'succeeded':
      return {
        label: props.status === 'succeeded' ? 'Succeeded' : 'Ready',
        icon: Check,
        classes: 'border-status-ready/25 bg-status-ready/10 text-status-ready',
      }
    case 'working':
    case 'starting':
    case 'rebuilding':
    case 'queued':
    case 'running':
      return {
        label: props.status === 'working'
          ? 'Working'
          : props.status.charAt(0).toUpperCase() + props.status.slice(1),
        icon: Loader2,
        classes: 'border-status-working/25 bg-status-working/10 text-status-working',
        iconClasses: 'animate-spin',
      }
    case 'degraded':
    case 'requires_approval':
      return {
        label: props.status === 'requires_approval' ? 'Requires approval' : 'Needs attention',
        icon: TriangleAlert,
        classes: 'border-status-degraded/30 bg-status-degraded/10 text-status-degraded',
      }
    case 'failed':
      return {
        label: 'Failed',
        icon: X,
        classes: 'border-status-failed/30 bg-status-failed/10 text-status-failed',
      }
    case 'stopped':
    case 'stopping':
    case 'cancelled':
      return {
        label: props.status === 'cancelled'
          ? 'Cancelled'
          : props.status === 'stopping'
            ? 'Stopping'
            : 'Stopped',
        icon: Circle,
        classes: 'border-status-stopped/25 bg-status-stopped/10 text-status-stopped',
      }
    case 'unavailable':
      return {
        label: 'Unavailable',
        icon: HelpCircle,
        classes: 'border-status-unavailable/25 bg-status-unavailable/10 text-status-unavailable',
      }
  }
})

const displayLabel = computed(() => props.label ?? statusPresentation.value.label)
</script>

<template>
  <Badge
    variant="outline"
    :data-status="status"
    :class="[
      'h-5 gap-1 rounded-md px-1.5 text-[0.6875rem] leading-none font-medium shadow-none',
      statusPresentation.classes,
    ]"
    :aria-label="`Status: ${displayLabel}`"
  >
    <component
      :is="statusPresentation.icon"
      aria-hidden="true"
      :class="['size-3', statusPresentation.iconClasses]"
    />
    <span :class="compact ? 'sr-only' : undefined">{{ displayLabel }}</span>
  </Badge>
</template>
