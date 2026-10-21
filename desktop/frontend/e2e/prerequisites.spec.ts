import { expect, test } from '@playwright/test'

test('creation recovers a missing CLI and distinguishes runtime warnings from file creation', async ({ page }, testInfo) => {
  await page.goto('/#/overview')
  await expect(page.getByRole('button', { name: 'New project', exact: true })).toBeVisible()
  await page.evaluate(async () => {
    const { harborBridge } = await import('/src/bridge/index.ts')
    const state = { cliMissing: true, starts: 0 }
    ;(window as Window & { readinessDemo?: typeof state }).readinessDemo = state
    harborBridge.getProjectCreationPrerequisites = async request => {
      const checks = [
        { id: 'forj', name: 'GoForj CLI', status: state.cliMissing ? 'missing' as const : 'ready' as const, detail: state.cliMissing ? "GoForj CLI was not found in Harbor's PATH." : 'Graphical creation contract is available.', action: 'Install the GoForj CLI, then recheck.', required_for: 'creation' as const },
        { id: 'go', name: 'Go', status: 'ready' as const, detail: 'Go is available.', required_for: 'creation' as const },
      ]
      if (request.options['starter-kit'] === 'vue') checks.push({ id: 'node', name: 'Node.js', status: 'incompatible' as never, detail: 'This frontend needs a newer Node.js version.', action: 'Install a supported Node.js LTS release.', required_for: 'start' as never })
      if (request.options.components?.split(',').includes('docker')) checks.push({ id: 'docker-engine', name: 'Docker engine', status: 'unavailable' as never, detail: 'Start Docker, then recheck.', action: 'Start your Docker engine.', required_for: 'start' as never })
      return { provider_id: request.provider_id, checks }
    }
  })
  await page.getByRole('button', { name: 'New project', exact: true }).click()
  const dialog = page.getByRole('dialog', { name: 'New project', exact: true })
  await expect(dialog).toContainText('Install the GoForj CLI, then recheck.')
  await expect(dialog.getByLabel('Project name', { exact: true })).toHaveCount(0)
  await page.evaluate(() => { (window as Window & { readinessDemo: { cliMissing: boolean } }).readinessDemo.cliMissing = false })
  await dialog.getByRole('button', { name: 'Recheck', exact: true }).click()
  await dialog.getByLabel('Project name', { exact: true }).fill('Readiness demo')
  await dialog.getByLabel('Go module', { exact: true }).fill('example.com/readiness-demo')
  await dialog.getByLabel('Project directory', { exact: true }).fill('/workspace/apps/readiness-demo')
  await dialog.getByRole('button', { name: 'Continue', exact: true }).click()
  await dialog.getByLabel('Starter kit', { exact: true }).selectOption('vue')
  await dialog.getByRole('button', { name: 'Continue', exact: true }).click()
  await dialog.getByRole('button', { name: 'Continue', exact: true }).click()
  await expect(dialog).toContainText('Ready to create; runtime tools need attention')
  await expect(dialog).toContainText('Needed when you start the project.')
  await expect(dialog.getByRole('button', { name: 'Create project', exact: true })).toBeEnabled()
  await expect.poll(() => dialog.getByTestId('creation-step').evaluate(element => element.scrollTop)).toBe(0)
  await page.evaluate(async () => {
    const { setThemePreference } = await import('/src/lib/theme.ts')
    setThemePreference('dark')
  })
  await page.screenshot({ path: testInfo.outputPath('creation-prerequisites-dark.png'), animations: 'disabled' })
  await page.evaluate(async () => {
    const { setThemePreference } = await import('/src/lib/theme.ts')
    setThemePreference('light')
  })
  await page.screenshot({ path: testInfo.outputPath('creation-prerequisites-light.png'), animations: 'disabled' })
  await dialog.getByRole('button', { name: 'Create project', exact: true }).click()
  await expect(dialog).toBeHidden()
  await expect(page.getByRole('heading', { name: 'Readiness demo', exact: true })).toBeVisible()
})

