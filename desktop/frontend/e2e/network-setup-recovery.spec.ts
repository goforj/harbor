import { expect, test } from '@playwright/test'

test('resumes the same verified operation after an interrupted response', async ({ page }) => {
  await page.goto('/#/settings/daemon')
  await expect(page.getByText('8 network identities', { exact: true })).toBeVisible()
  await page.evaluate(async () => {
    const bridgePath = '/src/bridge/index.ts'
    const { harborBridge } = await import(/* @vite-ignore */ bridgePath)
    const snapshot = await harborBridge.getSnapshot()
    const status = await harborBridge.getStatus()
    snapshot.sequence += 1
    snapshot.network_setup = {
      configured: true,
      pending: { operation_id: 'operation-resume-setup', intent_id: 'intent-resume-setup', operation_revision: snapshot.sequence, kind: 'expansion', phase: 'confirmation' },
    }
    const operation = {
      id: 'operation-resume-setup', intent_id: 'intent-resume-setup', kind: 'network.setup', state: 'requires_approval', phase: 'awaiting_approval',
      requested_at: '2026-10-19T12:00:00Z', started_at: '2026-10-19T12:00:01Z',
    }
    snapshot.operations.push(operation)
    status.capabilities.push('control.network-setup.v1', 'control.network-setup-recovery.v1')
    harborBridge.getSnapshot = async () => structuredClone(snapshot)
    harborBridge.getStatus = async () => ({ ...structuredClone(status), sequence: snapshot.sequence })
    harborBridge.setupNetwork = harborBridge.repairNetwork = harborBridge.expandNetwork = async () => {
      throw new Error('Recovery must keep its existing operation')
    }
    let calls = 0
    harborBridge.resumeNetworkSetup = async () => {
      calls += 1
      if (calls === 1) throw new Error('Connection interrupted. Verified changes are retained.')
      operation.state = 'succeeded'
      operation.phase = 'completed'
      Object.assign(operation, { finished_at: '2026-10-19T12:00:02Z' })
      snapshot.network_setup = { configured: true }
      snapshot.network_capacity = 256
      snapshot.sequence += 3
      return { operation: structuredClone(operation), revision: snapshot.sequence }
    }
  })
  await page.getByRole('button', { name: 'Refresh', exact: true }).click()
  const card = page.getByLabel('Pending network setup', { exact: true })
  await expect(card).toContainText('Finish network upgrade')
  await expect(card).toContainText('network changes were verified')
  await expect(page.getByRole('button', { name: 'Expand network capacity', exact: true })).toHaveCount(0)
  await page.screenshot({ path: '/tmp/harbor-setup-recovery-light.png', fullPage: true })
  await page.evaluate(() => document.documentElement.classList.add('dark'))
  await page.screenshot({ path: '/tmp/harbor-setup-recovery-dark.png', fullPage: true })
  await page.setViewportSize({ width: 430, height: 850 })
  await expect(card.getByRole('button', { name: 'Resume setup', exact: true })).toBeVisible()
  expect(await card.evaluate(element => element.scrollWidth <= element.clientWidth)).toBe(true)
  await page.screenshot({ path: '/tmp/harbor-setup-recovery-mobile.png', fullPage: true })
  await card.getByRole('button', { name: 'Resume setup', exact: true }).click()
  await expect(card.getByRole('alert')).toContainText('Connection interrupted')
  await expect(card.getByRole('button', { name: 'Resume setup', exact: true })).toBeEnabled()
  await card.getByRole('button', { name: 'Resume setup', exact: true }).click()
  await expect(card).toHaveCount(0)
  await expect(page.getByText('256 network identities', { exact: true })).toBeVisible()
})
