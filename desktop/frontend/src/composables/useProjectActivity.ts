import { onBeforeUnmount, onMounted, ref, watch } from 'vue'
import type { Ref } from 'vue'
import type { ProjectActivity, ProjectSessionActivity } from '@/domain/harbor'

const activityFallbackPollInterval = 750
const activityRetryInterval = 2000
const activityEarlyWakeThreshold = 1000
const activityEarlyWakeRetryInterval = 100
const activityWaitMilliseconds = 25_000
const maximumVisibleOutputCharacters = 256 * 1024
const retainedVisibleOutputCharacters = 192 * 1024
const maximumImmediateActivityReads = 8

type ActivityRequestKind = 'read' | 'wait'

interface AppliedActivity {
  hasMore: boolean
  waitable: boolean
}

export interface ProjectActivitySource {
  projectId: Readonly<Ref<string>>
  supported: Readonly<Ref<boolean>>
  waitSupported: Readonly<Ref<boolean>>
  connected: Readonly<Ref<boolean>>
  snapshotSequence: Readonly<Ref<number | undefined>>
  read(projectId: string, sessionId: string, cursor: number): Promise<ProjectActivity>
  wait(projectId: string, sessionId: string, cursor: number, waitMilliseconds: number): Promise<ProjectActivity>
}

