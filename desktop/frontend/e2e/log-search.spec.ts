import { expect, test } from '@playwright/test'

test('project and service logs share scoped search, navigation, and pause/resume controls', async ({ page }, testInfo) => {
  await page.goto('/#/projects/orders-api', { waitUntil: 'domcontentloaded' })
  await expect(page.getByRole('heading', { name: 'Orders API', exact: true })).toBeVisible()
  await page.evaluate(async () => {
    const { harborBridge } = await import('/src/bridge/index.ts')
    const { useHarborStore } = await import('/src/stores/harbor.ts')
    const { harborWireFixture } = await import('/src/bridge/harbor.fixture.ts')
    const store = useHarborStore()
    let text = Array.from({ length: 160 }, (_, index) => `\u001b[36mHTTP\u001b[0m GET /tasks/${index} ${index % 50 === 0 ? 'ERROR retry' : '\u001b[32m200\u001b[0m'}\n`).join('')
    const activity = (sessionId: string, cursor: number) => ({
      ...structuredClone(harborWireFixture.project_activity),
      project_id: 'orders-api',
      session: {
        ...structuredClone(harborWireFixture.project_activity.session),
        id: 'search-demo-session',
        output: { available: true, reset: sessionId !== 'search-demo-session', truncated: false, has_more: false, next_cursor: text.length, text: text.slice(sessionId === 'search-demo-session' ? cursor : 0) },
      },
    })
    harborBridge.getProjectActivity = async (_project, session, cursor) => activity(session, cursor)
    harborBridge.waitProjectActivity = async (_project, session, cursor) => new Promise(resolve => {
      ;(window as Window & { appendLogOutput?: () => void }).appendLogOutput = () => {
        text += 'ERROR appended while paused\n'
        resolve(activity(session, cursor))
      }
    })
    store.snapshot!.sequence += 1
  })
  await page.getByRole('tab', { name: 'Development output', exact: true }).click()
  const viewer = page.getByRole('region', { name: 'Current project development output viewer', exact: true })
  const viewport = viewer.getByLabel('Current project development output', { exact: true })
  await expect(viewport).toContainText('/tasks/159')
  await viewport.focus()
  await page.keyboard.press('Control+f')
  const search = viewer.getByRole('textbox', { name: 'Find in logs' })
  await expect(search).toBeFocused()
  await search.fill('error')
  await expect(viewer.getByRole('status')).toHaveText('1 of 4')
  await search.press('Enter')
  await expect(viewer.getByRole('status')).toHaveText('2 of 4')
  const scrollTop = await viewport.evaluate(element => element.scrollTop)
  await expect.poll(() => page.evaluate(() => typeof (window as Window & { appendLogOutput?: () => void }).appendLogOutput)).toBe('function')
  await page.evaluate(() => (window as Window & { appendLogOutput?: () => void }).appendLogOutput?.())
  await expect(viewer.getByRole('status')).toHaveText('2 of 5')
  expect(await viewport.evaluate(element => element.scrollTop)).toBe(scrollTop)
  await expect(viewer.getByRole('button', { name: 'Resume following' })).toBeVisible()
  await page.evaluate(async () => {
    const { setThemePreference } = await import('/src/lib/theme.ts')
    setThemePreference('dark')
  })
  await page.screenshot({ path: testInfo.outputPath('log-search-dark.png'), animations: 'disabled' })
  await page.evaluate(async () => {
    const { setThemePreference } = await import('/src/lib/theme.ts')
    setThemePreference('light')
  })
  await page.screenshot({ path: testInfo.outputPath('log-search-light.png'), animations: 'disabled' })
  await page.evaluate(async () => {
    const { setTerminalAppearance } = await import('/src/lib/terminalAppearance.ts')
    setTerminalAppearance('dark')
  })
  await expect.poll(() => search.evaluate(element => getComputedStyle(element).color)).toBe('rgb(226, 226, 226)')
  await expect.poll(() => viewer.getByRole('button', { name: 'Resume following' }).evaluate(element => getComputedStyle(element).color)).toBe('rgb(226, 226, 226)')
  await page.evaluate(async () => {
    const { setTerminalAppearance } = await import('/src/lib/terminalAppearance.ts')
    setTerminalAppearance('app')
  })
  await search.fill('not present')
  await expect(viewer.getByRole('status')).toHaveText('No matches')
  await expect(viewer.getByRole('button', { name: 'Next match' })).toBeDisabled()
  await search.press('Escape')
  await expect(search).toBeHidden()
  await expect(viewport).toBeFocused()
  await viewer.getByRole('button', { name: 'Jump to latest' }).click()
  await expect.poll(() => viewport.evaluate(element => element.scrollHeight - element.scrollTop - element.clientHeight)).toBeLessThan(2)

  await page.getByRole('tab', { name: /^Services\s+2$/ }).click()
  const serviceViewer = page.getByRole('region', { name: 'MySQL service logs viewer', exact: true })
  await expect(serviceViewer).toContainText('MySQL ready on orders-api')
  await serviceViewer.getByRole('button', { name: 'Search logs', exact: true }).click()
  await serviceViewer.getByRole('textbox', { name: 'Find in logs' }).fill('ready')
  await expect(serviceViewer.getByRole('status')).toHaveText('1 of 1')
  await serviceViewer.getByRole('button', { name: 'Clear', exact: true }).click()
  await expect(serviceViewer.getByRole('status')).toHaveText('No matches')
  await page.getByRole('tab', { name: 'Redis', exact: true }).click()
  const redisViewer = page.getByRole('region', { name: 'Redis service logs viewer', exact: true })
  await expect(redisViewer).toContainText('Redis ready on orders-api')
  await expect(redisViewer.getByRole('textbox', { name: 'Find in logs' })).toHaveCount(0)
  await page.setViewportSize({ width: 430, height: 800 })
  expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBe(true)
})
