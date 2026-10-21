import { beforeEach, describe, expect, it } from 'vitest'
import { offerProjectGuide, projectGuideState, selectProjectGuideProject, showProjectGuide } from './projectGuide'

describe('first-project guide session', () => {
  beforeEach(() => { projectGuideState.value = { offered: false, visible: false, projectId: '', appOpened: false } })

  it('offers automatically only for the first fresh empty snapshot', () => {
    offerProjectGuide(0, false)
    expect(projectGuideState.value.offered).toBe(false)
    offerProjectGuide(0, true)
    expect(projectGuideState.value.visible).toBe(true)
    projectGuideState.value.visible = false
    offerProjectGuide(0, true)
    expect(projectGuideState.value.visible).toBe(false)
  })

  it('keeps established workspaces quiet and lets users reopen the guide', () => {
    offerProjectGuide(2, true)
    expect(projectGuideState.value.visible).toBe(false)
    showProjectGuide()
    expect(projectGuideState.value.visible).toBe(true)
  })

  it('keeps link-open progress attached to the selected registration', () => {
    selectProjectGuideProject('first')
    projectGuideState.value.appOpened = true
    selectProjectGuideProject('first')
    expect(projectGuideState.value.appOpened).toBe(true)
    selectProjectGuideProject('second')
    expect(projectGuideState.value.appOpened).toBe(false)
  })
})
