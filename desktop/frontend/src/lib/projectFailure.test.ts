import { describe, expect, it } from 'vitest'
import { harborWireFixture } from '@/bridge/harbor.fixture'
import type { ProjectSessionActivity } from '@/domain/harbor'
import { projectFailureDetails, redactFailureText } from './projectFailure'

describe('project failure details', () => {
  it('uses the specific failed phase instead of the overall startup record', () => {
    const session: ProjectSessionActivity = structuredClone(harborWireFixture.project_activity.session)
    session.startup = { events: [
      { schema_version: 1, session: session.id, sequence: 1, phase: 'frontend.build', label: 'Build frontend', status: 'failed', elapsed_ms: 20 },
      { schema_version: 1, session: session.id, sequence: 2, phase: 'startup', label: 'Startup', status: 'failed', elapsed_ms: 21 },
    ] }
    const result = projectFailureDetails('Process exited', 'project.process.exited', session, '')
    expect(result.title).toBe('Project startup failed')
    expect(result.step).toBe('Build frontend')
    expect(result.stepLabel).toBe('Failed step')
  })

  it('does not label an interrupted running step as a proven failure', () => {
    const session: ProjectSessionActivity = structuredClone(harborWireFixture.project_activity.session)
    session.startup = { events: [{ schema_version: 1, session: session.id, sequence: 1, phase: 'task', label: 'Wait for MySQL', status: 'started', elapsed_ms: 0 }] }
    const result = projectFailureDetails('Process exited', 'project.process.exited', session, '')
    expect(result.step).toBe('Wait for MySQL')
    expect(result.stepLabel).toBe('Last startup step')
  })

  it('bounds long output after redaction and removes terminal escapes', () => {
    const output = [...Array.from({ length: 60 }, (_, index) => `Earlier ${index}`), '\u001b[31merror: build failed\u001b[0m', 'DB_PASSWORD="not for sharing"'].join('\n')
    const result = projectFailureDetails('', '', undefined, output)
    expect(result.clipped).toBe(true)
    expect(result.excerpt.split('\n')).toHaveLength(40)
    expect(result.excerpt).toContain('error: build failed')
    expect(result.excerpt).not.toContain('\u001b')
    expect(result.excerpt).not.toContain('not for sharing')
    expect(projectFailureDetails('', '', undefined, 'x'.repeat(9000)).excerpt).toHaveLength(6000)
  })

  it('handles missing output and classifies readiness without changing session state', () => {
    const result = projectFailureDetails('Readiness timed out', 'project.readiness.timeout', undefined, '')
    expect(result.title).toBe('Project readiness timed out')
    expect(result.excerpt).toBe('')
    expect(result.step).toBe('')
  })

  it('removes the obsolete file-hunting instruction from the visible message', () => {
    expect(projectFailureDetails("project runtime exited before readiness; inspect Harbor's retained launch trace at _data/harbor/forj-dev.log", '', undefined, '').message).toBe('project runtime exited before readiness')
  })
})

describe('failure excerpt redaction', () => {
  it.each([
    ['DB_PASSWORD="two secret words"', 'two secret words'],
    ['{"api_key":"json-secret-value"}', 'json-secret-value'],
    ['--access-token cli-secret-value', 'cli-secret-value'],
    ['Authorization: Bearer authorization-secret', 'authorization-secret'],
    ['Cookie: session=cookie-secret; extra=other-secret', 'other-secret'],
    ['GET https://user:url-secret@example.test/api?token=query-secret', 'url-secret'],
    ['GET https://example.test/api?token=query-secret', 'query-secret'],
    ['connect user:dsn-secret@tcp(127.0.0.1:3306)/app', 'dsn-secret'],
    ['git ghp_privateTokenValue', 'privateTokenValue'],
    ['-----BEGIN PRIVATE KEY-----\nprivate-key-value\n-----END PRIVATE KEY-----', 'private-key-value'],
    ['-----BEGIN RSA PRIVATE KEY-----\nunfinished-key-value', 'unfinished-key-value'],
    ['error /Users/alice/code/app/main.go', 'alice'],
    ['error C:\\Users\\alice\\app\\main.go', 'alice'],
  ])('redacts %s', (input, secret) => {
    expect(redactFailureText(input)).not.toContain(secret)
  })

  it('keeps non-secret build context readable', () => {
    const output = 'HTTP GET /health 503\ninternal/photos/controller.go:42: undefined: missing\nAPI_HTTP_PORT=3000'
    expect(redactFailureText(output)).toBe(output)
    expect(redactFailureText('x'.repeat(20000))).toBe('x'.repeat(20000))
  })
})
