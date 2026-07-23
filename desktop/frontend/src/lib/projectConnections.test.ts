import { describe, expect, it } from 'vitest'
import type { ProjectSnapshot, ServicePort } from '@/domain/harbor'
import { projectServiceConnections } from './projectConnections'

function projectFixture(): ProjectSnapshot {
  return {
    id: 'project-test',
    name: 'test',
    path: '/workspace/test',
    slug: 'test',
    state: 'ready',
    favorite: false,
    updated_at: '2026-07-23T20:00:00Z',
    apps: [],
    services: [
      { id: 'mysql', name: 'MySQL', kind: 'database', state: 'ready', owner: 'compose', selection: 'selected', required: true },
      { id: 'mailpit', name: 'Mailpit', kind: 'mail', state: 'ready', owner: 'compose', selection: 'selected', required: false },
      { id: 'victoriametrics', name: 'VictoriaMetrics', kind: 'metrics', state: 'ready', owner: 'compose', selection: 'selected', required: false },
      { id: 'vmagent', name: 'VM Agent', kind: 'metrics', state: 'ready', owner: 'compose', selection: 'selected', required: false },
    ],
    resources: [
      {
        id: 'mailpit',
        name: 'Mailpit inbox',
        kind: 'mail',
        owner: { kind: 'service', service_id: 'mailpit' },
        url: 'http://127.77.59.75:8025/',
      },
      {
        id: 'victoria-metrics',
        name: 'VictoriaMetrics',
        kind: 'observability',
        owner: { kind: 'service', service_id: 'victoriametrics' },
        url: 'http://127.77.59.75:8428/select/0/prometheus',
      },
    ],
  }
}

describe('projectServiceConnections', () => {
  it('projects reachable native and HTTPS service endpoints without advertising container-only ports', () => {
    const ports: Record<string, ServicePort[]> = {
      mysql: [
        { address: '127.77.59.75', private: 3306, public: 3306, protocol: 'tcp', replica: 1 },
      ],
      mailpit: [
        { address: '127.77.59.75', private: 1025, public: 1025, protocol: 'tcp', replica: 1 },
        { address: '127.77.59.75', private: 8025, public: 8025, protocol: 'tcp', replica: 1 },
      ],
      victoriametrics: [
        { address: '127.77.59.75', private: 8428, public: 8428, protocol: 'tcp', replica: 1 },
      ],
      vmagent: [
        { private: 8429, protocol: 'tcp', replica: 1 },
      ],
    }

    const rows = projectServiceConnections(projectFixture(), ports)

    expect(rows.map((row) => ({
      service: row.service.id,
      endpoints: row.connections.map((connection) => connection.endpoint),
    }))).toEqual([
      { service: 'mysql', endpoints: ['mysql.test.test:3306'] },
      { service: 'mailpit', endpoints: ['https://mailpit.test.test'] },
      {
        service: 'victoriametrics',
        endpoints: [
          'https://victoria-metrics.test.test/select/0/prometheus',
          'victoriametrics.test.test:8428',
        ],
      },
      { service: 'vmagent', endpoints: [] },
    ])
  })

  it('shows only the one localhost port Harbor can relay for a native service', () => {
    const project = projectFixture()
    project.services = [project.services[0]!]
    project.resources = []

    const rows = projectServiceConnections(project, {
      mysql: [
        { address: '127.0.0.1', private: 33060, public: 43060, protocol: 'tcp', replica: 1 },
        { address: '127.0.0.1', private: 3306, public: 4306, protocol: 'tcp', replica: 1 },
      ],
    })

    expect(rows[0]?.connections.map((connection) => connection.endpoint)).toEqual([
      'mysql.test.test:3306',
    ])
  })
})
