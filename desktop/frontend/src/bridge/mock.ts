import { harborWireFixture } from './harbor.fixture'
import type { HarborBridge } from './types'
import type { DaemonStatus, HarborSnapshot, ProjectRegistration } from '@/domain/harbor'

const fixture = harborWireFixture

export function createMockBridge(): HarborBridge {
  const snapshot: HarborSnapshot = structuredClone(fixture.snapshot)
  const status: DaemonStatus = structuredClone(fixture.status)

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
