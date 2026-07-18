import type { HarborBridge } from './types'
import type { HarborSnapshot, LogLine, ProjectSummary, ResourceSummary, ServiceSummary } from '@/domain/harbor'

const logMessages = [
  ['app', 'stdout', 'HTTP server listening on 127.0.0.11:43100'],
  ['mysql', 'stdout', 'ready for connections'],
  ['watch', 'combined', 'change detected in internal/orders/service.go'],
  ['app', 'stdout', 'build completed in 842ms'],
  ['ingress', 'stdout', 'route orders.test is ready'],
  ['metrics', 'stdout', 'collector scrape completed'],
  ['app', 'stdout', 'GET /health 200 1.8ms'],
  ['mailpit', 'stdout', 'SMTP listener ready on private endpoint'],
] as const

const logs: LogLine[] = Array.from({ length: 320 }, (_, index) => {
  const message = logMessages[index % logMessages.length]
  return {
    id: index + 1,
    timestamp: new Date(Date.UTC(2026, 6, 18, 14, 30, index)).toISOString(),
    source: message[0],
    stream: message[1],
    message: message[2],
  }
})

const services: ServiceSummary[] = [
  {
    id: 'orders-mysql',
    projectId: 'orders-api',
    projectName: 'orders-api',
    name: 'MySQL',
    kind: 'database',
    endpoint: 'mysql.orders.test:3306',
    privateEndpoint: '127.0.0.1:43106',
    status: 'ready',
    owner: 'managed',
  },
  {
    id: 'orders-redis',
    projectId: 'orders-api',
    projectName: 'orders-api',
    name: 'Redis',
    kind: 'cache',
    endpoint: 'redis.orders.test:6379',
    privateEndpoint: '127.0.0.1:43107',
    status: 'ready',
    owner: 'managed',
  },
  {
    id: 'billing-postgres',
    projectId: 'billing',
    projectName: 'billing',
    name: 'PostgreSQL',
    kind: 'database',
    endpoint: 'postgres.billing.test:5432',
    privateEndpoint: '127.0.0.1:43116',
    status: 'failed',
    owner: 'managed',
  },
  {
    id: 'storefront-mailpit',
    projectId: 'storefront',
    projectName: 'storefront',
    name: 'Mailpit',
    kind: 'mail',
    endpoint: 'mail.storefront.test',
    privateEndpoint: '127.0.0.1:43126',
    status: 'ready',
    owner: 'managed',
  },
]

const resources: ResourceSummary[] = [
  {
    id: 'orders-app',
    projectId: 'orders-api',
    projectName: 'orders-api',
    name: 'Application',
    kind: 'application',
    url: 'https://orders.test',
  },
  {
    id: 'orders-api-reference',
    projectId: 'orders-api',
    projectName: 'orders-api',
    name: 'API Reference',
    kind: 'api-reference',
    url: 'https://orders.test/swagger',
  },
  {
    id: 'orders-lighthouse',
    projectId: 'orders-api',
    projectName: 'orders-api',
    name: 'Lighthouse',
    kind: 'lighthouse',
    url: 'https://orders.test/lighthouse',
  },
  {
    id: 'storefront-mail',
    projectId: 'storefront',
    projectName: 'storefront',
    serviceId: 'storefront-mailpit',
    name: 'Mailpit',
    kind: 'mail',
    url: 'https://mail.storefront.test',
  },
]

const projects: ProjectSummary[] = [
  {
    id: 'orders-api',
    name: 'orders-api',
    path: '/workspace/apps/orders-api',
    domain: 'https://orders.test',
    status: 'ready',
    favorite: true,
    updatedAt: 'Rebuilt 2 minutes ago',
    apps: [
      { id: 'orders-app', name: 'app', command: 'forj dev', status: 'ready' },
      { id: 'orders-worker', name: 'worker', command: 'forj worker run', status: 'ready' },
    ],
    services: services.filter((service) => service.projectId === 'orders-api'),
    resources: resources.filter((resource) => resource.projectId === 'orders-api'),
    logs,
  },
  {
    id: 'billing',
    name: 'billing',
    path: '/workspace/apps/billing',
    domain: 'https://billing.test',
    status: 'failed',
    favorite: true,
    updatedAt: 'Failed 6 minutes ago',
    apps: [{ id: 'billing-app', name: 'app', command: 'forj dev', status: 'ready' }],
    services: services.filter((service) => service.projectId === 'billing'),
    resources: [],
    logs: logs.slice(0, 86),
  },
  {
    id: 'storefront',
    name: 'storefront',
    path: '/workspace/apps/storefront',
    domain: 'https://storefront.test',
    status: 'ready',
    favorite: false,
    updatedAt: 'Ready for 41 minutes',
    apps: [{ id: 'storefront-app', name: 'app', command: 'forj dev', status: 'ready' }],
    services: services.filter((service) => service.projectId === 'storefront'),
    resources: resources.filter((resource) => resource.projectId === 'storefront'),
    logs: logs.slice(0, 120),
  },
  {
    id: 'reports',
    name: 'reports',
    path: '/workspace/apps/reports',
    domain: 'https://reports.test',
    status: 'stopped',
    favorite: false,
    updatedAt: 'Stopped yesterday',
    apps: [{ id: 'reports-app', name: 'app', command: 'forj dev', status: 'stopped' }],
    services: [],
    resources: [],
    logs: [],
  },
]

const fixture: HarborSnapshot = {
  sequence: 42,
  capturedAt: '2026-07-18T14:35:20Z',
  projects,
  services,
  recentResources: resources,
  system: [
    { id: 'daemon', name: 'Harbor daemon', detail: 'Connected · sequence 42', status: 'ready' },
    { id: 'network', name: 'DNS and resolver', detail: '.test routing is healthy', status: 'ready' },
    { id: 'https', name: 'HTTPS and CA', detail: 'Trusted · 12 domains', status: 'ready' },
    { id: 'ingress', name: 'Ingress', detail: 'Ports 80 and 443 ready', status: 'ready' },
    { id: 'docker', name: 'Docker', detail: 'Docker Desktop connected', status: 'ready' },
    { id: 'updates', name: 'Updates', detail: 'Desktop preview build', status: 'degraded' },
  ],
}

export function createMockBridge(): HarborBridge {
  return {
    async getSnapshot() {
      return structuredClone(fixture)
    },
    async openResource(resourceId) {
      const resource = resources.find((entry) => entry.id === resourceId)
      if (!resource) {
        throw new Error(`Unknown resource: ${resourceId}`)
      }
      window.open(resource.url, '_blank', 'noopener,noreferrer')
    },
    subscribe() {
      return () => undefined
    },
  }
}

export function mockSnapshot(): HarborSnapshot {
  return structuredClone(fixture)
}
