<script setup lang="ts">
import { computed } from 'vue'
import { Activity, Database, RefreshCw, Server } from '@lucide/vue'
import StatusBadge from '@/components/harbor/StatusBadge.vue'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Card, CardContent, CardHeader, CardTitle } from '@/components/ui/card'
import { useHarborStore } from '@/stores/harbor'

const store = useHarborStore()
const capturedAt = computed(() => store.snapshot?.captured_at
  ? new Intl.DateTimeFormat(undefined, { dateStyle: 'medium', timeStyle: 'long' }).format(new Date(store.snapshot.captured_at))
  : 'Not available')
</script>

<template>
  <main class="h-full min-w-0 overflow-y-auto" aria-labelledby="system-title">
    <header class="flex items-start justify-between gap-4 border-b px-5 py-4 lg:px-7">
      <div class="min-w-0">
        <div class="flex items-center gap-2"><h1 id="system-title" class="truncate text-base font-semibold tracking-tight">System</h1><StatusBadge v-if="store.daemonStatus" :status="store.daemonStatus.state" /></div>
        <p class="mt-1 text-xs text-muted-foreground">Facts available through the current Harbor control connection</p>
      </div>
      <Button variant="outline" size="sm" :disabled="store.loading" @click="store.refresh"><RefreshCw :class="['size-3.5', store.loading && 'animate-spin']" />Refresh</Button>
    </header>

    <div class="space-y-5 p-5 lg:p-7">
      <Card class="gap-0 rounded-lg py-0 shadow-none">
        <CardHeader class="border-b px-4 py-3"><div class="flex items-center gap-2"><Server class="size-4 text-muted-foreground" /><CardTitle class="text-sm">Daemon status</CardTitle></div></CardHeader>
        <CardContent class="p-0">
          <dl v-if="store.daemonStatus" class="divide-y text-xs">
            <div class="grid grid-cols-[10rem_minmax(0,1fr)] gap-3 px-4 py-3"><dt class="text-muted-foreground">State</dt><dd><StatusBadge :status="store.daemonStatus.state" /></dd></div>
            <div class="grid grid-cols-[10rem_minmax(0,1fr)] gap-3 px-4 py-3"><dt class="text-muted-foreground">Build version</dt><dd class="font-mono">{{ store.daemonStatus.build.version }}</dd></div>
            <div class="grid grid-cols-[10rem_minmax(0,1fr)] gap-3 px-4 py-3"><dt class="text-muted-foreground">Revision</dt><dd class="break-all font-mono">{{ store.daemonStatus.build.revision || 'Not embedded' }}</dd></div>
            <div class="grid grid-cols-[10rem_minmax(0,1fr)] gap-3 px-4 py-3"><dt class="text-muted-foreground">Modified build</dt><dd>{{ store.daemonStatus.build.modified ? 'Yes' : 'No' }}</dd></div>
            <div class="grid grid-cols-[10rem_minmax(0,1fr)] gap-3 px-4 py-3"><dt class="text-muted-foreground">Protocol</dt><dd class="font-mono">{{ store.daemonStatus.protocol.major }}.{{ store.daemonStatus.protocol.minor }}</dd></div>
            <div class="grid grid-cols-[10rem_minmax(0,1fr)] gap-3 px-4 py-3"><dt class="text-muted-foreground">Capabilities</dt><dd class="flex flex-wrap gap-1"><Badge v-for="capability in store.daemonStatus.capabilities" :key="capability" variant="outline">{{ capability }}</Badge></dd></div>
            <div class="grid grid-cols-[10rem_minmax(0,1fr)] gap-3 px-4 py-3"><dt class="text-muted-foreground">Snapshot schema</dt><dd>{{ store.daemonStatus.snapshot_schema_version }}</dd></div>
            <div class="grid grid-cols-[10rem_minmax(0,1fr)] gap-3 px-4 py-3"><dt class="text-muted-foreground">Sequence</dt><dd>{{ store.daemonStatus.sequence }}</dd></div>
          </dl>
          <p v-else class="px-4 py-8 text-center text-sm text-muted-foreground">Daemon status has not been received.</p>
        </CardContent>
      </Card>

      <Card class="gap-0 rounded-lg py-0 shadow-none">
        <CardHeader class="border-b px-4 py-3"><div class="flex items-center gap-2"><Database class="size-4 text-muted-foreground" /><CardTitle class="text-sm">Snapshot</CardTitle></div></CardHeader>
        <CardContent class="p-0">
          <dl v-if="store.snapshot" class="divide-y text-xs">
            <div class="grid grid-cols-[10rem_minmax(0,1fr)] gap-3 px-4 py-3"><dt class="text-muted-foreground">Schema version</dt><dd>{{ store.snapshot.schema_version }}</dd></div>
            <div class="grid grid-cols-[10rem_minmax(0,1fr)] gap-3 px-4 py-3"><dt class="text-muted-foreground">Sequence</dt><dd>{{ store.snapshot.sequence }}</dd></div>
            <div class="grid grid-cols-[10rem_minmax(0,1fr)] gap-3 px-4 py-3"><dt class="text-muted-foreground">Captured</dt><dd>{{ capturedAt }}</dd></div>
            <div class="grid grid-cols-[10rem_minmax(0,1fr)] gap-3 px-4 py-3"><dt class="text-muted-foreground">Projects</dt><dd>{{ store.projects.length }}</dd></div>
            <div class="grid grid-cols-[10rem_minmax(0,1fr)] gap-3 px-4 py-3"><dt class="text-muted-foreground">Operations</dt><dd>{{ store.operations.length }}</dd></div>
            <div class="grid grid-cols-[10rem_minmax(0,1fr)] gap-3 px-4 py-3"><dt class="text-muted-foreground">Recent resources</dt><dd>{{ store.recentResources.length }}</dd></div>
          </dl>
          <p v-else class="px-4 py-8 text-center text-sm text-muted-foreground">No snapshot has been received.</p>
        </CardContent>
      </Card>

      <div class="flex items-start gap-3 rounded-lg border px-4 py-3 text-sm text-muted-foreground">
        <Activity class="mt-0.5 size-4 shrink-0" />
        <p>Additional host capability checks are not part of the current control response.</p>
      </div>
    </div>
  </main>
</template>