// useProjectActivity follows one project's current session without overlapping requests within a connection generation.
export function useProjectActivity(source: ProjectActivitySource) {
  const activity = ref<ProjectActivity | null>(null)
  const output = ref('')
  const error = ref<string | null>(null)
  const truncated = ref(false)
  const outputResetKey = ref(0)
  let cursor = 0
  let sessionId = ''
  let timer: number | null = null
  let generation = 0
  let mounted = false
  let requestInFlight = false
  let immediateReadRequested = false
  let immediateRequestQueued = false
  let queuedRequestKind: ActivityRequestKind = 'read'

  watch(source.projectId, () => {
    invalidate(true)
    requestImmediately('read')
  })

  watch([source.supported, source.waitSupported, source.connected], () => {
    invalidate(false)
    requestImmediately('read')
  })

  watch(source.snapshotSequence, () => requestImmediately('read'))

  onMounted(() => {
    mounted = true
    requestImmediately('read')
  })

  onBeforeUnmount(() => {
    mounted = false
    invalidate(false)
  })

  // invalidate retires every callback from the prior selection or connection generation.
  function invalidate(reset: boolean) {
    generation += 1
    clearTimer()
    requestInFlight = false
    immediateReadRequested = false
    immediateRequestQueued = false
    if (!reset) return
    activity.value = null
    output.value = ''
    error.value = null
    truncated.value = false
    cursor = 0
    sessionId = ''
    outputResetKey.value += 1
  }

  // requestImmediately queues one microtask so synchronous snapshot changes coalesce without delaying output.
  function requestImmediately(kind: ActivityRequestKind) {
    if (!mounted || !canRead()) return
    clearTimer()
    if (requestInFlight) {
      if (kind === 'read') immediateReadRequested = true
      return
    }
    if (immediateRequestQueued) {
      if (kind === 'read') queuedRequestKind = 'read'
      return
    }
    immediateRequestQueued = true
    queuedRequestKind = kind
    const selectedGeneration = generation
    queueMicrotask(() => {
      if (selectedGeneration !== generation) return
      immediateRequestQueued = false
      const requestedKind = queuedRequestKind
      queuedRequestKind = 'read'
      void requestActivity(selectedGeneration, requestedKind)
    })
  }

  // requestAfter is reserved for compatibility, failure retry, and a no-progress daemon response; valid live output has no client-side delay.
  function requestAfter(delay: number) {
    if (!mounted || !canRead()) return
    clearTimer()
    const selectedGeneration = generation
    timer = window.setTimeout(() => {
      timer = null
      void requestActivity(selectedGeneration, 'read')
    }, delay)
  }

  // requestActivity drains retained history before returning to a single blocking live read.
  async function requestActivity(selectedGeneration: number, requestedKind: ActivityRequestKind) {
    if (!isCurrent(selectedGeneration) || requestInFlight) return
    requestInFlight = true
    let nextRequest: ActivityRequestKind = source.waitSupported.value ? 'wait' : 'read'
    let continueLive = false
    let earlyWake = false
    let retry = false
    try {
      if (requestedKind === 'wait' && source.waitSupported.value) {
        const requestedSessionId = sessionId
        const requestedCursor = cursor
        const startedAt = Date.now()
        const current = await source.wait(source.projectId.value, sessionId, cursor, activityWaitMilliseconds)
        if (!isCurrent(selectedGeneration)) return
        const applied = applyActivity(current)
        continueLive = applied.waitable
        earlyWake = applied.waitable
          && sessionId === requestedSessionId
          && cursor === requestedCursor
          && Date.now() - startedAt < activityEarlyWakeThreshold
        if (applied.hasMore) nextRequest = 'read'
      }
      else {
        for (let read = 0; read < maximumImmediateActivityReads; read += 1) {
          const current = await source.read(source.projectId.value, sessionId, cursor)
          if (!isCurrent(selectedGeneration)) return
          const applied = applyActivity(current)
          continueLive = applied.waitable
          if (!applied.hasMore) {
            nextRequest = source.waitSupported.value ? 'wait' : 'read'
            break
          }
          nextRequest = 'read'
        }
      }
    }
    catch (cause) {
      if (!isCurrent(selectedGeneration)) return
      error.value = cause instanceof Error
        ? cause.message
        : 'Harbor could not read project output.'
      retry = true
    }
    finally {
      if (!isCurrent(selectedGeneration)) return
      requestInFlight = false
      if (immediateReadRequested) {
        immediateReadRequested = false
        nextRequest = 'read'
      }
      if (retry) requestAfter(activityRetryInterval)
      else if (!source.waitSupported.value) requestAfter(activityFallbackPollInterval)
      else if (nextRequest === 'read') requestImmediately('read')
      else if (earlyWake) requestAfter(activityEarlyWakeRetryInterval)
      else if (continueLive) requestImmediately('wait')
    }
  }

  // applyActivity validates request identity before allowing returned activity to advance local state.
  function applyActivity(current: ProjectActivity): AppliedActivity {
    if (current.project_id !== source.projectId.value) {
      throw new Error('Harbor returned project activity for another project.')
    }
    activity.value = current
    error.value = null
    if (!current.session) return { hasMore: false, waitable: false }
    return applySession(current.session)
  }

  // applySession advances a cursor only when the daemon still owns readable output for that exact session.
  function applySession(session: ProjectSessionActivity): AppliedActivity {
    const changedSession = sessionId !== '' && sessionId !== session.id
    const newSelection = sessionId !== session.id
    if (newSelection) sessionId = session.id

    if (!session.output.available) {
      if (newSelection) {
        cursor = 0
        output.value = ''
        truncated.value = false
        outputResetKey.value += 1
      }
      return { hasMore: false, waitable: false }
    }

    const replace = changedSession || session.output.reset || session.output.truncated
    cursor = session.output.next_cursor
    if (replace) {
      output.value = session.output.text
      truncated.value = session.output.truncated
      outputResetKey.value += 1
    }
    else if (session.output.text) {
      output.value += session.output.text
    }
    trimVisibleOutput()
    return {
      hasMore: session.output.has_more,
      waitable: session.state !== 'disconnected',
    }
  }

  // trimVisibleOutput bounds renderer memory while preserving a valid JavaScript string boundary.
  function trimVisibleOutput() {
    if (output.value.length <= maximumVisibleOutputCharacters) return
    let start = output.value.length - retainedVisibleOutputCharacters
    const code = output.value.charCodeAt(start)
    if (code >= 0xdc00 && code <= 0xdfff) start += 1
    const newline = output.value.indexOf('\n', start)
    output.value = output.value.slice(newline >= 0 ? newline + 1 : start)
    truncated.value = true
    outputResetKey.value += 1
  }

  // canRead excludes stale bindings and disconnected daemon generations.
  function canRead() {
    return source.projectId.value !== '' && source.supported.value && source.connected.value
  }

  // isCurrent fences asynchronous results against unmount, route changes, and reconnects.
  function isCurrent(selectedGeneration: number) {
    return mounted && selectedGeneration === generation && canRead()
  }

  // clearTimer prevents a retired generation from starting a compatibility poll or retry.
  function clearTimer() {
    if (timer === null) return
    window.clearTimeout(timer)
    timer = null
  }

  return { activity, output, outputResetKey, error, truncated }
}
