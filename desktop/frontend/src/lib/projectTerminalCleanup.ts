import { computed, readonly, ref, shallowRef } from 'vue'
import type { ProjectTerminalSession } from './projectTerminalSession'

const retryDelay = 500
const pendingCount = ref(0)
const inFlightCount = ref(0)
const cleanupError = ref<string | null>(null)
const failedSessions = shallowRef<ProjectTerminalSession[]>([])
const closingSessions = new Set<ProjectTerminalSession>()
const closingPromises = new Set<Promise<void>>()
const retryTimers = new Map<ProjectTerminalSession, number>()

// close retains one capacity reservation until its native terminal is definitively released.
function close(session: ProjectTerminalSession) {
  if (closingSessions.has(session) || failedSessions.value.includes(session)) return
  pendingCount.value += 1
  beginClose(session)
}

// beginClose performs one idempotent close attempt without releasing the shared reservation on failure.
function beginClose(session: ProjectTerminalSession) {
  inFlightCount.value += 1
  closingSessions.add(session)
  const closing = Promise.resolve()
    .then(() => session.close())
    .then(() => {
      clearRetry(session)
      failedSessions.value = failedSessions.value.filter((failed) => failed !== session)
      pendingCount.value -= 1
      if (pendingCount.value === 0) cleanupError.value = null
    })
    .catch((error) => {
      cleanupError.value = error instanceof Error ? error.message : String(error)
      if (!failedSessions.value.includes(session)) {
        failedSessions.value = [...failedSessions.value, session]
      }
      scheduleRetry(session)
    })
    .finally(() => {
      closingPromises.delete(closing)
      closingSessions.delete(session)
      inFlightCount.value -= 1
    })
  closingPromises.add(closing)
}

// scheduleRetry keeps navigation from abandoning the only handle to a live desktop shell.
function scheduleRetry(session: ProjectTerminalSession) {
  if (retryTimers.has(session)) return
  const timer = window.setTimeout(() => {
    retryTimers.delete(session)
    retryClose(session)
  }, retryDelay)
  retryTimers.set(session, timer)
}

// clearRetry prevents a manual or successful retry from issuing a duplicate close.
function clearRetry(session: ProjectTerminalSession) {
  const timer = retryTimers.get(session)
  if (timer !== undefined) window.clearTimeout(timer)
  retryTimers.delete(session)
}

// retryClose reuses a retained session only while its cleanup remains unresolved.
function retryClose(session: ProjectTerminalSession) {
  if (closingSessions.has(session) || !failedSessions.value.includes(session)) return
  clearRetry(session)
  failedSessions.value = failedSessions.value.filter((failed) => failed !== session)
  beginClose(session)
}

// retryFailed immediately retries every cleanup currently waiting on its background timer.
function retryFailed() {
  for (const session of [...failedSessions.value]) retryClose(session)
}

// waitForInFlight waits only for attempts already underway; failed reservations remain visible through pendingCount.
async function waitForInFlight() {
  await Promise.all([...closingPromises])
}

// projectTerminalCleanup owns terminal cleanup across route-scoped ProjectView instances.
export const projectTerminalCleanup = {
  close,
  error: readonly(cleanupError),
  failedCount: computed(() => failedSessions.value.length),
  inFlightCount: readonly(inFlightCount),
  pendingCount: readonly(pendingCount),
  retryFailed,
  waitForInFlight,
}
