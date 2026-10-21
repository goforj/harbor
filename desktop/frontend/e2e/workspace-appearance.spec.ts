import { expect, test } from '@playwright/test'

for (const theme of ['light', 'dark'] as const) {
  test(`operational panels remain usable in ${theme} at narrow widths and larger text`, async ({ page }, testInfo) => {
    await page.goto('/#/projects/orders-api', { waitUntil: 'domcontentloaded' })
    await expect(page.getByRole('heading', { name: 'Orders API', exact: true })).toBeVisible()
    await page.evaluate(async (selected) => {
      const { setThemePreference } = await import('/src/lib/theme.ts')
      setThemePreference(selected)
    }, theme)
    for (const tab of ['Environment', 'Connect', 'Services', 'Resources']) {
      await page.getByRole('tab', { name: new RegExp(`^${tab}(?:\\s|$)`) }).click()
      await page.setViewportSize({ width: 430, height: 820 })
      expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBe(true)
      await expect(page.locator('.harbor-illustration')).toHaveCount(0)
      await page.setViewportSize({ width: 1280, height: 850 })
    }
    await page.goto('/#/services/orders-api/mysql')
    await expect(page.getByRole('heading', { name: 'MySQL', exact: true })).toBeVisible()
    await page.evaluate(async () => {
      const { useHarborStore } = await import('/src/stores/harbor.ts')
      const project = useHarborStore().projectById('orders-api')!
      project.name = 'An unusually long project name that must remain available to every user'
      project.services.find(service => service.id === 'mysql')!.name = 'Long service identity with useful operational details'
      document.documentElement.style.fontSize = '200%'
    })
    await page.setViewportSize({ width: 640, height: 900 })
    await expect(page.getByRole('heading', { name: 'Long service identity with useful operational details' })).toBeVisible()
    expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBe(true)
    await expect(page.getByLabel('MySQL service logs', { exact: true })).toHaveCount(0)
    await page.screenshot({ path: testInfo.outputPath(`service-large-text-${theme}.png`) })
    await page.evaluate(() => { document.documentElement.style.fontSize = '' })
    await page.goto('/#/settings/appearance')
    await expect(page.getByRole('heading', { name: 'Appearance', exact: true })).toBeVisible()
    await page.setViewportSize({ width: 430, height: 820 })
    expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBe(true)
    await expect(page.getByRole('radio', { name: 'System app appearance', exact: true })).toBeVisible()
    await page.screenshot({ path: testInfo.outputPath(`appearance-narrow-${theme}.png`) })
  })
}

test('forced colors preserve keyboard focus, selected appearances, and readable output', async ({ page }) => {
  await page.emulateMedia({ forcedColors: 'active', colorScheme: 'dark' })
  await page.goto('/#/settings/appearance', { waitUntil: 'domcontentloaded' })
  const system = page.getByRole('radio', { name: 'System app appearance', exact: true })
  await expect(system).toBeChecked()
  await system.focus()
  await page.keyboard.press('ArrowRight')
  const dark = page.getByRole('radio', { name: 'Dark app appearance', exact: true })
  await expect(dark).toBeFocused()
  await expect(dark).toBeChecked()
  expect(await dark.evaluate(element => getComputedStyle(element.nextElementSibling!).outlineStyle)).toBe('solid')
  const preview = page.getByLabel('Terminal appearance preview', { exact: true })
  await expect(preview).toContainText('Build complete')
  const colors = await preview.locator('[role="log"]').evaluate(element => ({ text: getComputedStyle(element).color, surface: getComputedStyle(element.parentElement!).backgroundColor }))
  expect(colors.text).not.toBe(colors.surface)
  await page.goto('/#/overview')
  await expect(page.locator('.harbor-illustration')).toBeHidden()
  await page.getByRole('button', { name: 'Open command menu', exact: true }).click()
  const dialog = page.getByRole('dialog', { name: 'Command Menu', exact: true })
  const search = dialog.getByPlaceholder('Search projects, services, and Harbor…')
  await expect(search).toBeFocused()
  await page.keyboard.press('Escape')
  await expect(dialog).toBeHidden()
  await expect(page.getByRole('button', { name: 'Open command menu', exact: true })).toBeFocused()
})

test('Connect copies the daemon URL and drops stale connections on reconnect', async ({ page }) => {
  await page.goto('/#/projects/orders-api', { waitUntil: 'domcontentloaded' })
  await expect(page.getByRole('heading', { name: 'Orders API', exact: true })).toBeVisible()
  await page.evaluate(async () => {
    const { useHarborStore } = await import('/src/stores/harbor.ts')
    const { harborBridge } = await import('/src/bridge/index.ts')
    const project = useHarborStore().projectById('orders-api')!
    project.resources.push({ id: 'native-admin', name: 'Native admin', kind: 'operator', owner: { kind: 'service', service_id: 'mysql' }, url: 'http://127.77.0.10:8089/admin?view=all#status' })
    harborBridge.getServiceLogs = async (project_id, _session, service_id) => ({ project_id, service_id, supported: true, available: true, ports: [], output: { available: true, text: '', next_cursor: 0, reset: false, truncated: false, has_more: false } })
    Object.defineProperty(navigator, 'clipboard', { configurable: true, value: {
      writeText: async (value: string) => { (window as Window & { copiedConnection?: string }).copiedConnection = value },
    } })
  })
  await page.getByRole('tab', { name: 'Connect', exact: true }).click()
  const copy = page.getByRole('button', { name: 'Copy http://127.77.0.10:8089/admin?view=all#status address', exact: true })
  await expect(copy).toBeVisible()
  await copy.click()
  expect(await page.evaluate(() => (window as Window & { copiedConnection?: string }).copiedConnection)).toBe('http://127.77.0.10:8089/admin?view=all#status')
  await page.evaluate(async () => { const { useHarborStore } = await import('/src/stores/harbor.ts'); useHarborStore().snapshotStale = true })
  await expect(page.getByText('Waiting for a fresh Harbor connection before showing service addresses.')).toBeVisible()
  await expect(copy).toHaveCount(0)
})
