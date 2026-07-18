export type HarborStatus = 'ready' | 'working' | 'degraded' | 'failed' | 'stopped' | 'unavailable'

export type Destination = 'overview' | 'projects' | 'services' | 'system'

export interface AppSummary {
  id: string
  name: string
  command: string
  status: HarborStatus
}

export interface ServiceSummary {
  id: string
  projectId: string
  projectName: string
  name: string
  kind: 'database' | 'cache' | 'mail' | 'observability'
  endpoint: string
  privateEndpoint: string
  status: HarborStatus
  owner: 'managed' | 'external'
}

export interface ResourceSummary {
  id: string
  projectId: string
  projectName: string
  serviceId?: string
  name: string
  kind: 'application' | 'api-reference' | 'lighthouse' | 'mail' | 'observability'
  url: string
}

export interface LogLine {
  id: number
  timestamp: string
  source: string
  stream: 'stdout' | 'stderr' | 'combined'
  message: string
}

export interface ProjectSummary {
  id: string
  name: string
  path: string
  domain: string
  status: HarborStatus
  favorite: boolean
  updatedAt: string
  apps: AppSummary[]
  services: ServiceSummary[]
  resources: ResourceSummary[]
  logs: LogLine[]
}

export interface SystemCheck {
  id: string
  name: string
  detail: string
  status: HarborStatus
}

export interface HarborSnapshot {
  sequence: number
  capturedAt: string
  projects: ProjectSummary[]
  services: ServiceSummary[]
  recentResources: ResourceSummary[]
  system: SystemCheck[]
}

export interface HarborSnapshotEvent {
  type: 'snapshot'
  snapshot: HarborSnapshot
}
