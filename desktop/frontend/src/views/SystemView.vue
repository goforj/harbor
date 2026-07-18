<script setup lang="ts">
import { computed, nextTick, onMounted, watch } from 'vue'
import { RouterLink, useRoute } from 'vue-router'
import {
  Activity,
  Anchor,
  Box,
  CheckCircle2,
  Container,
  KeyRound,
  Network,
  RefreshCw,
  Route,
  Settings2,
  ShieldCheck,
} from '@lucide/vue'
import StatusBadge from '@/components/harbor/StatusBadge.vue'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Card, CardContent, CardHeader, CardTitle } from '@/components/ui/card'
import { Separator } from '@/components/ui/separator'
import type { HarborStatus } from '@/domain/harbor'
import { useHarborStore } from '@/stores/harbor'

const route = useRoute()
const store = useHarborStore()

const activeSection = computed(() => {
  const value = route.params.section
  const section = (Array.isArray(value) ? value[0] : value) ?? 'network'

  if (section === 'https') return 'trust'
  if (section === 'daemon' || section === 'docker' || section === 'updates') return 'runtime'
  if (section === 'ingress') return 'network'
  return section
})
const overallStatus = computed<HarborStatus>(() => {
  const statuses = store.system.map((check) => check.status)
  if (statuses.includes('failed')) return 'failed'
  if (statuses.includes('degraded')) return 'degraded'
  if (statuses.includes('working')) return 'working'
  if (statuses.includes('stopped')) return 'stopped'
  if (statuses.includes('unavailable')) return 'unavailable'
  if (statuses.length > 0 && statuses.every((status) => status === 'ready')) return 'ready'
  return 'unavailable'
})
const daemon = computed(() => store.system.find((check) => check.id === 'daemon'))
const networkCheck = computed(() => store.system.find((check) => check.id === 'network'))
const httpsCheck = computed(() => store.system.find((check) => check.id === 'https'))
const ingressCheck = computed(() => store.system.find((check) => check.id === 'ingress'))
const dockerCheck = computed(() => store.system.find((check) => check.id === 'docker'))
const updatesCheck = computed(() => store.system.find((check) => check.id === 'updates'))

const sections = [
  { id: 'network', label: 'DNS & ingress' },
  { id: 'trust', label: 'HTTPS & trust' },
  { id: 'runtime', label: 'Runtime' },
  { id: 'settings', label: 'Settings' },
]

async function revealActiveSection() {
  await nextTick()
  const section = document.getElementById(activeSection.value)
  section?.scrollIntoView({ block: 'start' })
  section?.focus({ preventScroll: true })
}

onMounted(() => {
  void revealActiveSection()
})

watch(activeSection, () => {
  void revealActiveSection()
})
</script>

