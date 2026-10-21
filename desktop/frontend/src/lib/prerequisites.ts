import type { PrerequisiteReport } from '@/domain/harbor'

// prerequisitesReady requires an observed, nonempty report before authoring or a new Start request proceeds.
export function prerequisitesReady(report?: PrerequisiteReport | null, stage: 'creation' | 'start' = 'start'): boolean {
  return Boolean(report?.checks.length) && report!.checks.every(check => check.status === 'ready' || (stage === 'creation' && check.required_for === 'start'))
}

// validatePrerequisites fences readiness to its requested provider/project and rejects unsupported observation states.
export function validatePrerequisites(report: PrerequisiteReport, providerId?: string, projectId?: string): void {
  if (!report || !report.provider_id || (providerId && report.provider_id !== providerId)
    || report.project_id !== projectId || !Array.isArray(report.checks) || !report.checks.length || report.checks.length > 16
    || new Set(report.checks.map(check => check.id)).size !== report.checks.length
    || report.checks.some(check => !check.id || !check.name || !check.detail || !['ready', 'missing', 'incompatible', 'unavailable'].includes(check.status)
      || (check.required_for != null && !['creation', 'start'].includes(check.required_for)))) {
    throw new Error('Harbor returned an invalid prerequisite report. Recheck before continuing.')
  }
}
