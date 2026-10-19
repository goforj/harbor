<script setup lang="ts">
import { computed } from 'vue'
import { RotateCw } from '@lucide/vue'
import { Button } from '@/components/ui/button'
import { Card, CardContent, CardHeader, CardTitle } from '@/components/ui/card'
import { Spinner } from '@/components/ui/spinner'
import { useHarborStore } from '@/stores/harbor'

const store = useHarborStore()
const title = computed(() => {
  switch (store.networkSetupRecovery?.kind) {
    case 'expansion': return 'Finish network upgrade'
    case 'repair': return 'Finish network repair'
    default: return 'Finish setting up networking'
  }
})
const description = computed(() => store.networkSetupRecovery?.phase === 'confirmation'
  ? store.networkSetupRecovery.kind === 'setup'
    ? 'The loopback changes were verified. Resume finishes confirmation and continues secure networking setup.'
    : 'The network changes were verified. Resume finishes saving setup without repeating those system changes.'
  : 'Setup was interrupted before its system changes were verified. Resume continues the existing operation and may ask for administrator approval.')
const blocked = computed(() => store.settingUpNetwork || store.removingOldNetworking || store.projectLifecycleBusy
  || store.snapshotStale || store.connectionState !== 'connected')
</script>

<template>
  <Card v-if="store.networkSetupRecovery" class="gap-0 rounded-lg border-amber-500/30 py-0 shadow-none" aria-label="Pending network setup">
    <CardHeader class="border-b border-amber-500/20 px-4 py-3">
      <div class="flex items-center gap-2.5">
        <span class="flex size-7 shrink-0 items-center justify-center rounded-md bg-amber-500/10 text-amber-600 dark:text-amber-400"><RotateCw class="size-4" aria-hidden="true" /></span>
        <CardTitle class="text-sm">{{ title }}</CardTitle>
      </div>
    </CardHeader>
    <CardContent class="flex flex-col items-start justify-between gap-4 p-4 sm:flex-row sm:items-center">
      <div class="min-w-0 space-y-1.5">
        <p class="max-w-2xl text-sm text-muted-foreground">{{ description }}</p>
        <p v-if="store.networkSetupError" class="text-xs text-destructive" role="alert">{{ store.networkSetupError }}</p>
      </div>
      <Button class="shrink-0" size="sm" :disabled="blocked" @click="store.resumeNetworkSetup">
        <Spinner v-if="store.settingUpNetwork" aria-hidden="true" />
        <RotateCw v-else class="size-3.5" aria-hidden="true" />
        {{ store.settingUpNetwork ? 'Resuming…' : 'Resume setup' }}
      </Button>
    </CardContent>
  </Card>
</template>
