import type { Component } from 'vue'
import type { Destination } from '@/domain/harbor'
import {
  FolderKanban,
  LayoutDashboard,
  Palette,
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
    label: 'Settings',
    path: '/settings',
    icon: Settings,
  },
]

export function destinationFromPath(path: string): Destination {
  const match = harborNavigation.find((item) => path === item.path || path.startsWith(`${item.path}/`))
  return match?.destination ?? 'overview'
}

export const harborSettingsNavigation = [
  { label: 'Appearance', description: 'App and terminal themes', path: '/settings/appearance', section: 'appearance', icon: Palette },
  { label: 'Daemon', description: 'Connection and diagnostics', path: '/settings/daemon', section: 'daemon', icon: Server },
] as const
