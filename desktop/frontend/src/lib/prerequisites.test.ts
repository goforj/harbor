import { describe, expect, it } from 'vitest'
import { prerequisitesReady, validatePrerequisites } from './prerequisites'
import type { PrerequisiteReport } from '@/domain/harbor'

const ready: PrerequisiteReport = { provider_id: 'goforj', checks: [{ id: 'go', name: 'Go', status: 'ready', detail: 'Available', required_for: 'creation' }] }

describe('prerequisite reports', () => {
  it('requires observed readiness and distinguishes creation from runtime-only requirements', () => {
    expect(prerequisitesReady(null)).toBe(false)
    expect(prerequisitesReady({ ...ready, checks: [] })).toBe(false)
    const report: PrerequisiteReport = { ...ready, checks: [...ready.checks, { id: 'docker', name: 'Docker', status: 'unavailable', detail: 'Start Docker', required_for: 'start' }] }
    expect(prerequisitesReady(report, 'creation')).toBe(true)
    expect(prerequisitesReady(report)).toBe(false)
    validatePrerequisites(report, 'goforj')
  })

  it.each(['provider', 'project', 'empty', 'duplicate', 'status', 'stage', 'identity', 'detail'])('rejects %s drift instead of enabling an action', mode => {
    const report = structuredClone(ready)
    if (mode === 'provider') report.provider_id = 'another-stack'
    if (mode === 'project') report.project_id = 'another-project'
    if (mode === 'empty') report.checks = []
    if (mode === 'duplicate') report.checks.push(report.checks[0]!)
    if (mode === 'status') report.checks[0]!.status = 'unknown' as never
    if (mode === 'stage') report.checks[0]!.required_for = 'unknown' as never
    if (mode === 'identity') report.checks[0]!.id = ''
    if (mode === 'detail') report.checks[0]!.detail = ''
    expect(() => validatePrerequisites(report, 'goforj')).toThrow('invalid prerequisite report')
  })
})
