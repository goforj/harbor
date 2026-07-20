import { onBeforeUnmount, onMounted, ref, watch } from 'vue'
import type { Ref } from 'vue'
import type { ProjectActivity, ProjectSessionActivity } from '@/domain/harbor'

const activityPollInterval = 750
const activityRetryInterval = 2000
const maximumVisibleOutputCharacters = 256 * 1024
const maximumImmediateActivityReads = 8

export interface ProjectActivitySource {
  projectId: Readonly<Ref<string>>
  supported: Readonly<Ref<boolean>>
  connected: Readonly<Ref<boolean>>
  snapshotSequence: Readonly<Ref<number | undefined>>
  read(projectId: string, sessionId: string, cursor: number): Promise<ProjectActivity>
}

// useProjectActivity follows one project's current session without allowing overlapping cursor reads.
export function useProjectActivity(source: ProjectActivitySource) {
  const activity = ref<ProjectActivity | null>(null)
  const output = ref('')
  const error = ref<string | null>(null)
  const truncated = ref(false)
  let cursor = 0
  let sessionId = ''
  let timer: number | null = null
  let generation = 0
  let mounted = false
  let pollInFlight = false
  let immediatePollRequested = false

  watch(source.projectId, () => {
    invalidate(true)
    requestPoll(0)
  })

  watch([source.supported, source.connected], () => {
    invalidate(false)
    requestPoll(0)
  })

  watch(source.snapshotSequence, () => requestPoll(0))

  onMounted(() => {
    mounted = true
    requestPoll(0)
  })

  onBeforeUnmount(() => {
    mounted = false
    invalidate(false)
  })

  // invalidate retires every callback from the prior selection or connection generation.
  function invalidate(reset: boolean) {
    generation += 1
    clearTimer()
    pollInFlight = false
    immediatePollRequested = false
    if (!reset) return
    activity.value = null
    output.value = ''
    error.value = null
    truncated.value = false
    cursor = 0
    sessionId = ''
  }

  // requestPoll keeps at most one read active for a generation and coalesces immediate refreshes.
  function requestPoll(delay: number) {
    if (!mounted || !canPoll()) return
    if (pollInFlight) {
      if (delay === 0) immediatePollRequested = true
      return
    }
    clearTimer()
    const selectedGeneration = generation
    timer = window.setTimeout(() => {
      timer = null
      void poll(selectedGeneration)
    }, delay)
  }

  // poll drains already-retained chunks before returning to the normal live interval.
  async function poll(selectedGeneration: number) {
    if (!isCurrent(selectedGeneration) || pollInFlight) return
    pollInFlight = true
    let nextDelay = activityPollInterval
    try {
      for (let read = 0; read < maximumImmediateActivityReads; read += 1) {
        const current = await source.read(source.projectId.value, sessionId, cursor)
        if (!isCurrent(selectedGeneration)) return
        if (current.project_id !== source.projectId.value) {
          throw new Error('Harbor returned project activity for another project.')
        }
        activity.value = current
        error.value = null
        if (!current.session) break
        const hasMore = applySession(current.session)
        if (!hasMore) break
        nextDelay = 0
      }
    }
    catch (cause) {
      if (!isCurrent(selectedGeneration)) return
      error.value = cause instanceof Error
        ? cause.message
        : 'Harbor could not read project output.'
      nextDelay = activityRetryInterval
    }
    finally {
      if (!isCurrent(selectedGeneration)) return
      pollInFlight = false
      if (immediatePollRequested) {
        immediatePollRequested = false
        nextDelay = 0
      }
      requestPoll(nextDelay)
    }
  }

  // applySession advances a cursor only when the daemon still owns readable output for that exact session.
  function applySession(session: ProjectSessionActivity): boolean {
    const changedSession = sessionId !== '' && sessionId !== session.id
    const newSelection = sessionId !== session.id
    if (newSelection) sessionId = session.id

    if (!session.output.available) {
      if (newSelection) {
        cursor = 0
        output.value = ''
        truncated.value = false
      }
      return false
    }

    const replace = changedSession || session.output.reset || session.output.truncated
    cursor = session.output.next_cursor
    if (replace) {
      output.value = session.output.text
      truncated.value = session.output.truncated
    }
    else if (session.output.text) {
      output.value += session.output.text
    }
    trimVisibleOutput()
    return session.output.has_more
  }

  // trimVisibleOutput bounds renderer memory while preserving a valid JavaScript string boundary.
  function trimVisibleOutput() {
    if (output.value.length <= maximumVisibleOutputCharacters) return
    let start = output.value.length - maximumVisibleOutputCharacters
    const code = output.value.charCodeAt(start)
    if (code >= 0xdc00 && code <= 0xdfff) start += 1
    const newline = output.value.indexOf('\n', start)
    output.value = output.value.slice(newline >= 0 ? newline + 1 : start)
    truncated.value = true
  }

  // canPoll excludes stale bindings and disconnected daemon generations.
  function canPoll() {
    return source.projectId.value !== '' && source.supported.value && source.connected.value
  }

  // isCurrent fences asynchronous results against unmount, route changes, and reconnects.
  function isCurrent(selectedGeneration: number) {
    return mounted && selectedGeneration === generation && canPoll()
  }

  // clearTimer prevents a retired generation from starting another read.
  function clearTimer() {
    if (timer === null) return
    window.clearTimeout(timer)
    timer = null
  }

  return { activity, output, error, truncated }
}
