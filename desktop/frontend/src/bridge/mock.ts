import { harborWireFixture } from './harbor.fixture'
import type { HarborBridge } from './types'
import type { DaemonStatus, HarborSnapshot, NetworkSetupOperation, Operation, ProjectLifecycleOperation, ProjectRegistration, ProjectUnregistration } from '@/domain/harbor'

const fixture = harborWireFixture

export function createMockBridge(): HarborBridge {
  const snapshot: HarborSnapshot = structuredClone(fixture.snapshot)
  const status: DaemonStatus = structuredClone(fixture.status)
  const removals = new Map<string, ProjectUnregistration>()
  const lifecycles = new Map<string, ProjectLifecycleOperation>()
  let networkSetup: NetworkSetupOperation | null = null

  async function changeProjectLifecycle(projectId: string, intentId: string, action: 'start' | 'stop') {
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
    const operation = structuredClone(action === 'start' ? fixture.start_project.operation : fixture.stop_project.operation)
    operation.id = `operation-${revision}-${action}-${projectId}`
    operation.intent_id = intentId
    operation.project_id = projectId
    operation.requested_at = new Date().toISOString()
    project.state = action === 'start' ? 'starting' : 'stopping'
    project.updated_at = operation.requested_at
    snapshot.operations = [
      ...snapshot.operations.filter((entry) => entry.project_id !== projectId
        || (entry.kind !== 'project.start' && entry.kind !== 'project.stop')),
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
    async openResource(projectId, resourceId) {
      const project = fixture.snapshot.projects.find((entry) => entry.id === projectId)
      const resource = project?.resources.find((entry) => entry.id === resourceId)
      if (!resource) {
        throw new Error(`Unknown resource: ${projectId}/${resourceId}`)
      }
      window.open(resource.url, '_blank', 'noopener,noreferrer')
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
    subscribe() {
      return () => undefined
    },
    subscribeConnection() {
      return () => undefined
    },
  }
}

export function mockStatus(): DaemonStatus {
  return structuredClone(fixture.status)
}

export function mockSnapshot(): HarborSnapshot {
  return structuredClone(fixture.snapshot)
}
