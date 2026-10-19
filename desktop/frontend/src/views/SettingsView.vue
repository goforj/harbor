<script setup lang="ts">
import { useRoute } from 'vue-router'
import AppAppearanceSettings from '@/components/harbor/AppAppearanceSettings.vue'
import TerminalAppearanceSettings from '@/components/harbor/TerminalAppearanceSettings.vue'
import SystemView from './SystemView.vue'

const route = useRoute()
</script>

<template>
  <SystemView v-if="route.params.section === 'daemon'" />
  <main v-else class="h-full min-w-0 overflow-y-auto" aria-labelledby="appearance-title">
    <header class="border-b px-5 py-4 lg:px-7">
      <h1 id="appearance-title" class="text-base font-semibold tracking-tight">Appearance</h1>
      <p class="mt-1 text-xs text-muted-foreground">Make Harbor feel at home.</p>
    </header>
    <div class="mx-auto max-w-5xl space-y-5 p-5 lg:p-7">
      <AppAppearanceSettings />
      <TerminalAppearanceSettings />
    </div>
  </main>
</template>
