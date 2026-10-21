import type { ProjectSessionActivity } from '@/domain/harbor'
import { terminalPlainText } from '@/lib/terminal'

const maximumExcerptCharacters = 6000
const maximumExcerptLines = 40
// Key scans stay bounded even when a compiler prints a long minified source line.
const sensitiveField = '[\\w.-]{0,96}(?:password|passwd|pwd|secret|token|api[_-]?key|private[_-]?key|access[_-]?key|credential|authorization|cookie|database[_-]?(?:url|dsn))[\\w.-]{0,96}'
const credentialAssignment = new RegExp(`((?:["']?${sensitiveField}["']?)\\s*[:=]\\s*)("(?:\\\\.|[^"\\\\])*"|'(?:\\\\.|[^'\\\\])*'|[^\\s,;]+)`, 'gi')
const credentialArgument = new RegExp(`(--${sensitiveField}\\s+)("[^"]*"|'[^']*'|\\S+)`, 'gi')

// redactFailureText removes common credential forms before an excerpt is clipped or copied.
export function redactFailureText(text: string): string {
  return text
    .replace(/-----BEGIN [^-\r\n]*PRIVATE KEY-----[\s\S]*?(?:-----END [^-\r\n]*PRIVATE KEY-----|$)/g, '[REDACTED PRIVATE KEY]')
    .replace(/^([\t ]*(?:authorization|proxy-authorization|cookie|set-cookie)[\t ]*:[\t ]*).*$/gmi, '$1[REDACTED]')
    .replace(/\b(Bearer|Basic)\s+[A-Za-z0-9+/_=.-]+/gi, '$1[REDACTED]')
    .replace(/\b([a-z][a-z0-9+.-]{0,31}:\/\/)([^\s<>"']+)/gi, (_match, scheme: string, rest: string) => {
      const authority = rest.split('/')[0] ?? ''
      const separator = authority.lastIndexOf('@')
      return scheme + (separator < 0 ? rest : `[REDACTED]@${rest.slice(separator + 1)}`)
    })
    .replace(/\b[^\s:@/]+:[^\s@]+@(?=(?:tcp|unix)\()/gi, '[REDACTED]@')
    .replace(credentialAssignment, '$1[REDACTED]')
    .replace(credentialArgument, '$1[REDACTED]')
    .replace(/\b(?:gh[pousr]_[A-Za-z0-9_]+|github_pat_[A-Za-z0-9_]+)\b/g, '[REDACTED]')
    .replace(/(?:\/Users\/|\/home\/)[^\s/]+/g, '~')
    .replace(/[A-Za-z]:\\Users\\[^\s\\]+/g, '~')
}

// projectFailureDetails derives presentation from the selected session without assigning runtime state.
export function projectFailureDetails(message: string, code: string, session: ProjectSessionActivity | undefined, output: string) {
  const events = [...(session?.startup?.events ?? [])].reverse()
  const failed = events.find(event => event.status === 'failed' && event.phase !== 'startup')
    ?? events.find(event => event.status === 'failed')
  const running = events.find(event => event.status === 'started'
    && !events.some(later => later.label === event.label && later.sequence > event.sequence))
  const conflicts = session?.diagnostics?.port_conflicts ?? []
  const timedOut = /readiness.*(?:timed? out|timeout)|(?:timed? out|timeout).*readiness/i.test(`${code} ${message}`)
  const cleanedMessage = message.replace(/; inspect Harbor's retained launch trace at [^\r\n]+/g, '')
  const plainOutput = redactFailureText(terminalPlainText(output)).trim()
  const lines = plainOutput ? plainOutput.split('\n') : []
  const tail = lines.slice(-maximumExcerptLines).join('\n')
  const excerpt = tail.slice(-maximumExcerptCharacters)
  const title = conflicts.length ? 'A service port is already in use'
    : timedOut ? 'Project readiness timed out'
      : failed ? 'Project startup failed'
        : code === 'project.process.exited' ? 'Development runtime stopped'
          : 'Project action failed'
  const guidance = conflicts.length
    ? 'Stop or reconfigure the competing workload, then retry this project. Harbor will not stop a workload it does not own.'
    : timedOut
      ? 'Check that the App starts and listens on its assigned address. Resolve the startup error before retrying.'
      : 'Inspect the available output and resolve the reported error before trying again.'

  return {
    title,
    message: redactFailureText(cleanedMessage || 'The development process could not complete startup.'),
    step: redactFailureText((failed ?? running)?.label ?? ''),
    stepLabel: failed ? 'Failed step' : 'Last startup step',
    guidance,
    excerpt,
    clipped: lines.length > maximumExcerptLines || tail.length > maximumExcerptCharacters,
    conflicts,
  }
}
