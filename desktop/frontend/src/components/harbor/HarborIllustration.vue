<script setup lang="ts">
import { computed } from 'vue'

export type HarborIllustrationPlacement = 'bottom-left' | 'bottom-right' | 'top-left' | 'top-right'
export type HarborIllustrationSize = 'compact' | 'standard' | 'wide'
export type HarborIllustrationFade = 'soft' | 'balanced' | 'strong'

const props = withDefaults(defineProps<{
  image: string
  placement?: HarborIllustrationPlacement
  size?: HarborIllustrationSize
  fade?: HarborIllustrationFade
  opacity?: number
}>(), {
  placement: 'bottom-right',
  size: 'standard',
  fade: 'balanced',
})

const illustrationStyle = computed<Record<string, string>>(() => {
  const style = {
    backgroundImage: `url(${JSON.stringify(props.image)})`,
  }
  if (props.opacity === undefined || !Number.isFinite(props.opacity)) {
    return style
  }
  return {
    ...style,
    '--harbor-illustration-opacity': String(Math.min(0.1, Math.max(0.04, props.opacity))),
  }
})
</script>

<template>
  <div
    class="harbor-illustration"
    :data-fade="fade"
    :data-placement="placement"
    :data-size="size"
    :style="illustrationStyle"
    aria-hidden="true"
  />
</template>
