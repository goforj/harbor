import type { AddProjectResult, ConnectionEvent, DaemonStatus, HarborSnapshot, NetworkResolverPolicyMigrationOperation, NetworkSetupOperation, Operation, Problem, ProjectActivity, ProjectLifecycleOperation, ProjectRuntimeRepairConfirmation, ProjectRuntimeRepairInspection, ProjectUnregistration, ServiceLogs } from '@/domain/harbor'

export interface HarborWireFixture {
  methods: {
    add_project: 'AddProject'
    approve_project_removal: 'ApproveProjectRemoval'
    confirm_project_runtime_repair: 'ConfirmProjectRuntimeRepair'
    inspect_project_runtime_repair: 'InspectProjectRuntimeRepair'
    open_resource: 'OpenResource'
    open_terminal_url: 'OpenTerminalURL'
    resource_icon_url: 'ResourceIconURL'
    project_activity: 'ProjectActivity'
    service_logs: 'ServiceLogs'
    wait_service_logs: 'WaitServiceLogs'
    wait_project_activity: 'WaitProjectActivity'
    remove_project: 'RemoveProject'
    remove_old_networking: 'RemoveOldNetworking'
    snapshot: 'Snapshot'
    setup_network: 'SetupNetwork'
    start_project: 'StartProject'
    restart_project: 'RestartProject'
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
  approve_project_removal: ProjectUnregistration & { operation: Operation & { state: 'succeeded' } }
  project_activity: ProjectActivity & { session: NonNullable<ProjectActivity['session']> }
  service_logs: ServiceLogs & { session_id: string; supported: true; available: true }
  project_runtime_repair_inspection: ProjectRuntimeRepairInspection & { disposition: 'confirmable' }
  project_runtime_repair_not_actionable: ProjectRuntimeRepairInspection & { disposition: 'not_actionable'; reason: 'ambiguous' }
  project_runtime_repair_unsupported: ProjectRuntimeRepairInspection & { disposition: 'unsupported' }
  project_runtime_repair_confirmation: ProjectRuntimeRepairConfirmation & { project: ProjectRuntimeRepairConfirmation['project'] & { state: 'stopped' } }
  remove_project: ProjectUnregistration & { operation: Operation & { state: 'requires_approval' } }
  remove_old_networking: NetworkResolverPolicyMigrationOperation & { operation: Operation & { kind: 'network.resolver.policy-migration'; state: 'succeeded' } }
  setup_network: NetworkSetupOperation & { operation: Operation & { kind: 'network.setup'; state: 'succeeded' } }
  start_project: ProjectLifecycleOperation & { operation: Operation & { kind: 'project.start'; state: 'queued' } }
  stop_project: ProjectLifecycleOperation & { operation: Operation & { kind: 'project.stop'; state: 'queued' } }
  restart_project: ProjectLifecycleOperation & { operation: Operation & { kind: 'project.restart'; state: 'queued' } }
  terminal_operation: Operation & {
    state: 'failed'
    problem: Problem
    started_at: string
    finished_at: string
  }
}

export interface HarborBridge {
  addProject(): Promise<AddProjectResult>
  approveProjectRemoval(projectId: string, intentId: string): Promise<ProjectUnregistration>
  confirmProjectRuntimeRepair(projectId: string, inspectionId: string, candidateFingerprint: string): Promise<ProjectRuntimeRepairConfirmation>
  getStatus(): Promise<DaemonStatus>
  getSnapshot(): Promise<HarborSnapshot>
  getProjectActivity(projectId: string, sessionId: string, cursor: number): Promise<ProjectActivity>
  getServiceLogs(projectId: string, sessionId: string, serviceId: string, cursor: number): Promise<ServiceLogs>
  inspectProjectRuntimeRepair(projectId: string): Promise<ProjectRuntimeRepairInspection>
  waitProjectActivity(projectId: string, sessionId: string, cursor: number, waitMilliseconds: number): Promise<ProjectActivity>
  waitServiceLogs(projectId: string, sessionId: string, serviceId: string, cursor: number, waitMilliseconds: number): Promise<ServiceLogs>
  openResource(projectId: string, resourceId: string): Promise<void>
  openTerminalURL(url: string): Promise<void>
  getResourceIconURL(projectId: string, resourceId: string): Promise<string>
  removeProject(projectId: string, intentId: string): Promise<ProjectUnregistration>
  removeOldNetworking(): Promise<NetworkResolverPolicyMigrationOperation>
  setupNetwork(): Promise<NetworkSetupOperation>
  startProject(projectId: string, intentId: string): Promise<ProjectLifecycleOperation>
  stopProject(projectId: string, intentId: string): Promise<ProjectLifecycleOperation>
  restartProject(projectId: string, intentId: string): Promise<ProjectLifecycleOperation>
  subscribe(listener: (snapshot: HarborSnapshot) => void): () => void
  subscribeConnection(listener: (event: ConnectionEvent) => void): () => void
}
