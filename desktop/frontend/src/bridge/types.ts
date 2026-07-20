import type { AddProjectResult, ConnectionEvent, DaemonStatus, HarborSnapshot, NetworkSetupOperation, Operation, Problem, ProjectActivity, ProjectLifecycleOperation, ProjectUnregistration } from '@/domain/harbor'

export interface HarborWireFixture {
  methods: {
    add_project: 'AddProject'
    open_resource: 'OpenResource'
    project_activity: 'ProjectActivity'
    wait_project_activity: 'WaitProjectActivity'
    remove_project: 'RemoveProject'
    snapshot: 'Snapshot'
    setup_network: 'SetupNetwork'
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
  project_activity: ProjectActivity & { session: NonNullable<ProjectActivity['session']> }
  remove_project: ProjectUnregistration & { operation: Operation & { state: 'requires_approval' } }
  setup_network: NetworkSetupOperation & { operation: Operation & { kind: 'network.setup'; state: 'succeeded' } }
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
  getProjectActivity(projectId: string, sessionId: string, cursor: number): Promise<ProjectActivity>
  waitProjectActivity(projectId: string, sessionId: string, cursor: number, waitMilliseconds: number): Promise<ProjectActivity>
  openResource(projectId: string, resourceId: string): Promise<void>
  removeProject(projectId: string, intentId: string): Promise<ProjectUnregistration>
  setupNetwork(): Promise<NetworkSetupOperation>
  startProject(projectId: string, intentId: string): Promise<ProjectLifecycleOperation>
  stopProject(projectId: string, intentId: string): Promise<ProjectLifecycleOperation>
  subscribe(listener: (snapshot: HarborSnapshot) => void): () => void
  subscribeConnection(listener: (event: ConnectionEvent) => void): () => void
}
