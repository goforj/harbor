import { describe, expect, it } from 'vitest'
import { harborWireFixture } from '@/bridge/harbor.fixture'
import { redactSupportText, supportDiagnostics, type SupportDiagnosticsSource } from './supportDiagnostics'

function source(): SupportDiagnosticsSource {
  const snapshot = structuredClone(harborWireFixture.snapshot)
  const activity = structuredClone(harborWireFixture.project_activity)
  activity.project_id = snapshot.projects[0]!.id
  return {
    connection: 'connected', stale: false,
    status: structuredClone(harborWireFixture.status), snapshot,
    projectId: snapshot.projects[0]!.id, activity,
    outputFresh: true, output: 'HTTP GET /health 200',
  }
}

describe('support diagnostics', () => {
  it.each(['connecting', 'disconnected'] as const)('does not export cached data while %s', connection => {
    expect(supportDiagnostics({ ...source(), connection }, true, 'now')).toBeNull()
  })

  it('requires a fresh snapshot and the selected registered project', () => {
    expect(supportDiagnostics({ ...source(), stale: true }, true, 'now')).toBeNull()
    expect(supportDiagnostics({ ...source(), snapshot: null }, true, 'now')).toBeNull()
    expect(supportDiagnostics({ ...source(), projectId: 'absent' }, true, 'now')).toBeNull()
  })

  it('allowlists summary fields and excludes unknown fields, paths, authority, and environment values', () => {
    const current = source()
    Object.assign(current.snapshot!, { private_token: 'snapshot-secret', environment: { PASSWORD: 'environment-secret' }, operations: [{ ticket: 'helper-secret' }] })
    Object.assign(current.status!, { authorization: 'status-secret' })
    Object.assign(current.snapshot!.projects[0]!, { proof: 'ownership-secret' })
    const report = supportDiagnostics(current, false, '2026-10-21T00:00:00Z')!
    expect(report).toContain('Snapshot sequence:')
    expect(report).toContain('Project state:')
    expect(report).toContain('Development output: not included.')
    for (const value of ['snapshot-secret', 'environment-secret', 'helper-secret', 'status-secret', 'ownership-secret', current.snapshot!.projects[0]!.path, 'HTTP GET']) expect(report).not.toContain(value)
  })

  it('keeps daemon-only and absent status reports explicit', () => {
    const report = supportDiagnostics({ ...source(), projectId: undefined, status: null }, true, 'now')!
    expect(report).toContain('Daemon version: Unavailable')
    expect(report).not.toContain('Project state:')
    expect(report).not.toContain('HTTP GET')
  })

  it.each([
    { outputFresh: false },
    { outputError: 'permission denied: secret details' },
    { activity: null },
    { activity: { project_id: 'foreign', session: harborWireFixture.project_activity.session } },
  ])('excludes unavailable, stale, unauthorized, and mismatched activity: %j', change => {
    const report = supportDiagnostics({ ...source(), ...change }, true, 'now')!
    expect(report).toContain('No cached output was included')
    expect(report).not.toContain('HTTP GET')
    expect(report).not.toContain('secret details')
  })

  it('does not export retained text when the daemon no longer authorizes output', () => {
    const current = source()
    current.activity!.session!.output.available = false
    current.activity!.session!.output.historical = false
    expect(supportDiagnostics(current, true, 'now')).not.toContain('HTTP GET')
    current.activity!.session!.output.historical = true
    expect(supportDiagnostics(current, true, 'now')).toContain('Retained output')
    current.status!.capabilities = []
    expect(supportDiagnostics(current, true, 'now')).not.toContain('HTTP GET')
  })

  it('includes the reviewed output only after redaction, ANSI removal, and bounded UTF-8 clipping', () => {
    const current = source()
    current.output = `${'earlier\n'.repeat(60)}\u001b[31mHTTP GET /health 503\u001b[0m\nDB_PASSWORD=secret-value\n${'😀'.repeat(4000)}`
    const report = supportDiagnostics(current, true, 'now')!
    expect(report).toContain('HTTP GET /health 503')
    expect(report).not.toContain('secret-value')
    expect(report).not.toContain('\u001b')
    expect(report).not.toContain('�')
    expect(report).toContain('[Output omitted by retention or report limits]')
    expect(new TextEncoder().encode(report).length).toBeLessThanOrEqual(16 * 1024)
  })

  it('marks upstream truncation and refuses oversized unexpected sources', () => {
    expect(supportDiagnostics({ ...source(), outputTruncated: true }, true, 'now')).toContain('[Output omitted')
    const report = supportDiagnostics({ ...source(), output: 'private'.repeat(50_000) }, true, 'now')!
    expect(report).toContain('No cached output was included')
    expect(report).not.toContain('privateprivate')
  })

  it('keeps only recent startup records from the exact selected session', () => {
    const current = source()
    const session = current.activity!.session!
    session.startup = { events: [
      { schema_version: 1, session: 'foreign', sequence: 1, phase: 'task', status: 'failed', label: 'foreign-marker', elapsed_ms: 1 },
      { schema_version: 1, session: session.id, sequence: 2, phase: 'task', status: 'completed', label: 'DB_PASSWORD=startup-secret', elapsed_ms: 12 },
    ] }
    const report = supportDiagnostics(current, false, 'now')!
    expect(report).toContain('completed: DB_PASSWORD=[REDACTED] (12 ms)')
    expect(report).not.toContain('foreign-marker')
    expect(report).not.toContain('startup-secret')
  })
})

describe('support redaction', () => {
  it.each([
    ['at /opt/private project/main.go', '/opt/private project', '/opt/private project'],
    ['at /Users/alice/work/project/main.go', '', '/Users/alice'],
    ['at "C:\\Users\\Alice Smith\\work\\main.go"', '', 'Alice Smith'],
    ['at C:\\Users\\alice\\work\\main.go', '', 'alice'],
    ['at C:/private/project/main.go', 'C:\\private\\project', 'private/project'],
    ['Authorization: Bearer private-authorization', '', 'private-authorization'],
    ['Cookie: session=private-cookie', '', 'private-cookie'],
    ['https://user:private-password@example.test/path?api_key=query-secret', '', 'private-password'],
    ['{"password":"private value"}', '', 'private value'],
    ['-----BEGIN PRIVATE KEY-----\nunclosed-private-material', '', 'unclosed-private-material'],
    ['AKIA1234567890ABCDEF', '', 'AKIA1234567890ABCDEF'],
    ['eyJhbGciOiJ.testClaims.testSignature', '', 'testClaims'],
  ])('redacts %s', (input, checkout, secret) => {
    expect(redactSupportText(input, checkout)).not.toContain(secret)
  })
})
