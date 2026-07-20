import type { ServiceSnapshot } from '@/domain/harbor'

export function countReadyServices(services: readonly ServiceSnapshot[]): number {
  return services.reduce((count, service) => count + Number(service.state === 'ready'), 0)
}

export function serviceOwnerLabel(owner: ServiceSnapshot['owner']): string {
  return owner === 'compose' ? 'Compose service' : 'External service'
}
