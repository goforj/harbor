import { describe, expect, it } from 'vitest'
import type { ServiceSnapshot } from '@/domain/harbor'
import { countReadyServices, serviceOwnerLabel } from './servicePresentation'

function service(state: ServiceSnapshot['state']): ServiceSnapshot {
  return {
    id: `service-${state}`,
    name: state,
    kind: 'database',
    state,
    owner: 'compose',
    selection: 'selected',
    required: true,
  }
}

describe('service presentation', () => {
  it('counts only services reported as ready', () => {
    const services = [
      service('ready'),
      service('working'),
      service('degraded'),
      service('failed'),
      service('stopped'),
      service('unavailable'),
    ]

    expect(countReadyServices(services)).toBe(1)
  })

  it('describes service ownership in product language', () => {
    expect(serviceOwnerLabel('compose')).toBe('Compose service')
    expect(serviceOwnerLabel('external')).toBe('External service')
  })
})
