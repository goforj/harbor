import type { AddProjectResult, ConnectionEvent, DaemonStatus, HarborSnapshot, Operation, Problem, ProjectLifecycleOperation, ProjectUnregistration } from '@/domain/harbor'

export interface HarborWireFixture {
  methods: {
    add_project: 'AddProject'
    open_resource: 'OpenResource'
    remove_project: 'RemoveProject'
    snapshot: 'Snapshot'
    start_project: 'StartProject'
    status: 'Status'
    stop_project: 'StopProject'
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
  remove_project: ProjectUnregistration & { operation: Operation & { state: 'requires_approval' } }
  start_project: ProjectLifecycleOperation & { operation: Operation & { kind: 'project.start'; state: 'queued' } }
  stop_project: ProjectLifecycleOperation & { operation: Operation & { kind: 'project.stop'; state: 'queued' } }
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
  removeProject(projectId: string, intentId: string): Promise<ProjectUnregistration>
  startProject(projectId: string, intentId: string): Promise<ProjectLifecycleOperation>
  stopProject(projectId: string, intentId: string): Promise<ProjectLifecycleOperation>
  subscribe(listener: (snapshot: HarborSnapshot) => void): () => void
  subscribeConnection(listener: (event: ConnectionEvent) => void): () => void
}
