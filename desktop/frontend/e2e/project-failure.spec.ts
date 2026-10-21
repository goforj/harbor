import { expect, test } from '@playwright/test'

test('failure details stay useful across themes, disclose safe output, and retry through Start', async ({ page }, testInfo) => {
  await page.goto('/#/projects/orders-api', { waitUntil: 'domcontentloaded' })
  await expect(page.getByRole('heading', { name: 'Orders API', exact: true })).toBeVisible()
  await page.evaluate(async () => {
    const { useHarborStore } = await import('/src/stores/harbor.ts')
    const { harborBridge } = await import('/src/bridge/index.ts')
    const { harborWireFixture } = await import('/src/bridge/harbor.fixture.ts')
    const store = useHarborStore()
    const activity = structuredClone(harborWireFixture.project_activity)
    const session = activity.session
    const text = 'Build frontend\nsrc/main.ts:7: error TS2304: Cannot find name "missingConfig".\nDB_PASSWORD="private-example-value"\nDATABASE_URL=postgres://dev:private-url-password@localhost/app\n'
    harborBridge.getProjectActivity = async () => ({
      project_id: 'orders-api',
      session: {
        ...session,
        state: 'failed',
        output: { available: false, historical: true, reset: true, truncated: true, has_more: false, next_cursor: text.length, text },
        startup: { events: [
          { schema_version: 1, session: session.id, sequence: 1, phase: 'frontend.build', label: 'Build frontend', status: 'failed', elapsed_ms: 1400 },
          { schema_version: 1, session: session.id, sequence: 2, phase: 'startup', label: 'Startup', status: 'failed', elapsed_ms: 1401 },
        ] },
      },
    })
    store.projectById('orders-api')!.state = 'failed'
    store.projectLifecycleErrors['orders-api'] = "project runtime exited before readiness; inspect Harbor's retained launch trace at _data/harbor/forj-dev.log"
    store.projectLifecycleProblemCodes['orders-api'] = 'project.process.exited'
    store.snapshot!.sequence += 1
    harborBridge.startProject = async (projectId, intentId) => {
      ;(window as Window & { retriedProject?: string }).retriedProject = projectId
      return { ...harborWireFixture.start_project, operation: { ...harborWireFixture.start_project.operation, project_id: projectId, intent_id: intentId } }
    }
  })
  const panel = page.getByTestId('project-failure-panel')
  await expect(panel.getByRole('heading')).toHaveText('Project startup failed')
  await expect(panel).toContainText('Failed step: Build frontend')
  await panel.getByRole('button', { name: 'Show details', exact: true }).click()
  await expect(panel).toContainText('Retained launch output')
  await expect(panel).toContainText('Earlier output omitted')
  await expect(panel.locator('pre')).toContainText('missingConfig')
  await expect(panel).not.toContainText('private-example-value')
  await expect(panel).not.toContainText('private-url-password')
  await page.evaluate(async () => {
    const { setThemePreference } = await import('/src/lib/theme.ts')
    setThemePreference('dark')
  })
  await expect.poll(() => panel.getByRole('button', { name: 'Copy failure details' }).evaluate(element => getComputedStyle(element).color)).toBe(await panel.evaluate(element => getComputedStyle(element).color))
  await page.screenshot({ path: testInfo.outputPath('project-failure-dark.png'), animations: 'disabled' })
  await page.evaluate(async () => {
    const { setThemePreference } = await import('/src/lib/theme.ts')
    setThemePreference('light')
  })
  await page.screenshot({ path: testInfo.outputPath('project-failure-light.png'), animations: 'disabled' })
  await page.setViewportSize({ width: 430, height: 800 })
  expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBe(true)
  await panel.getByRole('button', { name: 'View development output', exact: true }).click()
  await expect(page.getByRole('tab', { name: 'Development output', exact: true })).toHaveAttribute('data-state', 'active')
  await panel.getByRole('button', { name: 'Retry start', exact: true }).click()
  await expect.poll(() => page.evaluate(() => (window as Window & { retriedProject?: string }).retriedProject)).toBe('orders-api')
})
