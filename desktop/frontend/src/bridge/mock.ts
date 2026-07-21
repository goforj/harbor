import { harborWireFixture } from './harbor.fixture'
import type { HarborBridge } from './types'
import type { DaemonStatus, HarborSnapshot, NetworkSetupOperation, Operation, ProjectActivity, ProjectLifecycleOperation, ProjectRegistration, ProjectRuntimeRepairConfirmation, ProjectRuntimeRepairInspection, ProjectUnregistration, ServiceLogs } from '@/domain/harbor'

const fixture = harborWireFixture
type ConfirmableProjectRuntimeRepairInspection = Extract<ProjectRuntimeRepairInspection, { disposition: 'confirmable' }>

export function createMockBridge(): HarborBridge {
  const snapshot: HarborSnapshot = structuredClone(fixture.snapshot)
  const status: DaemonStatus = structuredClone(fixture.status)
  const removals = new Map<string, ProjectUnregistration>()
  const lifecycles = new Map<string, ProjectLifecycleOperation>()
  let networkSetup: NetworkSetupOperation | null = null
  let runtimeRepairPlan: ConfirmableProjectRuntimeRepairInspection | null = null

  // projectActivity applies the fixture's byte-addressed current-session cursor contract.
  function projectActivity(projectId: string, sessionId: string, cursor: number): ProjectActivity {
    const project = snapshot.projects.find((entry) => entry.id === projectId)
    if (!project) {
      throw new Error(`Unknown project: ${projectId}`)
    }
    if (project.state === 'stopped' || project.state === 'failed' || project.state === 'unavailable') {
      return { project_id: projectId }
    }

    const fixtureActivity: ProjectActivity = structuredClone(fixture.project_activity)
    if (!fixtureActivity.session) {
      return { project_id: projectId }
    }
    fixtureActivity.project_id = projectId
    fixtureActivity.session.id = `session-${projectId}`
    const output = fixtureActivity.session.output
    const nextCursor = output.next_cursor
    if (sessionId && sessionId !== fixtureActivity.session.id) {
      output.reset = true
    }
    else if (cursor > 0) {
      const encoded = new TextEncoder().encode(output.text)
      if (cursor > encoded.length || !isUTF8Boundary(encoded, cursor)) {
        output.reset = true
      }
      else {
        output.text = cursor === encoded.length
          ? ''
          : new TextDecoder().decode(encoded.slice(cursor))
      }
    }
    return fixtureActivity
  }

  // serviceLogs applies the same byte-cursor semantics as the native service stream.
  function serviceLogs(projectId: string, sessionId: string, serviceId: string, cursor: number): ServiceLogs {
    const project = snapshot.projects.find((entry) => entry.id === projectId)
    const service = project?.services.find((entry) => entry.id === serviceId)
    if (!project || !service) {
      throw new Error(`Unknown service: ${projectId}/${serviceId}`)
    }
    const currentSessionId = `service-logs-${projectId}-${serviceId}`
    const text = `\u001b[36m${service.name}\u001b[0m ready on ${project.slug}\n`
    const encoded = new TextEncoder().encode(text)
    let reset = sessionId !== '' && sessionId !== currentSessionId
    let outputText = text
    if (!reset && cursor > 0) {
      if (cursor > encoded.length || !isUTF8Boundary(encoded, cursor)) {
        reset = true
      }
      else {
        outputText = cursor === encoded.length
          ? ''
          : new TextDecoder().decode(encoded.slice(cursor))
      }
    }
    return {
      project_id: projectId,
      service_id: serviceId,
      session_id: currentSessionId,
      supported: true,
      available: service.state !== 'stopped' && service.state !== 'unavailable',
      output: {
        available: service.state !== 'stopped' && service.state !== 'unavailable',
        reset,
        truncated: false,
        has_more: false,
        next_cursor: encoded.length,
        text: outputText,
      },
      ports: serviceId === 'mysql'
        ? [{ address: '127.0.0.1', private: 3306, public: 3306, protocol: 'tcp', replica: 1 }]
        : [{ private: 6379, protocol: 'tcp', replica: 1 }],
    }
  }

  async function changeProjectLifecycle(projectId: string, intentId: string, action: 'start' | 'stop' | 'restart') {
    const previous = lifecycles.get(intentId)
    const kind = `project.${action}`
    if (previous) {
      if (previous.operation.project_id !== projectId || previous.operation.kind !== kind) {
        throw new Error('The lifecycle intent already belongs to another project action.')
      }
      return structuredClone(previous)
    }

    const project = snapshot.projects.find((entry) => entry.id === projectId)
    if (!project) {
      throw new Error(`Unknown project: ${projectId}`)
    }

    const revision = snapshot.sequence + 1
    const operation = structuredClone(
      action === 'start'
        ? fixture.start_project.operation
        : action === 'stop'
          ? fixture.stop_project.operation
          : fixture.restart_project.operation,
    )
    operation.id = `operation-${revision}-${action}-${projectId}`
    operation.intent_id = intentId
    operation.project_id = projectId
    operation.requested_at = new Date().toISOString()
    project.state = action === 'start' ? 'starting' : 'stopping'
    project.updated_at = operation.requested_at
    snapshot.operations = [
      ...snapshot.operations.filter((entry) => entry.project_id !== projectId
        || (entry.kind !== 'project.start' && entry.kind !== 'project.stop' && entry.kind !== 'project.restart')),
      operation,
    ]
    snapshot.sequence = revision
    status.sequence = revision
    const result = { operation, revision }
    lifecycles.set(intentId, structuredClone(result))
    return result
  }

  return {
    async addProject() {
      const registration: ProjectRegistration = structuredClone(fixture.add_project.registration)
      const existingIndex = snapshot.projects.findIndex((project) => project.id === registration.project.id)
      registration.created = existingIndex < 0
      if (existingIndex < 0) {
        snapshot.projects.push(registration.project)
      }
      else {
        registration.project = snapshot.projects[existingIndex]
      }
      snapshot.sequence = Math.max(snapshot.sequence, registration.revision)
      status.sequence = snapshot.sequence
      return { canceled: false, registration }
    },
    async getStatus() {
      return structuredClone(status)
    },
    async getSnapshot() {
      return structuredClone(snapshot)
    },
    async getProjectActivity(projectId, sessionId, cursor) {
      return projectActivity(projectId, sessionId, cursor)
    },
    async getServiceLogs(projectId, sessionId, serviceId, cursor) {
      return serviceLogs(projectId, sessionId, serviceId, cursor)
    },
    async inspectProjectRuntimeRepair(projectId) {
      const project = snapshot.projects.find((entry) => entry.id === projectId)
      if (!project) {
        throw new Error(`Unknown project: ${projectId}`)
      }

      const inspection: ConfirmableProjectRuntimeRepairInspection = structuredClone(fixture.project_runtime_repair_inspection)
      inspection.project_id = projectId
      inspection.confirmable.candidate.checkout = project.path
      runtimeRepairPlan = structuredClone(inspection)
      return inspection
    },
    async confirmProjectRuntimeRepair(projectId, inspectionId, candidateFingerprint) {
      const inspection = runtimeRepairPlan
      runtimeRepairPlan = null
      if (!inspection) {
        throw new Error('The stale runtime inspection is no longer available.')
      }
      if (inspection.project_id !== projectId
        || inspection.confirmable.inspection_id !== inspectionId
        || inspection.confirmable.candidate_fingerprint !== candidateFingerprint) {
        throw new Error('The stale runtime confirmation does not match its inspection.')
      }
      if (Date.parse(inspection.confirmable.expires_at) <= Date.now()) {
        throw new Error('The stale runtime inspection has expired.')
      }

      const projectIndex = snapshot.projects.findIndex((entry) => entry.id === projectId)
      if (projectIndex < 0) {
        throw new Error(`Unknown project: ${projectId}`)
      }
      const revision = snapshot.sequence + 1
      const repairedAt = new Date().toISOString()
      const project = structuredClone(snapshot.projects[projectIndex])
      project.state = 'stopped'
      project.updated_at = repairedAt
      project.apps = project.apps.map((app) => ({ ...app, state: 'stopped', active: false }))
      project.services = project.services.map((service) => ({ ...service, state: 'stopped' }))
      project.resources = []
      const confirmation: ProjectRuntimeRepairConfirmation = { project, revision }
      snapshot.projects[projectIndex] = project
      snapshot.operations = snapshot.operations.filter((operation) => operation.project_id !== projectId
        || (operation.kind !== 'project.start' && operation.kind !== 'project.stop' && operation.kind !== 'project.restart'))
      snapshot.recent_resource_ids = snapshot.recent_resource_ids.filter((reference) => reference.project_id !== projectId)
      snapshot.sequence = revision
      status.sequence = revision
      return confirmation
    },
    async waitProjectActivity(projectId, sessionId, cursor, waitMilliseconds) {
      // A synchronous caught-up fixture response would create a tight loop that native long-polling never produces.
      await new Promise((resolve) => window.setTimeout(resolve, Math.min(Math.max(waitMilliseconds, 1), 1000)))
      return projectActivity(projectId, sessionId, cursor)
    },
    async waitServiceLogs(projectId, sessionId, serviceId, cursor, waitMilliseconds) {
      // A fixture wait remains held briefly so browser development cannot spin on an empty cursor.
      await new Promise((resolve) => window.setTimeout(resolve, Math.min(Math.max(waitMilliseconds, 1), 1000)))
      return serviceLogs(projectId, sessionId, serviceId, cursor)
    },
    async openResource(projectId, resourceId) {
      const project = fixture.snapshot.projects.find((entry) => entry.id === projectId)
      const resource = project?.resources.find((entry) => entry.id === resourceId)
      if (!resource) {
        throw new Error(`Unknown resource: ${projectId}/${resourceId}`)
      }
      window.open(resource.url, '_blank', 'noopener,noreferrer')
    },
    async openTerminalURL(url) {
      window.open(url, '_blank', 'noopener,noreferrer')
    },
    async getResourceIconURL() {
      return ''
    },
    async removeProject(projectId, intentId) {
      const previous = removals.get(intentId)
      if (previous) {
        if (previous.operation.project_id !== projectId) {
          throw new Error('The removal intent already belongs to another project.')
        }
        return structuredClone(previous)
      }

      const projectIndex = snapshot.projects.findIndex((project) => project.id === projectId)
      if (projectIndex < 0) {
        throw new Error(`Unknown project: ${projectId}`)
      }

      const project = snapshot.projects[projectIndex]
      const revision = snapshot.sequence + 1
      const requestedAt = new Date().toISOString()
      const operation: Operation = structuredClone(fixture.remove_project.operation)
      operation.id = `operation-${revision}-${projectId}`
      operation.intent_id = intentId
      operation.project_id = projectId
      operation.requested_at = requestedAt
      operation.started_at = requestedAt

      if (project.state === 'stopped') {
        operation.state = 'succeeded'
        operation.phase = 'completed'
        operation.finished_at = requestedAt
        snapshot.projects.splice(projectIndex, 1)
        snapshot.operations = snapshot.operations.filter((entry) => entry.project_id !== projectId)
        snapshot.recent_resource_ids = snapshot.recent_resource_ids.filter((reference) => reference.project_id !== projectId)
      }
      else {
        operation.state = 'requires_approval'
        operation.phase = 'awaiting_host_approval'
        delete operation.finished_at
        snapshot.operations = [...snapshot.operations.filter((entry) => entry.id !== operation.id), operation]
      }

      snapshot.sequence = revision
      status.sequence = revision
      const result = { operation, revision }
      removals.set(intentId, structuredClone(result))
      return result
    },
    async approveProjectRemoval(projectId, intentId) {
      const pending = removals.get(intentId)
      if (!pending) {
        throw new Error('The project removal approval is no longer available.')
      }
      if (pending.operation.project_id !== projectId) {
        throw new Error('The removal approval intent already belongs to another project.')
      }
      if (pending.operation.state !== 'requires_approval') {
        return structuredClone(pending)
      }

      const projectIndex = snapshot.projects.findIndex((project) => project.id === projectId)
      if (projectIndex < 0) {
        throw new Error(`Unknown project: ${projectId}`)
      }

      const revision = snapshot.sequence + 1
      const completedAt = new Date().toISOString()
      const operation: Operation = structuredClone(fixture.approve_project_removal.operation)
      operation.id = pending.operation.id
      operation.intent_id = intentId
      operation.project_id = projectId
      operation.requested_at = pending.operation.requested_at
      operation.started_at = pending.operation.started_at ?? completedAt
      operation.finished_at = completedAt
      operation.state = 'succeeded'
      operation.phase = 'completed'
      snapshot.projects.splice(projectIndex, 1)
      snapshot.operations = snapshot.operations.filter((entry) => entry.project_id !== projectId)
      snapshot.recent_resource_ids = snapshot.recent_resource_ids.filter((reference) => reference.project_id !== projectId)
      snapshot.sequence = revision
      status.sequence = revision
      const result = { operation, revision }
      removals.set(intentId, structuredClone(result))
      return result
    },
    async setupNetwork() {
      if (networkSetup) {
        return structuredClone(networkSetup)
      }

      const revision = snapshot.sequence + 1
      const completedAt = new Date().toISOString()
      networkSetup = {
        operation: {
          id: `operation-${revision}-network-setup`,
          intent_id: 'intent-network-setup',
          kind: 'network.setup',
          state: 'succeeded',
          phase: 'completed',
          requested_at: completedAt,
          started_at: completedAt,
          finished_at: completedAt,
        },
        revision,
      }
      snapshot.sequence = revision
      status.sequence = revision
      return structuredClone(networkSetup)
    },
    startProject(projectId, intentId) {
      return changeProjectLifecycle(projectId, intentId, 'start')
    },
    stopProject(projectId, intentId) {
      return changeProjectLifecycle(projectId, intentId, 'stop')
    },
    restartProject(projectId, intentId) {
      return changeProjectLifecycle(projectId, intentId, 'restart')
    },
    subscribe() {
      return () => undefined
    },
    subscribeConnection() {
      return () => undefined
    },
  }
}

function isUTF8Boundary(value: Uint8Array, cursor: number): boolean {
  return cursor === value.length || (value[cursor]! & 0xc0) !== 0x80
}

export function mockStatus(): DaemonStatus {
  return structuredClone(fixture.status)
}

export function mockSnapshot(): HarborSnapshot {
  return structuredClone(fixture.snapshot)
}
