import type { Component } from 'vue'
import type { Destination } from '@/domain/harbor'
import {
  FolderKanban,
  LayoutDashboard,
  Server,
  Settings,
} from '@lucide/vue'

export interface HarborNavigationItem {
  destination: Destination
  label: string
  path: string
  icon: Component
}

export const harborNavigation: HarborNavigationItem[] = [
  {
    destination: 'overview',
    label: 'Overview',
    path: '/overview',
    icon: LayoutDashboard,
  },
  {
    destination: 'projects',
    label: 'Projects',
    path: '/projects',
    icon: FolderKanban,
  },
  {
    destination: 'services',
    label: 'Services',
    path: '/services',
    icon: Server,
  },
  {
    destination: 'system',
    label: 'System',
    path: '/system',
    icon: Settings,
  },
]

export function destinationFromPath(path: string): Destination {
  const match = harborNavigation.find((item) => path === item.path || path.startsWith(`${item.path}/`))
  return match?.destination ?? 'overview'
}
