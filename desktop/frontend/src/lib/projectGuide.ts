import { ref } from 'vue'

// projectGuideState keeps navigation intent in this UI session; project and networking facts remain in daemon snapshots.
export const projectGuideState = ref({ offered: false, visible: false, projectId: '', appOpened: false })

// offerProjectGuide offers the guide only from the first fresh empty snapshot, never retained or disconnected state.
export function offerProjectGuide(projectCount: number, fresh: boolean) {
  if (!fresh || projectGuideState.value.offered) return
  projectGuideState.value.offered = true
  projectGuideState.value.visible = projectCount === 0
}

// showProjectGuide makes the same guide reachable from Overview and command search.
export function showProjectGuide() {
  projectGuideState.value.offered = true
  projectGuideState.value.visible = true
}

// selectProjectGuideProject follows one registration without confusing it with another project's progress.
export function selectProjectGuideProject(projectId: string) {
  if (projectGuideState.value.projectId !== projectId) projectGuideState.value.appOpened = false
  projectGuideState.value.projectId = projectId
}
