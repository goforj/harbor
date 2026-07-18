import type { AddProjectResult, ConnectionEvent, DaemonStatus, HarborSnapshot, Operation, Problem } from '@/domain/harbor'

export interface HarborWireFixture {
  methods: {
    add_project: 'AddProject'
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
  add_project: AddProjectResult & { canceled: false; registration: NonNullable<AddProjectResult['registration']> }
  terminal_operation: Operation & {
    state: 'failed'
    problem: Problem
    started_at: string
    finished_at: string
  }
}

export interface HarborBridge {
  addProject(): Promise<AddProjectResult>
  getStatus(): Promise<DaemonStatus>
  getSnapshot(): Promise<HarborSnapshot>
  openResource(projectId: string, resourceId: string): Promise<void>
  subscribe(listener: (snapshot: HarborSnapshot) => void): () => void
  subscribeConnection(listener: (event: ConnectionEvent) => void): () => void
}
