import { describe, expect, it } from 'vitest'
import { serviceArtwork } from './serviceArtwork'

const service = { id: 'custom', name: 'Custom' }

describe('service artwork', () => {
  it.each([
    ['docker.io/library/mysql:8.4', 'mysql'],
    ['redis@sha256:1234567890abcdef', 'redis'],
    ['grafana/grafana:latest', 'grafana'],
    ['axllent/mailpit:v1.30', 'mailpit'],
    ['victoriametrics/vmagent:v1', 'victoriametrics'],
    ['quay.io/minio/minio:latest', 'minio'],
  ])('resolves the complete image reference %s', (image, id) => {
    expect(serviceArtwork({ ...service, image })?.id).toBe(id)
  })

  it('uses existing service aliases, including helper services', () => {
    expect(serviceArtwork({ id: 'grafana-seed', name: 'grafana-seed' })?.id).toBe('grafana')
    expect(serviceArtwork({ id: 'vmagent', name: 'vmagent' })?.id).toBe('victoriametrics')
    expect(serviceArtwork({ id: 'db', name: 'MySQL' })?.id).toBe('mysql')
  })

  it('keeps unknown and private images generic even when names resemble a known vendor', () => {
    expect(serviceArtwork({ ...service, id: 'redis', image: 'registry.local:5000/private/redis:latest' })).toBeUndefined()
    expect(serviceArtwork({ ...service, image: 'someone/mysql:8' })).toBeUndefined()
    expect(serviceArtwork({ ...service, id: 'my-grafana-custom' })).toBeUndefined()
    expect(serviceArtwork({ ...service, id: 'rapidocr' })).toBeUndefined()
  })
})
