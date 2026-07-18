import { expect, test } from '@playwright/test'

test('navigates from the Harbor overview to the project list', async ({ page }) => {
  await page.goto('/#/overview')

  await expect(page).toHaveTitle('Overview · GoForj Harbor')
  await expect(page.getByRole('button', { name: 'Overview', exact: true })).toBeVisible()
  await expect(page.getByRole('button', { name: 'Projects', exact: true })).toBeVisible()
  await expect(page.getByRole('button', { name: 'Services', exact: true })).toBeVisible()
  await expect(page.getByRole('button', { name: 'System', exact: true })).toBeVisible()

  await page.getByRole('button', { name: 'Projects', exact: true }).click()

  await expect(page).toHaveURL(/#\/projects(?:\/|$)/)
  await expect(page.getByText('orders-api', { exact: true }).first()).toBeVisible()
  await expect(page.getByText('billing', { exact: true }).first()).toBeVisible()
})

test('uses a single detail surface and a back path at narrow widths', async ({ page }) => {
  await page.setViewportSize({ width: 430, height: 760 })
  await page.goto('/#/projects/orders-api')

  await expect(page.locator('.harbor-rail-slot')).toBeHidden()
  await expect(page.locator('.harbor-context-slot')).toBeHidden()
  await expect(page.locator('.harbor-detail-slot')).toBeVisible()
  await expect(page.locator('.harbor-mobile-slot')).toBeVisible()
  await expect(page.getByRole('heading', { name: 'orders-api' })).toBeVisible()

  await page.getByRole('link', { name: 'Back to projects' }).click()

  await expect(page).toHaveURL(/#\/projects$/)
  await expect(page.locator('.harbor-context-slot')).toBeVisible()
  await expect(page.getByText('Running · 2', { exact: true })).toBeVisible()
})

test('moves from two panes to three panes at the desktop breakpoint', async ({ page }) => {
  await page.setViewportSize({ width: 900, height: 700 })
  await page.goto('/#/projects/orders-api')

  await expect(page.locator('.harbor-rail-slot')).toBeVisible()
  await expect(page.locator('.harbor-context-slot')).toBeHidden()
  await expect(page.locator('.harbor-detail-slot')).toBeVisible()
  await expect(page.getByRole('link', { name: 'Back to projects' })).toBeVisible()

  await page.setViewportSize({ width: 1100, height: 700 })

  await expect(page.locator('.harbor-context-slot')).toBeVisible()
  await expect(page.locator('.harbor-detail-slot')).toBeVisible()
  await expect(page.getByRole('link', { name: 'Back to projects' })).toBeHidden()
})

test('searches command metadata that is not rendered in item labels', async ({ page }) => {
  await page.goto('/#/overview')
  await page.keyboard.press('Control+k')

  const dialog = page.getByRole('dialog', { name: 'Command Menu' })
  await expect(dialog).toBeVisible()
  await dialog.getByPlaceholder('Search projects, services, and Harbor…').fill('/workspace/apps/billing')
  await expect(dialog.getByText('billing', { exact: true })).toBeVisible()
})

test('keeps search and appearance reachable from mobile navigation', async ({ page }) => {
  await page.setViewportSize({ width: 430, height: 760 })
  await page.goto('/#/overview')
  await page.getByRole('button', { name: 'More Harbor actions' }).click()

  await expect(page.getByRole('menuitem', { name: 'System', exact: true })).toBeVisible()
  await expect(page.getByRole('menuitem', { name: 'Dark appearance' })).toBeVisible()
  await page.getByRole('menuitem', { name: 'Search Harbor' }).click()

  await expect(page.getByRole('dialog', { name: 'Command Menu' })).toBeVisible()
})
