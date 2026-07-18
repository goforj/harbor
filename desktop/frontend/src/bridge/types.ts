import type { ConnectionEvent, DaemonStatus, HarborSnapshot, Operation, Problem } from '@/domain/harbor'

export interface HarborWireFixture {
  methods: {
    open_resource: 'OpenResource'
    snapshot: 'Snapshot'
    status: 'Status'
  }
  events: {
    connection: 'harbor:connection'
    snapshot: 'harbor:snapshot'
  }
  connection_payloads: {
    connecting: ConnectionEvent & { state: 'connecting' }
    connected: ConnectionEvent & { state: 'connected' }
    disconnected: ConnectionEvent & { state: 'disconnected' }
  }
  status: DaemonStatus
  snapshot: HarborSnapshot
  terminal_operation: Operation & {
    state: 'failed'
    problem: Problem
    started_at: string
    finished_at: string
  }
}

export interface HarborBridge {
  getStatus(): Promise<DaemonStatus>
  getSnapshot(): Promise<HarborSnapshot>
  openResource(projectId: string, resourceId: string): Promise<void>
  subscribe(listener: (snapshot: HarborSnapshot) => void): () => void
  subscribeConnection(listener: (event: ConnectionEvent) => void): () => void
}
