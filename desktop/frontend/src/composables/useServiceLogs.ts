import { computed, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import type { Ref } from 'vue'
import type { ServiceLogs } from '@/domain/harbor'

const logFallbackPollInterval = 750
const logRetryInterval = 2000
const logEarlyWakeThreshold = 1000
const logEarlyWakeRetryInterval = 100
const logWaitMilliseconds = 25_000
const maximumVisibleOutputCharacters = 256 * 1024
const retainedVisibleOutputCharacters = 192 * 1024
const maximumImmediateLogReads = 8

type LogRequestKind = 'read' | 'wait'

interface AppliedLogs {
  hasMore: boolean
  recheckable: boolean
  retryable: boolean
  waitable: boolean
}

export type ServiceLogStreamState = 'connecting' | 'waiting' | 'live' | 'reconnecting' | 'unsupported' | 'ended' | 'error'

export interface ServiceLogSource {
  projectId: Readonly<Ref<string>>
  serviceId: Readonly<Ref<string>>
  supported: Readonly<Ref<boolean>>
  waitSupported: Readonly<Ref<boolean>>
  connected: Readonly<Ref<boolean>>
  snapshotSequence: Readonly<Ref<number | undefined>>
  read(projectId: string, sessionId: string, serviceId: string, cursor: number): Promise<ServiceLogs>
  wait(projectId: string, sessionId: string, serviceId: string, cursor: number, waitMilliseconds: number): Promise<ServiceLogs>
}

// useServiceLogs follows one project-scoped service stream without overlapping requests within a connection generation.
export function useServiceLogs(source: ServiceLogSource) {
  const logs = ref<ServiceLogs | null>(null)
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
  let queuedRequestKind: LogRequestKind = 'read'
  let observedSession = false

  const state = computed<ServiceLogStreamState>(() => {
    if (!source.supported.value) return 'unsupported'
    if (!source.connected.value) return 'reconnecting'
    if (error.value || logs.value?.problem) return 'error'
    if (logs.value?.supported === false) return 'unsupported'
    if (!logs.value) return 'connecting'
    if (!logs.value.available) return 'waiting'
    if (logs.value.output.available) return 'live'
    return logs.value.session_id || observedSession ? 'ended' : 'waiting'
  })

  watch([source.projectId, source.serviceId], () => {
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

  // clear removes only the local transcript so the transport can continue from its current byte cursor.
  function clear() {
    output.value = ''
    truncated.value = false
    outputResetKey.value += 1
  }

  // invalidate retires callbacks from a prior service, connection, or capability generation.
  function invalidate(reset: boolean) {
    generation += 1
    clearTimer()
    requestInFlight = false
    immediateReadRequested = false
    immediateRequestQueued = false
    if (!reset) return
    logs.value = null
    output.value = ''
    error.value = null
    truncated.value = false
    cursor = 0
    sessionId = ''
    observedSession = false
    outputResetKey.value += 1
  }

  // requestImmediately coalesces synchronous state changes without delaying live output.
  function requestImmediately(kind: LogRequestKind) {
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
      void requestLogs(selectedGeneration, requestedKind)
    })
  }

  // requestAfter is reserved for compatibility polling, transport recovery, and no-progress responses.
  function requestAfter(delay: number) {
    if (!mounted || !canRead()) return
    clearTimer()
    const selectedGeneration = generation
    timer = window.setTimeout(() => {
      timer = null
      void requestLogs(selectedGeneration, 'read')
    }, delay)
  }

  // requestLogs drains retained history before returning to one held cursor read.
  async function requestLogs(selectedGeneration: number, requestedKind: LogRequestKind) {
    if (!isCurrent(selectedGeneration) || requestInFlight) return
    requestInFlight = true
    let nextRequest: LogRequestKind = source.waitSupported.value ? 'wait' : 'read'
    let continueLive = false
    let earlyWake = false
    let recheck = false
    let retry = false
    try {
      if (requestedKind === 'wait' && source.waitSupported.value) {
        const requestedSessionId = sessionId
        const requestedCursor = cursor
        const startedAt = Date.now()
        const current = await source.wait(source.projectId.value, sessionId, source.serviceId.value, cursor, logWaitMilliseconds)
        if (!isCurrent(selectedGeneration)) return
        const applied = applyLogs(current)
        continueLive = applied.waitable
        recheck = applied.recheckable
        retry = applied.retryable
        earlyWake = applied.waitable
          && sessionId === requestedSessionId
          && cursor === requestedCursor
          && Date.now() - startedAt < logEarlyWakeThreshold
        if (applied.hasMore) nextRequest = 'read'
      }
      else {
        for (let read = 0; read < maximumImmediateLogReads; read += 1) {
          const current = await source.read(source.projectId.value, sessionId, source.serviceId.value, cursor)
          if (!isCurrent(selectedGeneration)) return
          const applied = applyLogs(current)
          continueLive = applied.waitable
          recheck = applied.recheckable
          retry = applied.retryable
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
        : 'Harbor could not read service logs.'
      retry = true
    }
    finally {
      if (!isCurrent(selectedGeneration)) return
      requestInFlight = false
      if (immediateReadRequested) {
        immediateReadRequested = false
        nextRequest = 'read'
      }
      if (retry) requestAfter(logRetryInterval)
      else if (recheck) requestAfter(logFallbackPollInterval)
      else if (!source.waitSupported.value && continueLive) requestAfter(logFallbackPollInterval)
      else if (nextRequest === 'read' && continueLive) requestImmediately('read')
      else if (earlyWake) requestAfter(logEarlyWakeRetryInterval)
      else if (continueLive) requestImmediately('wait')
    }
  }

  // applyLogs validates stream identity before allowing the response to advance local state.
  function applyLogs(current: ServiceLogs): AppliedLogs {
    if (current.project_id !== source.projectId.value || current.service_id !== source.serviceId.value) {
      throw new Error('Harbor returned logs for another service.')
    }
    const changedSession = adoptSession(current.session_id)
    logs.value = current
    error.value = current.problem?.message ?? null
    if (!current.supported || current.problem) {
      return { hasMore: false, recheckable: false, retryable: current.problem?.retryable === true, waitable: false }
    }
    if (!current.available) {
      return { hasMore: false, recheckable: true, retryable: false, waitable: false }
    }
    if (!current.output.available) {
      return { hasMore: false, recheckable: false, retryable: false, waitable: false }
    }
    if (!current.session_id) {
      throw new Error('Harbor returned available service logs without a session identity.')
    }
    observedSession = true
    if (!Number.isSafeInteger(current.output.next_cursor) || current.output.next_cursor < 0) {
      throw new Error('Harbor returned an invalid service log cursor.')
    }
    return applyOutput(current, changedSession)
  }

  // adoptSession retires the prior byte cursor before an unavailable replacement can later become readable.
  function adoptSession(currentSessionId: string | undefined) {
    if (!currentSessionId || currentSessionId === sessionId) return false
    sessionId = currentSessionId
    cursor = 0
    output.value = ''
    truncated.value = false
    observedSession = false
    outputResetKey.value += 1
    return true
  }

  // applyOutput preserves exact terminal bytes while safely replacing gaps and new process sessions.
  function applyOutput(current: ServiceLogs, changedSession: boolean): AppliedLogs {
    if (!changedSession
      && !current.output.reset
      && (current.output.next_cursor < cursor
        || (current.output.text !== '' && current.output.next_cursor === cursor)
        || (current.output.has_more && current.output.next_cursor === cursor))) {
      throw new Error('Harbor returned service logs without advancing their byte cursor.')
    }

    const replace = changedSession || current.output.reset || current.output.truncated
    cursor = current.output.next_cursor
    if (replace) {
      output.value = current.output.text
      truncated.value = current.output.truncated || current.output.reset
      outputResetKey.value += 1
    }
    else if (current.output.text) {
      output.value += current.output.text
    }
    trimVisibleOutput()
    return {
      hasMore: current.output.has_more,
      recheckable: false,
      retryable: false,
      waitable: true,
    }
  }

  // trimVisibleOutput bounds renderer memory without splitting a JavaScript surrogate pair.
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

  // canRead excludes incomplete identities, stale capabilities, and disconnected daemon generations.
  function canRead() {
    return source.projectId.value !== ''
      && source.serviceId.value !== ''
      && source.supported.value
      && source.connected.value
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

  return { logs, output, outputResetKey, error, truncated, state, clear }
}