<template>
  <main class="h-full min-w-0 overflow-y-auto" aria-labelledby="system-title">
    <header class="border-b px-5 pt-4 lg:px-7">
      <div class="flex min-w-0 flex-wrap items-start justify-between gap-3 pb-4">
        <div class="min-w-0">
          <div class="flex min-w-0 items-center gap-2">
            <h1 id="system-title" class="truncate text-base font-semibold tracking-tight">System</h1>
            <StatusBadge :status="overallStatus" />
          </div>
          <p class="mt-1 text-xs text-muted-foreground">
            Host networking, trust, runtime dependencies, and Harbor-owned state
          </p>
        </div>
        <Button
          variant="outline"
          size="sm"
          :disabled="store.loading"
          @click="store.refresh"
        >
          <RefreshCw :class="['size-3.5', store.loading && 'animate-spin']" aria-hidden="true" />
          Recheck
        </Button>
      </div>

      <nav class="-mb-px flex gap-4 overflow-x-auto" aria-label="System sections">
        <RouterLink
          v-for="section in sections"
          :key="section.id"
          :to="`/system/${section.id}`"
          :aria-current="activeSection === section.id ? 'page' : undefined"
          :class="[
            'shrink-0 border-b-2 px-1 pb-3 text-xs font-medium transition-colors focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring',
            activeSection === section.id
              ? 'border-primary text-foreground'
              : 'border-transparent text-muted-foreground hover:text-foreground',
          ]"
        >
          {{ section.label }}
        </RouterLink>
      </nav>
    </header>

    <div class="space-y-5 p-5 lg:p-7">
      <section aria-label="System checks" class="grid overflow-hidden rounded-lg border sm:grid-cols-2 xl:grid-cols-3">
        <div v-for="check in store.system" :key="check.id" class="flex items-start gap-3 border-b p-4 sm:odd:border-r xl:border-r xl:[&:nth-child(3n)]:border-r-0 xl:[&:nth-last-child(-n+3)]:border-b-0">
          <StatusBadge :status="check.status" />
          <div class="min-w-0 flex-1">
            <p class="text-sm font-medium">{{ check.name }}</p>
            <p class="mt-0.5 truncate text-xs text-muted-foreground">{{ check.detail }}</p>
          </div>
        </div>
        <p v-if="!store.system.length" class="col-span-full px-4 py-8 text-center text-sm text-muted-foreground">
          Waiting for the daemon to report system checks.
        </p>
      </section>

      <div class="grid min-w-0 gap-5 xl:grid-cols-2">
        <Card
          id="network"
          tabindex="-1"
          :class="[
            'scroll-mt-5 gap-0 rounded-lg py-0 shadow-none',
            activeSection === 'network' && 'ring-1 ring-ring/30',
          ]"
        >
          <CardHeader class="border-b px-4 py-3">
            <div class="flex items-center justify-between gap-3">
              <div class="flex items-center gap-2">
                <Network class="size-4 text-muted-foreground" aria-hidden="true" />
                <CardTitle class="text-sm">DNS and ingress</CardTitle>
              </div>
              <StatusBadge :status="networkCheck?.status ?? 'unavailable'" />
            </div>
            <p class="text-xs text-muted-foreground">One local entry point routes stable project domains</p>
          </CardHeader>
          <CardContent class="p-0">
            <dl class="divide-y">
              <div class="grid grid-cols-[7.5rem_minmax(0,1fr)] gap-3 px-4 py-3">
                <dt class="text-xs text-muted-foreground">Resolver suffix</dt>
                <dd class="font-mono text-xs font-medium">.test</dd>
              </div>
              <div class="grid grid-cols-[7.5rem_minmax(0,1fr)] gap-3 px-4 py-3">
                <dt class="text-xs text-muted-foreground">DNS</dt>
                <dd class="truncate text-xs font-medium">{{ networkCheck?.detail ?? 'Not reported' }}</dd>
              </div>
              <div class="grid grid-cols-[7.5rem_minmax(0,1fr)] gap-3 px-4 py-3">
                <dt class="text-xs text-muted-foreground">Public ingress</dt>
                <dd class="truncate text-xs font-medium">{{ ingressCheck?.detail ?? 'Not reported' }}</dd>
              </div>
            </dl>
          </CardContent>
        </Card>

        <Card
          id="trust"
          tabindex="-1"
          :class="[
            'scroll-mt-5 gap-0 rounded-lg py-0 shadow-none',
            activeSection === 'trust' && 'ring-1 ring-ring/30',
          ]"
        >
          <CardHeader class="border-b px-4 py-3">
            <div class="flex items-center justify-between gap-3">
              <div class="flex items-center gap-2">
                <ShieldCheck class="size-4 text-muted-foreground" aria-hidden="true" />
                <CardTitle class="text-sm">HTTPS and trust</CardTitle>
              </div>
              <StatusBadge :status="httpsCheck?.status ?? 'unavailable'" />
            </div>
            <p class="text-xs text-muted-foreground">Harbor-owned local CA and exact project certificates</p>
          </CardHeader>
          <CardContent class="p-0">
            <dl class="divide-y">
              <div class="grid grid-cols-[7.5rem_minmax(0,1fr)] gap-3 px-4 py-3">
                <dt class="text-xs text-muted-foreground">Authority</dt>
                <dd class="text-xs font-medium">Harbor local CA</dd>
              </div>
              <div class="grid grid-cols-[7.5rem_minmax(0,1fr)] gap-3 px-4 py-3">
                <dt class="text-xs text-muted-foreground">Trust</dt>
                <dd class="truncate text-xs font-medium">{{ httpsCheck?.detail ?? 'Not reported' }}</dd>
              </div>
              <div class="grid grid-cols-[7.5rem_minmax(0,1fr)] gap-3 px-4 py-3">
                <dt class="text-xs text-muted-foreground">Certificate scope</dt>
                <dd class="text-xs font-medium">Registered exact domains</dd>
              </div>
            </dl>
          </CardContent>
        </Card>

        <Card class="gap-0 rounded-lg py-0 shadow-none">
          <CardHeader class="border-b px-4 py-3">
            <div class="flex items-center justify-between gap-3">
              <div class="flex items-center gap-2">
                <Route class="size-4 text-muted-foreground" aria-hidden="true" />
                <CardTitle class="text-sm">Loopback identities</CardTitle>
              </div>
              <StatusBadge :status="ingressCheck?.status ?? 'unavailable'" />
            </div>
            <p class="text-xs text-muted-foreground">Project-scoped addresses preserve native ports without collisions</p>
          </CardHeader>
          <CardContent class="p-0">
            <dl class="divide-y">
              <div class="grid grid-cols-[7.5rem_minmax(0,1fr)] gap-3 px-4 py-3">
                <dt class="text-xs text-muted-foreground">Projects</dt>
                <dd class="text-xs font-medium">{{ store.projects.length }} registered identities</dd>
              </div>
              <div class="grid grid-cols-[7.5rem_minmax(0,1fr)] gap-3 px-4 py-3">
                <dt class="text-xs text-muted-foreground">Address scope</dt>
                <dd class="text-xs font-medium">Host loopback only</dd>
              </div>
              <div class="grid grid-cols-[7.5rem_minmax(0,1fr)] gap-3 px-4 py-3">
                <dt class="text-xs text-muted-foreground">Ports</dt>
                <dd class="text-xs font-medium">Native service ports retained</dd>
              </div>
            </dl>
          </CardContent>
        </Card>

        <Card
          id="runtime"
          tabindex="-1"
          :class="[
            'scroll-mt-5 gap-0 rounded-lg py-0 shadow-none',
            activeSection === 'runtime' && 'ring-1 ring-ring/30',
          ]"
        >
          <CardHeader class="border-b px-4 py-3">
            <div class="flex items-center gap-2">
              <Activity class="size-4 text-muted-foreground" aria-hidden="true" />
              <CardTitle class="text-sm">Runtime</CardTitle>
            </div>
            <p class="text-xs text-muted-foreground">Daemon and container dependencies</p>
          </CardHeader>
          <CardContent class="p-0">
            <div class="divide-y">
              <div class="flex items-start gap-3 px-4 py-3">
                <StatusBadge :status="daemon?.status ?? 'unavailable'" />
                <div class="min-w-0 flex-1">
                  <p class="text-sm font-medium">Harbor daemon</p>
                  <p class="truncate text-xs text-muted-foreground">{{ daemon?.detail ?? 'Not connected' }}</p>
                </div>
              </div>
              <div class="flex items-start gap-3 px-4 py-3">
                <StatusBadge :status="dockerCheck?.status ?? 'unavailable'" />
                <div class="min-w-0 flex-1">
                  <p class="text-sm font-medium">Container runtime</p>
                  <p class="truncate text-xs text-muted-foreground">{{ dockerCheck?.detail ?? 'Not reported' }}</p>
                </div>
              </div>
              <div class="flex items-start gap-3 px-4 py-3">
                <StatusBadge :status="updatesCheck?.status ?? 'unavailable'" />
                <div class="min-w-0 flex-1">
                  <p class="text-sm font-medium">Desktop updates</p>
                  <p class="truncate text-xs text-muted-foreground">{{ updatesCheck?.detail ?? 'Not reported' }}</p>
                </div>
              </div>
            </div>
          </CardContent>
        </Card>
      </div>

      <Card
        id="settings"
        tabindex="-1"
        :class="[
          'scroll-mt-5 gap-0 rounded-lg py-0 shadow-none',
          activeSection === 'settings' && 'ring-1 ring-ring/30',
        ]"
      >
        <CardHeader class="border-b px-4 py-3">
          <div class="flex items-center gap-2">
            <Settings2 class="size-4 text-muted-foreground" aria-hidden="true" />
            <CardTitle class="text-sm">Settings</CardTitle>
          </div>
          <p class="text-xs text-muted-foreground">Machine-global choices for the active Harbor profile</p>
        </CardHeader>
        <CardContent class="p-0">
          <div class="grid md:grid-cols-2">
            <div class="flex items-start gap-3 border-b px-4 py-4 md:border-r">
              <Anchor class="mt-0.5 size-4 shrink-0 text-muted-foreground" aria-hidden="true" />
              <div class="min-w-0 flex-1">
                <div class="flex items-center justify-between gap-3">
                  <p class="text-sm font-medium">Development domains</p>
                  <Badge variant="outline">.test</Badge>
                </div>
                <p class="mt-1 text-xs leading-5 text-muted-foreground">Exact project names resolve through Harbor's local resolver.</p>
              </div>
            </div>
            <div class="flex items-start gap-3 border-b px-4 py-4">
              <KeyRound class="mt-0.5 size-4 shrink-0 text-muted-foreground" aria-hidden="true" />
              <div class="min-w-0 flex-1">
                <div class="flex items-center justify-between gap-3">
                  <p class="text-sm font-medium">Host changes</p>
                  <Badge variant="secondary">On demand</Badge>
                </div>
                <p class="mt-1 text-xs leading-5 text-muted-foreground">A short-lived privileged helper applies only approved Harbor-owned changes.</p>
              </div>
            </div>
            <div class="flex items-start gap-3 border-b px-4 py-4 md:border-r md:border-b-0">
              <Container class="mt-0.5 size-4 shrink-0 text-muted-foreground" aria-hidden="true" />
              <div class="min-w-0 flex-1">
                <div class="flex items-center justify-between gap-3">
                  <p class="text-sm font-medium">Service isolation</p>
                  <Badge variant="secondary">Per project</Badge>
                </div>
                <p class="mt-1 text-xs leading-5 text-muted-foreground">Projects retain separate containers, versions, lifecycle, and data.</p>
              </div>
            </div>
            <div class="flex items-start gap-3 px-4 py-4">
              <Box class="mt-0.5 size-4 shrink-0 text-muted-foreground" aria-hidden="true" />
              <div class="min-w-0 flex-1">
                <div class="flex items-center justify-between gap-3">
                  <p class="text-sm font-medium">Active profile</p>
                  <Badge variant="outline">Default</Badge>
                </div>
                <p class="mt-1 text-xs leading-5 text-muted-foreground">One Harbor profile owns machine-global ingress and resolver state.</p>
              </div>
            </div>
          </div>
        </CardContent>
      </Card>

      <Separator />
      <p class="flex items-start gap-2 text-xs leading-5 text-muted-foreground">
        <CheckCircle2 class="mt-0.5 size-3.5 shrink-0" aria-hidden="true" />
        Harbor records the host state it owns and leaves unrelated listeners, resolver configuration, and certificates untouched.
      </p>
    </div>
  </main>
</template>
