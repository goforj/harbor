<script setup lang="ts">
import { computed } from 'vue'
import type { ProjectSnapshot } from '@/domain/harbor'
import goforjIcon from '@/assets/brand/goforj-ios.png'
import ResourceFavicon from './ResourceFavicon.vue'

const props = defineProps<{ project: ProjectSnapshot; compact?: boolean }>()
const online = computed(() => ['ready', 'rebuilding', 'degraded'].includes(props.project.state))
const primaryResource = computed(() => props.project.resources.find(resource => resource.owner.kind === 'app' && resource.kind === 'application'))
</script>

<template>
  <ResourceFavicon
    v-if="online && primaryResource"
    :name="project.name"
    :url="primaryResource.url"
    :project-id="project.id"
    :resource-id="primaryResource.id"
    :compact="compact"
    fallback="goforj"
  />
  <img v-else :src="goforjIcon" alt="" aria-hidden="true" class="shrink-0 rounded-[22%] object-contain" :class="compact ? 'size-6' : 'size-8'">
</template>
