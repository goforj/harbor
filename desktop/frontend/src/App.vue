<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { RouterView, useRoute } from 'vue-router'
import { toast } from 'vue-sonner'
import ContextPane from '@/components/harbor/ContextPane.vue'
import HarborCommandMenu from '@/components/harbor/HarborCommandMenu.vue'
import HarborIllustration from '@/components/harbor/HarborIllustration.vue'
import HarborMobileNav from '@/components/harbor/HarborMobileNav.vue'
import HarborRail from '@/components/harbor/HarborRail.vue'
import { harborBridgeMode } from '@/bridge'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Spinner } from '@/components/ui/spinner'
import { Toaster } from '@/components/ui/sonner'
import { TooltipProvider } from '@/components/ui/tooltip'
import { useHarborStore } from '@/stores/harbor'
import harborBackground from '@/assets/illustrations/harbor-background.png'

const route = useRoute()
const harbor = useHarborStore()
const commandOpen = ref(false)

watch(() => harbor.actionError, (message) => {
  if (message) {
    toast.error('Harbor could not open the resource', { description: message })
  }
})

watch(() => harbor.projectRegistrationError, (message) => {
  if (message) {
    toast.error('Harbor could not add the project', { description: message })
  }
})

const hasDetail = computed(() => {
  if (route.name === 'overview') {
    return true
  }

  if (route.name === 'project') {
    return typeof route.params.projectId === 'string' && route.params.projectId.length > 0
  }

  if (route.name === 'service') {
    return typeof route.params.projectId === 'string'
      && route.params.projectId.length > 0
      && typeof route.params.serviceId === 'string'
      && route.params.serviceId.length > 0
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
})

onBeforeUnmount(() => {
  window.removeEventListener('keydown', keydownHandler)
  harbor.dispose()
})
</script>

<template>
  <TooltipProvider :delay-duration="300">
    <div class="harbor-workspace" :data-has-detail="hasDetail">
      <HarborIllustration :image="harborBackground" />

      <Badge
        v-if="harborBridgeMode === 'fixture'"
        variant="outline"
        class="pointer-events-none fixed right-3 bottom-[4.75rem] z-50 border-amber-500/35 bg-background/90 text-[10px] font-medium tracking-wide text-amber-600 uppercase shadow-sm backdrop-blur min-[700px]:bottom-3 dark:text-amber-400"
      >
        Development fixture
      </Badge>

      <div class="harbor-rail-slot">
        <HarborRail @command="commandOpen = true" />
      </div>

      <div class="harbor-context-slot">
        <ContextPane />
      </div>

      <div class="harbor-detail-slot">
        <div class="flex h-full min-h-0 flex-col">
          <div
            v-if="harbor.snapshot && harbor.connectionMessage"
            class="shrink-0 border-b border-amber-500/30 bg-amber-500/10 px-5 py-2 text-xs text-amber-800 dark:text-amber-300"
            role="status"
          >
            <p>{{ harbor.connectionMessage }}</p>
            <p v-if="harbor.error && harbor.error !== harbor.connectionMessage" class="mt-0.5 opacity-80">{{ harbor.error }}</p>
          </div>

          <div class="min-h-0 flex-1">
            <div
              v-if="!harbor.snapshot"
              class="flex h-full items-center justify-center p-6"
              :role="harbor.error ? 'alert' : 'status'"
            >
              <div class="max-w-sm text-center">
                <Spinner v-if="harbor.loading" class="mx-auto mb-3" aria-hidden="true" />
                <p class="text-sm font-medium">{{ harbor.connectionMessage }}</p>
                <p v-if="harbor.error && harbor.error !== harbor.connectionMessage" class="mt-1 text-sm text-muted-foreground">{{ harbor.error }}</p>
                <Button
                  v-if="harbor.error"
                  class="mt-4"
                  size="sm"
                  :disabled="harbor.refreshing"
                  @click="harbor.refresh"
                >
                  Try again
                </Button>
              </div>
            </div>

            <RouterView v-else />
          </div>
        </div>
      </div>

      <HarborMobileNav class="harbor-mobile-slot" @command="commandOpen = true" />
    </div>

    <HarborCommandMenu v-model:open="commandOpen" />
    <Toaster rich-colors position="bottom-right" />
  </TooltipProvider>
</template>
