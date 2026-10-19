import { Activity, AppWindow, BookOpen, Code2, Database, FolderKanban, FolderOpen, FolderPlus, HardDrive, Layers, LayoutDashboard, ListOrdered, Mail, Monitor, Moon, Palette, Radar, RefreshCw, Server, Settings, Sun, Waypoints } from '@lucide/vue'

// presentationIcon keeps navigation, actions, resources, and services in one visual vocabulary.
export function presentationIcon(kind: string) {
  switch (kind) {
    case 'overview': return { icon: LayoutDashboard, tone: 'docs' }
    case 'projects': return { icon: FolderKanban, tone: 'application' }
    case 'services':
    case 'daemon': return { icon: Server, tone: 'service' }
    case 'system': return { icon: Settings, tone: 'data' }
    case 'appearance': return { icon: Palette, tone: 'data' }
    case 'new-project': return { icon: FolderPlus, tone: 'application' }
    case 'import-project': return { icon: FolderOpen, tone: 'docs' }
    case 'refresh': return { icon: RefreshCw, tone: 'mail' }
    case 'theme-system': return { icon: Monitor, tone: 'service' }
    case 'theme-dark': return { icon: Moon, tone: 'data' }
    case 'theme-light': return { icon: Sun, tone: 'cache' }
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
