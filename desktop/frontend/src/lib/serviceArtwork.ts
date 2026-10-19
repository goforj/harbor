import grafana from '@/assets/brand/services/grafana.svg'
import mysql from '@/assets/brand/services/mysql.svg'
import redis from '@/assets/brand/services/redis.svg'
import postgresql from '@/assets/brand/services/postgresql.svg'
import mariadb from '@/assets/brand/services/mariadb.svg'
import mongodb from '@/assets/brand/services/mongodb.svg'
import rabbitmq from '@/assets/brand/services/rabbitmq.svg'
import nats from '@/assets/brand/services/natsdotio.svg'
import kafka from '@/assets/brand/services/apachekafka.svg'
import minio from '@/assets/brand/services/minio.svg'
import prometheus from '@/assets/brand/services/prometheus.svg'
import opensearch from '@/assets/brand/services/opensearch.svg'
import clickhouse from '@/assets/brand/services/clickhouse.svg'
import victoria from '@/assets/brand/services/victoriametrics.svg'
import mailpit from '@/assets/brand/services/mailpit.svg'

interface ServiceArtwork {
  id: string
  logo: string
  color: string
  background?: string
  names: readonly string[]
  images: readonly string[]
}

const catalog: readonly ServiceArtwork[] = [
  { id: 'grafana', logo: grafana, color: '#f46800', names: ['grafana', 'grafana-seed'], images: ['grafana/grafana', 'grafana/grafana-oss', 'grafana/grafana-enterprise'] },
  { id: 'mysql', logo: mysql, color: '#4479a1', names: ['mysql'], images: ['mysql', 'mysql/mysql-server'] },
  { id: 'redis', logo: redis, color: '#ff4438', names: ['redis'], images: ['redis', 'redis/redis-stack', 'redis/redis-stack-server'] },
  { id: 'postgresql', logo: postgresql, color: '#4169e1', names: ['postgres', 'postgresql'], images: ['postgres'] },
  { id: 'mariadb', logo: mariadb, color: '#c0765a', names: ['mariadb'], images: ['mariadb'] },
  { id: 'mongodb', logo: mongodb, color: '#47a248', names: ['mongo', 'mongodb'], images: ['mongo', 'mongodb/mongodb-community-server'] },
  { id: 'mailpit', logo: mailpit, color: '#00b786', background: '#203743', names: ['mailpit'], images: ['axllent/mailpit'] },
  { id: 'victoriametrics', logo: victoria, color: '#8f6aca', names: ['victoriametrics', 'victoria-metrics', 'vmagent', 'vmalert'], images: ['victoriametrics/victoria-metrics', 'victoriametrics/vmagent', 'victoriametrics/vmalert'] },
  { id: 'rabbitmq', logo: rabbitmq, color: '#ff6600', names: ['rabbitmq'], images: ['rabbitmq'] },
  { id: 'nats', logo: nats, color: '#27aae1', names: ['nats'], images: ['nats'] },
  { id: 'kafka', logo: kafka, color: '#999999', names: ['kafka'], images: ['apache/kafka', 'apache/kafka-native', 'confluentinc/cp-kafka'] },
  { id: 'minio', logo: minio, color: '#c72e49', names: ['minio'], images: ['minio/minio', 'quay.io/minio/minio'] },
  { id: 'prometheus', logo: prometheus, color: '#e6522c', names: ['prometheus'], images: ['prom/prometheus'] },
  { id: 'opensearch', logo: opensearch, color: '#359ac4', names: ['opensearch', 'opensearch-dashboards'], images: ['opensearchproject/opensearch', 'opensearchproject/opensearch-dashboards'] },
  { id: 'clickhouse', logo: clickhouse, color: '#e3b341', names: ['clickhouse'], images: ['clickhouse/clickhouse-server'] },
]

// canonicalImageRepository strips a tag or digest while preserving custom registries and namespaces.
function canonicalImageRepository(image: string): string {
  const repository = image.trim().toLowerCase().split('@')[0]!.replace(/:[^/]*$/, '')
  return repository.replace(/^(?:docker\.io|index\.docker\.io)\//, '').replace(/^library\//, '')
}

// serviceArtwork uses an image when available and exact known aliases for existing name-only snapshots.
export function serviceArtwork(service: { id: string; name: string; image?: string }): ServiceArtwork | undefined {
  if (service.image) {
    const repository = canonicalImageRepository(service.image)
    return catalog.find(brand => brand.images.includes(repository))
  }
  const names = [service.id, service.name].map(name => name.toLowerCase().replaceAll('_', '-'))
  return catalog.find(brand => names.some(name => brand.names.includes(name)))
}