test('Start waits for tool repair and rechecks without disturbing another project', async ({ page }, testInfo) => {
  await page.goto('/#/projects/reports')
  await expect(page.getByRole('heading', { name: 'Reports', exact: true })).toBeVisible()
  await page.evaluate(async () => {
    const { harborBridge } = await import('/src/bridge/index.ts')
    const original = harborBridge.startProject
    const state = { repaired: false, starts: 0 }
    ;(window as Window & { startReadinessDemo?: typeof state }).startReadinessDemo = state
    harborBridge.getProjectPrerequisites = async projectId => ({ provider_id: 'goforj', project_id: projectId, checks: [
      { id: 'docker-engine', name: 'Docker engine', status: state.repaired ? 'ready' : 'unavailable', detail: state.repaired ? 'Docker engine is available.' : 'Docker is installed, but Harbor could not reach the selected engine.', action: 'Start Docker and select its intended context, then recheck.' },
    ] })
    harborBridge.startProject = async (projectId, intentId) => { state.starts++; return original(projectId, intentId) }
  })
  await page.getByRole('button', { name: 'Start project', exact: true }).click()
  const panel = page.getByRole('region', { name: 'Tool prerequisites', exact: true })
  await expect(panel).toContainText('Harbor could not reach the selected engine.')
  expect(await page.evaluate(() => (window as Window & { startReadinessDemo: { starts: number } }).startReadinessDemo.starts)).toBe(0)
  expect(await page.evaluate(async () => (await import('/src/stores/harbor.ts')).useHarborStore().projectById('orders-api')?.state)).toBe('ready')
  await page.screenshot({ path: testInfo.outputPath('start-prerequisites.png'), animations: 'disabled' })
  await page.evaluate(() => { (window as Window & { startReadinessDemo: { repaired: boolean } }).startReadinessDemo.repaired = true })
  await panel.getByRole('button', { name: 'Recheck', exact: true }).click()
  await expect(panel).toContainText('Required tools are ready')
  await page.getByRole('button', { name: 'Start project', exact: true }).click()
  await expect.poll(() => page.evaluate(() => (window as Window & { startReadinessDemo: { starts: number } }).startReadinessDemo.starts)).toBe(1)
  expect(await page.evaluate(async () => (await import('/src/stores/harbor.ts')).useHarborStore().projectById('orders-api')?.state)).toBe('ready')
  await page.setViewportSize({ width: 430, height: 800 })
  expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBe(true)
})

test('a delayed readiness response cannot enable creation after the wizard closes and reopens', async ({ page }) => {
  await page.goto('/#/overview')
  await expect(page.getByRole('button', { name: 'New project', exact: true })).toBeVisible()
  await page.evaluate(async () => {
    const { harborBridge } = await import('/src/bridge/index.ts')
    let first = true
    harborBridge.getProjectCreationPrerequisites = async request => {
      const report = { provider_id: request.provider_id, checks: [{ id: 'forj', name: 'GoForj CLI', status: 'missing' as const, detail: 'Install GoForj, then recheck.', required_for: 'creation' as const }] }
      if (first) {
        first = false
        return new Promise(resolve => {
          ;(window as Window & { finishOldReadiness?: () => void }).finishOldReadiness = () => resolve({ ...report, checks: report.checks.map(check => ({ ...check, status: 'ready' as never })) })
        })
      }
      return report
    }
  })
  await page.getByRole('button', { name: 'New project', exact: true }).click()
  const dialog = page.getByRole('dialog', { name: 'New project', exact: true })
  await expect(dialog).toContainText('Checking local tools')
  await dialog.getByRole('button', { name: 'Cancel', exact: true }).click()
  await page.getByRole('button', { name: 'New project', exact: true }).click()
  await expect(dialog).toContainText('Install GoForj, then recheck.')
  await page.evaluate(() => (window as Window & { finishOldReadiness?: () => void }).finishOldReadiness?.())
  await expect(dialog.getByLabel('Project name', { exact: true })).toHaveCount(0)
  await expect(dialog).toContainText('Install GoForj, then recheck.')
})
