export type Destination = 'overview' | 'projects' | 'services' | 'system'

export type ConnectionState = 'connecting' | 'connected' | 'disconnected'

export interface ConnectionEvent {
  state: ConnectionState
}

export type ProjectState =
  | 'stopped'
  | 'starting'
  | 'ready'
  | 'rebuilding'
  | 'degraded'
  | 'failed'
  | 'stopping'
  | 'unavailable'

export type EntityState = 'ready' | 'working' | 'degraded' | 'failed' | 'stopped' | 'unavailable'

export type OperationState =
  | 'queued'
  | 'running'
  | 'requires_approval'
  | 'succeeded'
  | 'failed'
  | 'cancelled'

export type HarborStatus = ProjectState | EntityState | OperationState

export interface ProtocolVersion {
  major: number
  minor: number
}

export interface DaemonBuild {
  version: string
  revision?: string
  modified: boolean
}

export interface DaemonStatus {
  state: 'ready'
  build: DaemonBuild
  protocol: ProtocolVersion
  capabilities: string[]
  snapshot_schema_version: number
  sequence: number
}

export interface AppSnapshot {
  id: string
  name: string
  state: EntityState
  active: boolean
  required: boolean
}

export interface ServiceSnapshot {
  id: string
  name: string
  kind: string
  state: EntityState
  owner: 'compose' | 'external'
  selection: 'selected' | 'available'
  required: boolean
}

export interface ResourceOwner {
  kind: 'app' | 'service'
  app_id?: string
  service_id?: string
}

export interface ResourceSnapshot {
  id: string
  name: string
  kind: string
  owner: ResourceOwner
  url: string
}

export interface ProjectSnapshot {
  id: string
  name: string
  path: string
  slug: string
  state: ProjectState
  favorite: boolean
  updated_at: string
  apps: AppSnapshot[]
  services: ServiceSnapshot[]
  resources: ResourceSnapshot[]
}

export interface ProjectRegistration {
  project: ProjectSnapshot
  revision: number
  created: boolean
}

export interface AddProjectResult {
  canceled: boolean
  registration?: ProjectRegistration
}

export interface ProjectUnregistration {
  operation: Operation
  revision: number
}

export interface ProjectLifecycleOperation {
  operation: Operation
  revision: number
}

export interface NetworkSetupOperation {
  operation: Operation
  revision: number
}

export interface Problem {
  code: string
  message: string
  retryable: boolean
}

export interface Operation {
  id: string
  intent_id: string
  kind: string
  project_id?: string
  state: OperationState
  phase: string
  problem?: Problem
  requested_at: string
  started_at?: string
  finished_at?: string
}

export interface ResourceRef {
  project_id: string
  resource_id: string
}

export interface HarborSnapshot {
  schema_version: number
  sequence: number
  captured_at: string
  projects: ProjectSnapshot[]
  operations: Operation[]
  recent_resource_ids: ResourceRef[]
}

export interface ProjectService extends ServiceSnapshot {
  project_id: string
  project_name: string
}

export interface ProjectResource extends ResourceSnapshot {
  project_id: string
  project_name: string
}
