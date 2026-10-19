import { ref } from 'vue'

export const newProjectDialogOpen = ref(false)

// showNewProjectDialog shares one creation flow across navigation, views, and the command menu.
export function showNewProjectDialog() {
  newProjectDialogOpen.value = true
}
