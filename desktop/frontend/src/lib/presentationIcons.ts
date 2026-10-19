import { Activity, AppWindow, BookOpen, Code2, Database, HardDrive, Layers, ListOrdered, Mail, Radar, Server, Waypoints } from '@lucide/vue'

// presentationIcon gives resource and service kinds one consistent visual vocabulary.
export function presentationIcon(kind: string) {
  switch (kind) {
    case 'application': return { icon: AppWindow, tone: 'application' }
    case 'api-reference':
    case 'docs': return { icon: BookOpen, tone: 'docs' }
    case 'api-index': return { icon: Code2, tone: 'docs' }
    case 'lighthouse': return { icon: Radar, tone: 'docs' }
    case 'database': return { icon: Database, tone: 'data' }
    case 'cache': return { icon: Layers, tone: 'cache' }
    case 'mail': return { icon: Mail, tone: 'mail' }
    case 'queue': return { icon: ListOrdered, tone: 'service' }
    case 'events': return { icon: Waypoints, tone: 'service' }
    case 'storage': return { icon: HardDrive, tone: 'data' }
    case 'metrics':
    case 'observability': return { icon: Activity, tone: 'cache' }
    default: return { icon: Server, tone: 'service' }
  }
}
