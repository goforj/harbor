import type { ProjectSnapshot, ServicePort, ServiceSnapshot } from '@/domain/harbor'

export interface ServiceConnection {
  id: string
  endpoint: string
  hostname: string
  label: string
  port: number
  protocol: string
  source: 'native' | 'resource'
}

export interface ProjectServiceConnections {
  service: ServiceSnapshot
  connections: ServiceConnection[]
}

type ServicePorts = Readonly<Record<string, readonly ServicePort[]>>

export function projectServiceConnections(
  project: ProjectSnapshot,
  portsByService: ServicePorts,
): ProjectServiceConnections[] {
  return project.services.map((service) => {
    const resources = project.resources.filter((resource) => (
      resource.owner.kind === 'service' && resource.owner.service_id === service.id
    ))
    const resourceConnections = resources.map((resource): ServiceConnection => {
      const upstream = new URL(resource.url)
      const hostname = resource.id === 'app-http'
        ? `${project.slug}.test`
        : `${resource.id}.${project.slug}.test`
      const path = upstream.pathname === '/' ? '' : upstream.pathname
      return {
        id: `resource:${resource.id}`,
        endpoint: `https://${hostname}${path}`,
        hostname,
        label: resource.name,
        port: 443,
        protocol: 'HTTPS',
        source: 'resource',
      }
    })

    const serviceHostnameClaimed = project.resources.some((resource) => resource.id === service.id)
    const nativeConnections = serviceHostnameClaimed
      ? []
      : nativeServiceConnections(project.slug, service, portsByService[service.id] ?? [])

    return {
      service,
      connections: [...resourceConnections, ...nativeConnections],
    }
  })
}

function nativeServiceConnections(
  slug: string,
  service: ServiceSnapshot,
  ports: readonly ServicePort[],
): ServiceConnection[] {
  const hostname = `${service.id}.${slug}.test`
  const published = ports.filter((port) => (
    port.protocol.toLowerCase() === 'tcp'
    && port.private > 0
    && (port.public ?? 0) > 0
    && isIPv4Loopback(port.address)
  ))
  const direct = published.filter((port) => port.address !== '127.0.0.1')
  const reachable = direct.length > 0
    ? direct
    : [...published]
        .sort(compareServicePorts)
        .slice(0, 1)

  const seen = new Set<string>()
  const connections: ServiceConnection[] = []
  for (const port of reachable) {
    const endpointPort = direct.length > 0 ? port.public! : port.private
    const endpoint = `${hostname}:${endpointPort}`
    if (seen.has(endpoint)) continue
    seen.add(endpoint)
    connections.push({
      id: `native:${service.id}:${endpointPort}`,
      endpoint,
      hostname,
      label: service.name,
      port: endpointPort,
      protocol: port.protocol.toUpperCase(),
      source: 'native',
    })
  }
  return connections
}

function isIPv4Loopback(address: string | undefined): boolean {
  if (!address) return false
  const octets = address.split('.')
  if (octets.length !== 4 || octets[0] !== '127') return false
  return octets.every((octet) => {
    if (!/^\d+$/.test(octet)) return false
    const value = Number(octet)
    return value >= 0 && value <= 255
  })
}

function compareServicePorts(left: ServicePort, right: ServicePort): number {
  if (left.private !== right.private) return left.private - right.private
  return (left.public ?? 0) - (right.public ?? 0)
}
