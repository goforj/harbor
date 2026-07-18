import { harborWireFixture } from './harbor.fixture'
import type { HarborBridge } from './types'
import type { DaemonStatus, HarborSnapshot } from '@/domain/harbor'

const fixture = harborWireFixture

export function createMockBridge(): HarborBridge {
  return {
    async getStatus() {
      return structuredClone(fixture.status)
    },
    async getSnapshot() {
      return structuredClone(fixture.snapshot)
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
