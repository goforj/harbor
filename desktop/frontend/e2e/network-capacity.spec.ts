import { expect, test } from '@playwright/test'

test('expands legacy networking from settings and refreshes confirmed capacity', async ({ page }) => {
  await page.goto('/#/settings/daemon')
  await expect(page.getByText('8 network identities', { exact: true })).toBeVisible()
  await page.getByRole('button', { name: 'Expand network capacity', exact: true }).click()
  await expect(page.getByText('256 network identities', { exact: true })).toBeVisible()
  await expect(page.getByRole('button', { name: 'Expand network capacity', exact: true })).toHaveCount(0)
  await page.screenshot({ path: '/tmp/harbor-capacity-settings.png', fullPage: true })
})
