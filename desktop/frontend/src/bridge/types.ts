import type { HarborSnapshot, HarborSnapshotEvent } from '@/domain/harbor'

export interface HarborBridge {
  getSnapshot(): Promise<HarborSnapshot>
  openResource(resourceId: string): Promise<void>
  subscribe(listener: (event: HarborSnapshotEvent) => void): () => void
}
