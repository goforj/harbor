import type { ConnectionState, DaemonStatus, HarborSnapshot, ProjectActivity } from '@/domain/harbor'
import { redactFailureText } from '@/lib/projectFailure'
import { terminalPlainText } from '@/lib/terminal'

export interface SupportDiagnosticsSource {
  connection: ConnectionState
  stale: boolean
  status: DaemonStatus | null
  snapshot: HarborSnapshot | null
  projectId?: string
  activity?: ProjectActivity | null
  output?: string
  outputFresh?: boolean
  outputError?: string | null
  outputTruncated?: boolean
}

// limitUTF8 keeps clipboard reports bounded without splitting a Unicode code point.
function limitUTF8(text: string, maximum: number): string {
  const bytes = new TextEncoder().encode(text)
  if (bytes.length <= maximum) return text
  return new TextDecoder().decode(bytes.slice(0, maximum)).replace(/\uFFFD$/, '')
}

// redactSupportText omits known checkout and home paths in addition to common credential forms.
export function redactSupportText(text: string, checkout = ''): string {
  let result = text
  if (checkout && checkout.length <= 4096) {
    for (const path of new Set([checkout, checkout.replaceAll('\\', '/'), checkout.replaceAll('\\', '\\\\')])) {
      result = result.split(path).join('[PROJECT]')
    }
  }
  result = result
    .replace(/(["'])(?:\/(?:Users|home)\/|[A-Za-z]:\\Users\\)[^\r\n]*?\1/g, '[PRIVATE PATH]')
    .replace(/(?:\/(?:Users|home)\/|[A-Za-z]:\\Users\\)[^\s"'<>]+/g, '[PRIVATE PATH]')
  return redactFailureText(result)
    .replace(/\bAKIA[A-Z0-9]{16}\b/g, '[REDACTED]')
    .replace(/\beyJ[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+\b/g, '[REDACTED]')
    .replace(/[\u0000-\u0008\u000B-\u001F\u007F]/g, '')
}

// supportDiagnostics creates an allowlisted local summary, never a serialized snapshot or environment export.
export function supportDiagnostics(source: SupportDiagnosticsSource, includeOutput: boolean, capturedAt: string): string | null {
  if (source.connection !== 'connected' || source.stale || !source.snapshot) return null
  const project = source.projectId ? source.snapshot.projects.find(item => item.id === source.projectId) : undefined
  if (source.projectId && !project) return null
  const text = (value: string | undefined, maximum = 256) => {
    if (!value) return 'Unavailable'
    if (value.length > 16_384) return '[Value omitted]'
    return limitUTF8(redactSupportText(value, project?.path).replace(/[\r\n\t]/g, ' '), maximum)
  }
  const count = (value: number | undefined) => Number.isSafeInteger(value) && value! >= 0 ? String(value) : 'Unavailable'
  const status = source.status
  const snapshot = source.snapshot
  const lines = [
    'Harbor support diagnostics v1',
    `Captured: ${text(capturedAt)}`,
    'Source: authenticated desktop observations',
    `Daemon version: ${text(status?.build.version)}`,
    `Daemon revision: ${text(status?.build.revision)}`,
    `Modified build: ${status ? String(status.build.modified) : 'Unavailable'}`,
    `Protocol: ${status ? `${count(status.protocol.major)}.${count(status.protocol.minor)}` : 'Unavailable'}`,
    `Snapshot schema: ${count(snapshot.schema_version)}`,
    `Snapshot sequence: ${count(snapshot.sequence)}`,
    `Snapshot captured: ${text(snapshot.captured_at)}`,
    `Projects: ${count(snapshot.projects.length)}`,
    `Networking configured: ${snapshot.network_setup ? String(snapshot.network_setup.configured) : 'Unavailable'}`,
    `Networking setup pending: ${snapshot.network_setup ? String(!!snapshot.network_setup.pending) : 'Unavailable'}`,
    `Network capacity: ${count(snapshot.network_capacity)}`,
  ]
  if (project) {
    lines.push('', `Project state: ${text(project.state)}`, `Apps: ${count(project.apps.length)}`, `Services: ${count(project.services.length)}`)
    const activity = source.activity?.project_id === project.id ? source.activity : undefined
    const session = source.outputFresh && !source.outputError
      && status?.capabilities.includes('control.project-activity.v1') ? activity?.session : undefined
    lines.push(`Session: ${session ? text(session.state) : 'Unavailable'}`)
    if (session) {
      lines.push(`Session generation: ${count(session.generation)}`)
      const events = (session.startup?.events ?? []).filter(event => event.session === session.id).slice(-5)
      if (events.length) lines.push('Recent startup steps:', ...events.map(event => `  ${text(event.status, 32)}: ${text(event.label, 160)} (${count(event.elapsed_ms)} ms)`))
    }
    if (includeOutput) {
      const raw = source.output ?? ''
      if (!session || (!session.output.available && !session.output.historical) || raw.length > 256 * 1024) {
        lines.push('', 'Recent output: unavailable. No cached output was included.')
      }
      else {
        const clean = redactSupportText(terminalPlainText(raw), project.path).trim()
        const tail = clean.split('\n').slice(-40).join('\n')
        const excerpt = limitUTF8(tail, 6000)
        lines.push('', `${session.output.historical ? 'Retained' : 'Recent'} output (up to 40 lines / 6000 UTF-8 bytes):`, excerpt || '[No output]')
        if (source.outputTruncated || session.output.truncated || clean !== tail || tail !== excerpt) lines.push('[Output omitted by retention or report limits]')
      }
    }
    else lines.push('', 'Development output: not included.')
  }
  lines.push('', 'Project paths, environment files, helper authority, and raw snapshots are excluded from the summary.', 'Common credentials and known private paths are redacted. Review before sharing; arbitrary secrets or private paths may remain in logs.')
  // All variable-length fields above are bounded before assembly; this final cap also bounds clipboard cost.
  return limitUTF8(lines.join('\n'), 16 * 1024)
}
