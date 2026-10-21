import { expect, test } from '@playwright/test'

test('an empty workspace guides creation, approved networking, Start, and opening the published App', async ({ page }, testInfo) => {
  await page.goto('/#/overview')
  await expect(page.locator('#overview-title')).toBeVisible()
  await page.evaluate(async () => {
    const { harborBridge } = await import('/src/bridge/index.ts')
    const { useHarborStore } = await import('/src/stores/harbor.ts')
    const { projectGuideState } = await import('/src/lib/projectGuide.ts')
    const originalSnapshot = harborBridge.getSnapshot
    const originalStatus = harborBridge.getStatus
    const originalSetup = harborBridge.setupNetwork
    const originalStart = harborBridge.startProject
    const originalPrerequisites = harborBridge.getProjectPrerequisites
    const hidden = new Set((await originalSnapshot()).projects.map(project => project.id))
    const state = { configured: false, toolsReady: false, started: false, opened: '' }
    ;(window as Window & { projectGuideDemo?: typeof state }).projectGuideDemo = state
    harborBridge.getStatus = async () => {
      const status = await originalStatus()
      status.capabilities = [...new Set([...status.capabilities, 'control.network-setup.v1'])]
      return status
    }
    harborBridge.getSnapshot = async () => {
      const snapshot = await originalSnapshot()
      snapshot.projects = snapshot.projects.filter(project => !hidden.has(project.id))
      snapshot.operations = snapshot.operations.filter(operation => !operation.project_id || !hidden.has(operation.project_id))
      snapshot.recent_resource_ids = []
      snapshot.network_setup = { configured: state.configured }
      if (state.started) {
        for (const project of snapshot.projects) {
          project.state = 'ready'
          project.apps = [{ id: 'app', name: 'App', state: 'ready', active: true, required: true }]
          project.resources = [{ id: 'application', name: 'Application', kind: 'application', owner: { kind: 'app', app_id: 'app' }, url: 'https://onboarding-demo.test' }]
        }
        for (const operation of snapshot.operations) {
          if (operation.kind === 'project.start') { operation.state = 'succeeded'; operation.phase = 'completed'; operation.finished_at = new Date().toISOString() }
        }
      }
      return snapshot
    }
    harborBridge.setupNetwork = async () => { state.configured = true; return originalSetup() }
    harborBridge.getProjectPrerequisites = async id => state.toolsReady ? originalPrerequisites(id) : { provider_id: 'goforj', project_id: id, checks: [{ id: 'node', name: 'Node.js', status: 'missing', detail: 'Node.js is not available in this environment.', action: 'Install Node.js, then recheck.' }] }
    harborBridge.startProject = async (id, intent) => { state.started = true; return originalStart(id, intent) }
    harborBridge.openResource = async (id, resource) => {
      const snapshot = await harborBridge.getSnapshot()
      if (!snapshot.projects.some(project => project.id === id && project.resources.some(candidate => candidate.id === resource))) throw new Error('Resource was not published')
      state.opened = resource
    }
    projectGuideState.value = { offered: false, visible: false, projectId: '', appOpened: false }
    const store = useHarborStore()
    store.snapshot = null
    store.snapshotStale = true
    await store.refresh()
  })
  const guide = page.getByRole('region', { name: 'Getting started guide', exact: true })
  await expect(guide).toBeVisible()
  await expect(guide.getByRole('button', { name: 'Start project', exact: true })).toBeDisabled()
  await guide.getByRole('button', { name: 'New project', exact: true }).click()
  const dialog = page.getByRole('dialog', { name: 'New project', exact: true })
  await dialog.getByLabel('Project name', { exact: true }).fill('Onboarding demo')
  await dialog.getByLabel('Go module', { exact: true }).fill('example.com/onboarding-demo')
  await dialog.getByLabel('Project directory', { exact: true }).fill('/workspace/apps/onboarding-demo')
  await dialog.getByRole('button', { name: 'Continue', exact: true }).click()
  await dialog.getByLabel('Starter kit', { exact: true }).selectOption('vue')
  for (let index = 0; index < 2; index++) await dialog.getByRole('button', { name: 'Continue', exact: true }).click()
  await dialog.getByRole('button', { name: 'Create project', exact: true }).click()
  await expect(dialog).toBeHidden()
  await expect(guide).toContainText('Onboarding demo')
  await expect(page).toHaveURL(/#\/overview$/)
  await guide.getByRole('button', { name: 'Set up secure networking', exact: true }).click()
  await expect(guide).toContainText('Configured')
  await guide.getByRole('button', { name: 'Start project', exact: true }).click()
  const prerequisites = guide.getByRole('region', { name: 'Tool prerequisites', exact: true })
  await expect(prerequisites).toContainText('Install Node.js, then recheck.')
  expect(await page.evaluate(() => (window as Window & { projectGuideDemo: { started: boolean } }).projectGuideDemo.started)).toBe(false)
  await page.evaluate(() => { (window as Window & { projectGuideDemo: { toolsReady: boolean } }).projectGuideDemo.toolsReady = true })
  await prerequisites.getByRole('button', { name: 'Recheck', exact: true }).click()
  await expect(prerequisites).toContainText('Required tools are ready')
  await guide.getByRole('button', { name: 'Start project', exact: true }).click()
  await expect(guide).toContainText('The daemon reports this project ready.')
  await guide.getByRole('button', { name: 'Open app', exact: true }).click()
  await expect(guide).toContainText('4 of 4 steps complete')
  expect(await page.evaluate(() => (window as Window & { projectGuideDemo: { opened: string } }).projectGuideDemo.opened)).toBe('application')
  await page.evaluate(async () => (await import('/src/lib/theme.ts')).setThemePreference('dark'))
  await page.screenshot({ path: testInfo.outputPath('project-guide-dark.png'), animations: 'disabled' })
  await page.evaluate(async () => (await import('/src/lib/theme.ts')).setThemePreference('light'))
  await page.screenshot({ path: testInfo.outputPath('project-guide-light.png'), animations: 'disabled' })
  await page.setViewportSize({ width: 430, height: 800 })
  expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBe(true)
  await guide.getByRole('button', { name: 'Finish setup', exact: true }).click()
  await expect(guide).toBeHidden()
  await page.keyboard.press('Control+k')
  const menu = page.getByRole('dialog', { name: 'Command Menu' })
  await menu.getByPlaceholder('Search projects, services, and Harbor…').fill('Getting started')
  await menu.getByRole('option', { name: 'Getting started', exact: true }).click()
  await expect(guide).toBeVisible()
})
