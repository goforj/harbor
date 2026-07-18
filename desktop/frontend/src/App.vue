<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref } from 'vue'
import { RouterView, useRoute } from 'vue-router'
import { toast } from 'vue-sonner'
import ContextPane from '@/components/harbor/ContextPane.vue'
import HarborCommandMenu from '@/components/harbor/HarborCommandMenu.vue'
import HarborMobileNav from '@/components/harbor/HarborMobileNav.vue'
import HarborRail from '@/components/harbor/HarborRail.vue'
import { Button } from '@/components/ui/button'
import { Spinner } from '@/components/ui/spinner'
import { Toaster } from '@/components/ui/sonner'
import { TooltipProvider } from '@/components/ui/tooltip'
import { useHarborStore } from '@/stores/harbor'

const route = useRoute()
const harbor = useHarborStore()
const commandOpen = ref(false)

const hasDetail = computed(() => {
  if (route.name === 'overview') {
    return true
  }

  if (route.name === 'project') {
    return typeof route.params.projectId === 'string' && route.params.projectId.length > 0
  }

  if (route.name === 'service') {
    return typeof route.params.serviceId === 'string' && route.params.serviceId.length > 0
  }

  return route.name === 'system'
})

const keydownHandler = (event: KeyboardEvent) => {
  const target = event.target as HTMLElement | null
  const tagName = target?.tagName.toLowerCase()
  const isTyping = tagName === 'input' || tagName === 'textarea' || tagName === 'select' || target?.isContentEditable

  if (!isTyping && (event.metaKey || event.ctrlKey) && event.key.toLowerCase() === 'k') {
    event.preventDefault()
    commandOpen.value = !commandOpen.value
  }
}

onMounted(async () => {
  window.addEventListener('keydown', keydownHandler)
  await harbor.initialize()

  if (harbor.error) {
    toast.error('Harbor is unavailable', { description: harbor.error })
  }
})

onBeforeUnmount(() => {
  window.removeEventListener('keydown', keydownHandler)
  harbor.dispose()
})
</script>

<template>
  <TooltipProvider :delay-duration="300">
    <div class="harbor-workspace" :data-has-detail="hasDetail">
      <div class="harbor-rail-slot">
        <HarborRail @command="commandOpen = true" />
      </div>

      <div class="harbor-context-slot">
        <ContextPane />
      </div>

      <div class="harbor-detail-slot">
        <div v-if="harbor.loading && !harbor.snapshot" class="flex h-full items-center justify-center gap-2 text-sm text-muted-foreground">
          <Spinner />
          <span>Connecting to Harbor</span>
        </div>

        <div v-else-if="harbor.error && !harbor.snapshot" class="flex h-full items-center justify-center p-6">
          <div class="max-w-sm text-center">
            <p class="text-sm font-medium">Harbor could not load local state</p>
            <p class="mt-1 text-sm text-muted-foreground">{{ harbor.error }}</p>
            <Button class="mt-4" size="sm" @click="harbor.refresh">Try again</Button>
          </div>
        </div>

        <RouterView v-else />
      </div>

      <HarborMobileNav class="harbor-mobile-slot" @command="commandOpen = true" />
    </div>

    <HarborCommandMenu v-model:open="commandOpen" />
    <Toaster rich-colors position="bottom-right" />
  </TooltipProvider>
</template>
