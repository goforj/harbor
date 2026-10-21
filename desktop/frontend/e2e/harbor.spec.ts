import { expect, test } from '@playwright/test'
import { harborWireFixture } from '../src/bridge/harbor.fixture'

test('shows typed startup steps without changing project readiness', async ({ page }) => {
  await page.goto('/#/projects/orders-api', { waitUntil: 'domcontentloaded' })
  await page.getByRole('tab', { name: 'Development output', exact: true }).click()
  const compact = page.getByTestId('startup-timeline')
  await expect(compact.getByRole('button')).toHaveAttribute('aria-expanded', 'false')
  await expect(compact.getByRole('list', { name: 'Development startup steps' })).toHaveCount(0)
  await expect(compact.getByRole('button')).toContainText('3.4 s')
  await page.getByRole('tab', { name: 'Overview', exact: true }).click()
  const timeline = page.getByTestId('startup-timeline')
  await expect(timeline).toBeVisible()
  await timeline.getByRole('button').click()
  await expect(timeline.getByRole('list', { name: 'Development startup steps' })).toBeVisible()
  await expect(timeline.getByText('Run Docker Compose', { exact: true })).toBeVisible()
  await expect(timeline).toContainText('1.2 s')
  await page.setViewportSize({ width: 430, height: 800 })
  expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBe(true)
})

test('navigates from the Harbor overview to the project list', async ({ page }) => {
  await page.goto('/#/overview', { waitUntil: 'domcontentloaded' })

  await expect(page).toHaveTitle('Overview · GoForj Harbor')
  await expect(page.getByRole('button', { name: 'Overview', exact: true })).toBeVisible()
  await expect(page.getByRole('button', { name: 'Projects', exact: true })).toBeVisible()
  await expect(page.getByRole('button', { name: 'Services', exact: true })).toBeVisible()
  await expect(page.getByRole('button', { name: 'Settings', exact: true })).toBeVisible()

  await page.getByRole('button', { name: 'Projects', exact: true }).click()

  await expect(page).toHaveURL(/#\/projects(?:\/|$)/)
  await expect(page.getByText('Orders API', { exact: true }).first()).toBeVisible()
  await expect(page.getByText('Billing', { exact: true }).first()).toBeVisible()
})

test('keeps branded artwork behind the application surface in both themes', async ({ page }) => {
  await page.goto('/#/overview', { waitUntil: 'domcontentloaded' })

  const artwork = page.locator('.harbor-illustration')
  await expect(artwork).toHaveAttribute('aria-hidden', 'true')
  await expect(artwork).toHaveAttribute('data-size', 'wide')
  await expect(artwork).toHaveAttribute('data-fade', 'soft')

  const light = await artwork.evaluate((element) => {
    const styles = getComputedStyle(element)
    return {
      backgroundImage: styles.backgroundImage,
      maskImage: styles.maskImage || styles.webkitMaskImage,
      mixBlendMode: styles.mixBlendMode,
      opacity: Number(styles.opacity),
      pointerEvents: styles.pointerEvents,
      position: styles.position,
      userSelect: styles.userSelect,
      zIndex: styles.zIndex,
    }
  })

  expect(light.backgroundImage).not.toBe('none')
  expect(light.maskImage).not.toBe('none')
  expect(light.mixBlendMode).toBe('multiply')
  expect(light.opacity).toBeGreaterThanOrEqual(0.04)
  expect(light.opacity).toBeLessThanOrEqual(0.1)
  expect(light.pointerEvents).toBe('none')
  expect(light.position).toBe('fixed')
  expect(light.userSelect).toBe('none')
  expect(light.zIndex).toBe('0')
  await expect.poll(() => page.locator('.harbor-detail-slot').evaluate((element) => getComputedStyle(element).zIndex)).toBe('1')

  await page.evaluate(() => document.documentElement.classList.add('dark'))
  await expect.poll(() => artwork.evaluate((element) => getComputedStyle(element).mixBlendMode)).toBe('screen')
  const darkOpacity = await artwork.evaluate((element) => Number(getComputedStyle(element).opacity))
  expect(darkOpacity).toBeGreaterThanOrEqual(0.04)
  expect(darkOpacity).toBeLessThanOrEqual(0.1)

  await page.setViewportSize({ width: 430, height: 800 })
  const mobileBounds = await artwork.boundingBox()
  expect(mobileBounds).not.toBeNull()
  expect(mobileBounds!.width / mobileBounds!.height).toBeCloseTo(1.5, 1)
  expect(mobileBounds!.x + mobileBounds!.width).toBeGreaterThan(430 * 0.8)
  expect(mobileBounds!.y + mobileBounds!.height).toBeGreaterThan(800 * 0.8)
  await expect.poll(() => artwork.evaluate((element) => Number(getComputedStyle(element).opacity))).toBe(0.04)
})

test('adds the selected project and opens its detail immediately', async ({ page }) => {
  await page.goto('/#/overview', { waitUntil: 'domcontentloaded' })

  await page.getByRole('button', { name: 'Add project', exact: true }).click()

  await expect(page).toHaveURL(/#\/projects\/inventory$/)
  await expect(page.getByRole('heading', { name: 'Inventory' })).toBeVisible()
  await expect(page.getByText('Inventory added', { exact: true })).toBeVisible()
  await expect(page.getByText('Stopped; routing is not configured yet.', { exact: true })).toBeVisible()
})

test('offers one repeat-safe network setup action for an empty capable Harbor', async ({ page }) => {
  await page.addInitScript(({ initialSnapshot, initialStatus }) => {
    let snapshot = { ...structuredClone(initialSnapshot), projects: [], operations: [], recent_resource_ids: [] }
    const status = structuredClone(initialStatus)
    status.capabilities = [...status.capabilities, 'control.network-setup.v1']
    const testWindow = window as typeof window & { networkSetupCalls: number }
    testWindow.networkSetupCalls = 0
    window.go = {
      main: {
        App: {
          async AddProject() {
            return { canceled: true }
          },
          async ApproveProjectRemoval() {
            throw new Error('Project removal approval is not exercised in this setup test')
          },
          async ConfirmProjectRuntimeRepair() {
            throw new Error('Runtime repair is not exercised in this setup test')
          },
          async InspectProjectRuntimeRepair() {
            throw new Error('Runtime repair is not exercised in this setup test')
          },
          async OpenResource() {},
          async ResourceIconURL() { return '' },
          async ProjectActivity(projectId) { return { project_id: projectId } },
          async WaitProjectActivity(projectId) { return { project_id: projectId } },
          async RemoveProject() {
            throw new Error('Project removal is not exercised in this setup test')
          },
          async RemoveOldNetworking() {
            throw new Error('Legacy network removal is not exercised in this setup test')
          },
          async SetupNetwork() {
            testWindow.networkSetupCalls += 1
            const revision = snapshot.sequence + 1
            const completedAt = new Date().toISOString()
            const result = {
              operation: {
                id: `operation-${revision}-network-setup`,
                intent_id: 'intent-network-setup',
                kind: 'network.setup',
                state: 'succeeded',
                phase: 'completed',
                requested_at: completedAt,
                started_at: completedAt,
                finished_at: completedAt,
              },
              revision,
            }
            snapshot = { ...snapshot, sequence: revision, network_setup: { configured: true } }
            status.sequence = revision
            return result
          },
          async StartProject() {
            throw new Error('Project start is not exercised in this setup test')
          },
          async RestartProject() {
            throw new Error('Project restart is not exercised in this setup test')
          },
          async StopProject() {
            throw new Error('Project stop is not exercised in this setup test')
          },
          async Status() {
            return structuredClone(status)
          },
          async Snapshot() {
            return structuredClone(snapshot)
          },
        },
      },
    }
    window.runtime = {
      EventsOn: () => () => undefined,
      EventsOff: () => undefined,
    }
  }, { initialSnapshot: harborWireFixture.snapshot, initialStatus: harborWireFixture.status })

  await page.goto('/#/overview', { waitUntil: 'domcontentloaded' })

  const guide = page.getByRole('region', { name: 'Getting started guide', exact: true })
  await expect(guide.getByText('Set up local domains, HTTPS, and ingress with your approval.', { exact: true })).toBeVisible()
  await expect(page.getByRole('button', { name: 'Set up secure networking', exact: true })).toHaveCount(1)
  await page.getByRole('button', { name: 'Set up secure networking', exact: true }).click()

  await expect(page.getByRole('button', { name: 'Set up secure networking', exact: true })).toHaveCount(0)
  await expect(page.getByText('Secure networking is ready', { exact: true })).toBeVisible()
  await expect(page.getByText('Harbor completed trusted local DNS, HTTPS, and ingress.', { exact: true })).toBeVisible()
  await expect.poll(() => page.evaluate(() => (window as typeof window & { networkSetupCalls: number }).networkSetupCalls)).toBe(1)
})

test('starts a stopped project from its selected detail view', async ({ page }) => {
  await page.goto('/#/projects/reports', { waitUntil: 'domcontentloaded' })

  await page.getByRole('button', { name: 'Start project', exact: true }).click()
  await expect(page.getByRole('status', { name: 'Starting…', exact: true })).toBeVisible()
})

test('stops a running project from its selected detail view', async ({ page }) => {
  await page.goto('/#/projects/orders-api', { waitUntil: 'domcontentloaded' })

  await page.getByRole('button', { name: 'Stop project', exact: true }).click()
  await expect(page.getByRole('status', { name: 'Stopping…', exact: true })).toBeVisible()
})

test('shows live output from the current project session', async ({ page }) => {
  await page.goto('/#/projects/orders-api', { waitUntil: 'domcontentloaded' })

  await page.getByRole('tab', { name: 'Development output', exact: true }).click()
  const output = page.getByLabel('Current project development output', { exact: true })
  await expect(output).toContainText('Building app: web')
  await expect(output).toContainText('Built web in 482ms')
  await expect(output).toContainText('migrations complete (0)')
  const grid = await output.locator('.harbor-terminal-output').evaluate(element => {
    const row = element.firstElementChild!
    return { lineHeight: Number.parseFloat(getComputedStyle(element).lineHeight), rowHeight: row.getBoundingClientRect().height }
  })
  expect(grid.lineHeight).toBeGreaterThanOrEqual(15)
  expect(grid.lineHeight).toBeLessThanOrEqual(16)
  expect(grid.rowHeight).toBeCloseTo(grid.lineHeight, 1)

})

test('shows an ambiguous recovered launch without leaving the project spinning', async ({ page }) => {
  await page.addInitScript(({ initialSnapshot, initialStatus }) => {
    const snapshot = structuredClone(initialSnapshot)
    const project = snapshot.projects.find((entry) => entry.id === 'reports')
    if (!project) throw new Error('reports fixture is missing')
    project.state = 'unavailable'
    project.updated_at = '2026-07-19T23:45:00Z'
    snapshot.sequence += 1
    snapshot.operations.push({
      id: 'operation-recovered-ambiguous-launch',
      intent_id: 'intent-recovered-ambiguous-launch',
      kind: 'project.start',
      project_id: 'reports',
      state: 'failed',
      phase: 'recovery required',
      problem: {
        code: 'project.recovery.ambiguous_launch',
        message: 'Harbor restarted before it could record the managed process identity.',
        retryable: false,
      },
      requested_at: '2026-07-19T23:44:58Z',
      started_at: '2026-07-19T23:44:59Z',
      finished_at: '2026-07-19T23:45:00Z',
    })
    const status = structuredClone(initialStatus)
    status.sequence = snapshot.sequence
    window.go = {
      main: {
        App: {
          async AddProject() { return { canceled: true } },
          async ApproveProjectRemoval() { throw new Error('Project removal approval is not exercised in this recovery test') },
          async ConfirmProjectRuntimeRepair() { throw new Error('Runtime repair confirmation requires explicit test setup') },
          async InspectProjectRuntimeRepair() { throw new Error('Runtime repair inspection requires explicit test setup') },
          async OpenResource() {},
          async ResourceIconURL() { return '' },
          async ProjectActivity(projectId) { return { project_id: projectId } },
          async WaitProjectActivity(projectId) { return { project_id: projectId } },
          async RemoveProject() { throw new Error('Quarantined project removal is disabled') },
          async RemoveOldNetworking() { throw new Error('Legacy network removal is not exercised in this recovery test') },
          async SetupNetwork() { throw new Error('Network setup is not exercised in this recovery test') },
          async StartProject() { throw new Error('Quarantined project start is disabled') },
          async RestartProject() { throw new Error('Quarantined project restart is disabled') },
          async StopProject() { throw new Error('Quarantined project stop is disabled') },
          async Status() { return structuredClone(status) },
          async Snapshot() { return structuredClone(snapshot) },
        },
      },
    }
    window.runtime = {
      EventsOn: () => () => undefined,
      EventsOff: () => undefined,
    }
  }, { initialSnapshot: harborWireFixture.snapshot, initialStatus: harborWireFixture.status })

  await page.goto('/#/projects/reports', { waitUntil: 'domcontentloaded' })

  const recoveryAlert = page.getByRole('alert')
  await expect(recoveryAlert.getByText('Ready to start again', { exact: true })).toBeVisible()
  await expect(recoveryAlert.getByText('Starting again will reconcile the previous runtime and launch a fresh process.', { exact: true })).toBeVisible()
  await expect(page.getByRole('button', { name: 'Start project', exact: true })).toBeVisible()
  await expect(recoveryAlert.getByRole('button', { name: 'Inspect stale runtime', exact: true })).toHaveCount(0)
  await expect(page.getByRole('button', { name: 'Open resource', exact: true })).toHaveCount(0)
  await expect(page.getByRole('button', { name: 'Starting…', exact: true })).toHaveCount(0)
  await expect(page.getByRole('heading', { name: 'Current activity' })).toBeVisible()
  await expect(page.getByText('recovery required', { exact: true })).toBeVisible()
})

test('confirms project removal and offers the administrator approval action', async ({ page }) => {
  await page.goto('/#/projects/orders-api', { waitUntil: 'domcontentloaded' })

  const remove = page.getByRole('button', { name: 'Remove project', exact: true })
  await remove.click()

  const dialog = page.getByRole('alertdialog')
  await expect(dialog.getByRole('heading', { name: 'Remove Orders API?' })).toBeVisible()
  await expect(dialog.getByText('The project files at /workspace/apps/orders-api will stay on disk.', { exact: false })).toBeVisible()
  await dialog.getByRole('button', { name: 'Remove project', exact: true }).click()

  await expect(page.getByText('Administrator approval required', { exact: true })).toBeVisible()
  await expect(page.getByText('Harbor paused removal until it can release this project’s local networking.', { exact: false })).toBeVisible()
  await expect(page.getByRole('button', { name: 'Approve and remove', exact: true })).toBeVisible()
  await expect(page.getByRole('heading', { name: 'Orders API' })).toBeVisible()
})

test('refreshes and leaves project detail after an immediate removal', async ({ page }) => {
  await page.goto('/#/projects/reports', { waitUntil: 'domcontentloaded' })

  await page.getByRole('button', { name: 'Remove project', exact: true }).click()
  await page.getByRole('alertdialog').getByRole('button', { name: 'Remove project', exact: true }).click()

  await expect(page).toHaveURL(/#\/projects$/)
  await expect(page.getByText('Reports', { exact: true })).toHaveCount(0)
})

test('leaves project detail when an active removal completes through a snapshot event', async ({ page }) => {
  await page.addInitScript(({ initialSnapshot, initialStatus, initialUnregistration }) => {
    const listeners = new Map<string, (payload: unknown) => void>()
    let snapshot = structuredClone(initialSnapshot)
    const status = structuredClone(initialStatus)

    window.go = {
      main: {
        App: {
          async AddProject() {
            return { canceled: true }
          },
          async ApproveProjectRemoval() {
            throw new Error('Project removal approval is not exercised in this removal test')
          },
          async ConfirmProjectRuntimeRepair() {
            throw new Error('Runtime repair is not exercised in this removal test')
          },
          async InspectProjectRuntimeRepair() {
            throw new Error('Runtime repair is not exercised in this removal test')
          },
          async OpenResource() {},
          async ResourceIconURL() { return '' },
          async ProjectActivity(projectId) { return { project_id: projectId } },
          async WaitProjectActivity(projectId) { return { project_id: projectId } },
          async RemoveProject(projectId, intentId) {
            const result = structuredClone(initialUnregistration)
            result.revision = snapshot.sequence + 1
            result.operation.id = `operation-${result.revision}-${projectId}`
            result.operation.project_id = projectId
            result.operation.intent_id = intentId
            snapshot = {
              ...snapshot,
              sequence: result.revision,
              operations: [...snapshot.operations, result.operation],
            }
            status.sequence = snapshot.sequence

            window.setTimeout(() => {
              snapshot = {
                ...snapshot,
                sequence: snapshot.sequence + 1,
                projects: snapshot.projects.filter((project) => project.id !== projectId),
                operations: snapshot.operations.filter((operation) => operation.project_id !== projectId),
                recent_resource_ids: snapshot.recent_resource_ids.filter((reference) => reference.project_id !== projectId),
              }
              status.sequence = snapshot.sequence
              listeners.get('harbor:snapshot')?.(structuredClone(snapshot))
            }, 50)
            return result
          },
          async RemoveOldNetworking() {
            throw new Error('Legacy network removal is not exercised in this removal test')
          },
          async SetupNetwork() {
            throw new Error('Network setup is not exercised in this removal test')
          },
          async StartProject(projectId, intentId) {
            const result = structuredClone(initialStart)
            result.operation.project_id = projectId
            result.operation.intent_id = intentId
            return result
          },
          async RestartProject() {
            throw new Error('Project restart is not exercised in this removal test')
          },
          async StopProject(projectId, intentId) {
            const result = structuredClone(initialStop)
            result.operation.project_id = projectId
            result.operation.intent_id = intentId
            return result
          },
          async Snapshot() {
            return structuredClone(snapshot)
          },
          async Status() {
            return structuredClone(status)
          },
        },
      },
    }
    window.runtime = {
      EventsOn(eventName, callback) {
        listeners.set(eventName, callback as (payload: unknown) => void)
        return () => listeners.delete(eventName)
      },
      EventsOff(eventName) {
        listeners.delete(eventName)
      },
    }
  }, {
    initialSnapshot: harborWireFixture.snapshot,
    initialStatus: harborWireFixture.status,
    initialStart: harborWireFixture.start_project,
    initialStop: harborWireFixture.stop_project,
    initialUnregistration: harborWireFixture.remove_project,
  })
  await page.goto('/#/projects/reports', { waitUntil: 'domcontentloaded' })

  await page.getByRole('button', { name: 'Remove project', exact: true }).click()
  await page.getByRole('alertdialog').getByRole('button', { name: 'Remove project', exact: true }).click()

  await expect(page).toHaveURL(/#\/projects$/)
  await expect(page.getByText('Reports', { exact: true })).toHaveCount(0)
})

test('disables removal honestly across projects while another request is in flight', async ({ page }) => {
  await page.goto('/#/projects/orders-api', { waitUntil: 'domcontentloaded' })
  await page.evaluate(async () => {
    const bridgeModulePath = '/src/bridge/index.ts'
    const bridgeModule = await import(/* @vite-ignore */ bridgeModulePath) as {
      harborBridge: {
        removeProject(projectId: string, intentId: string): Promise<unknown>
      }
    }
    bridgeModule.harborBridge.removeProject = () => new Promise(() => undefined)
  })

  await page.getByRole('button', { name: 'Remove project', exact: true }).click()
  await page.getByRole('alertdialog').getByRole('button', { name: 'Remove project', exact: true }).click()
  await page.locator('a[href="#/projects/reports"]').click()

  await expect(page).toHaveURL(/#\/projects\/reports$/)
  await expect(page.getByRole('button', { name: 'Another removal is in progress', exact: true })).toBeDisabled()
})

test('uses a single detail surface and a back path at narrow widths', async ({ page }) => {
  await page.setViewportSize({ width: 430, height: 760 })
  await page.goto('/#/projects/orders-api', { waitUntil: 'domcontentloaded' })

  await expect(page.locator('.harbor-rail-slot')).toBeHidden()
  await expect(page.locator('.harbor-context-slot')).toBeHidden()
  await expect(page.locator('.harbor-detail-slot')).toBeVisible()
  await expect(page.locator('.harbor-mobile-slot')).toBeVisible()
  await expect(page.getByRole('heading', { name: 'Orders API' })).toBeVisible()

  await page.getByRole('link', { name: 'Back to projects' }).click()

  await expect(page).toHaveURL(/#\/projects$/)
  await expect(page.locator('.harbor-context-slot')).toBeVisible()
  await expect(page.getByText('Active · 2', { exact: true })).toBeVisible()
})

test('moves from two panes to three panes at the desktop breakpoint', async ({ page }) => {
  await page.setViewportSize({ width: 900, height: 700 })
  await page.goto('/#/projects/orders-api', { waitUntil: 'domcontentloaded' })

  await expect(page.locator('.harbor-rail-slot')).toBeVisible()
  await expect(page.locator('.harbor-context-slot')).toBeHidden()
  await expect(page.locator('.harbor-detail-slot')).toBeVisible()
  await expect(page.getByRole('link', { name: 'Back to projects' })).toBeVisible()

  await page.setViewportSize({ width: 1100, height: 700 })

  await expect(page.locator('.harbor-context-slot')).toBeVisible()
  await expect(page.locator('.harbor-detail-slot')).toBeVisible()
  await expect(page.getByRole('link', { name: 'Back to projects' })).toBeHidden()
})

test('routes services with project and service identities', async ({ page }) => {
  await page.goto('/#/services/orders-api/mysql', { waitUntil: 'domcontentloaded' })

  await expect(page.getByRole('heading', { name: 'MySQL' })).toBeVisible()
  await expect(page.getByText('orders-api', { exact: true }).first()).toBeVisible()
  await expect(page.getByText('Compose service', { exact: true }).first()).toBeVisible()
  await expect(page.getByLabel('MySQL service logs', { exact: true })).toContainText('MySQL ready on orders-api')
  await expect(page.getByRole('button', { name: 'Clear', exact: true })).toBeVisible()
  await expect(page.getByRole('button', { name: 'Pause scrolling', exact: true })).toHaveAttribute('aria-pressed', 'true')
  await expect(page).toHaveURL(/#\/services\/orders-api\/mysql$/)
})

test('shows ready service counts without treating failed services as ready', async ({ page }) => {
  await page.goto('/#/projects/orders-api', { waitUntil: 'domcontentloaded' })

  const readySummary = page.getByRole('region', { name: 'Project summary' })
  await expect(readySummary.getByText('2 ready', { exact: true })).toBeVisible()
  await expect(readySummary.getByText('2 reported', { exact: true })).toBeVisible()
  await page.getByRole('tab', { name: /^Services\s+2$/, exact: true }).click()
  await expect(page.getByRole('tab', { name: 'MySQL', exact: true })).toBeVisible()
  await expect(page.getByRole('tab', { name: 'Redis', exact: true })).toBeVisible()

  await page.goto('/#/projects/billing', { waitUntil: 'domcontentloaded' })

  const failedSummary = page.getByRole('region', { name: 'Project summary' })
  await expect(failedSummary.getByText('0 ready', { exact: true })).toBeVisible()
  await expect(failedSummary.getByText('1 reported', { exact: true })).toBeVisible()
})

test('searches authoritative project metadata that is not rendered in item labels', async ({ page }) => {
  await page.goto('/#/overview', { waitUntil: 'domcontentloaded' })
  await expect(page.getByRole('button', { name: 'Open command menu' })).toBeVisible()
  await page.keyboard.press('Control+k')

  const dialog = page.getByRole('dialog', { name: 'Command Menu' })
  await expect(dialog).toBeVisible()
  await dialog.getByPlaceholder('Search projects, services, and Harbor…').fill('/workspace/apps/billing')
  await expect(dialog.getByText('Billing', { exact: true })).toBeVisible()
})

test('opens and closes the command menu with accessible keyboard focus', async ({ page }) => {
  await page.goto('/#/overview', { waitUntil: 'domcontentloaded' })

  const trigger = page.getByRole('button', { name: 'Open command menu' })
  await trigger.focus()
  await trigger.press('Enter')

  const dialog = page.getByRole('dialog', { name: 'Command Menu' })
  const search = dialog.getByPlaceholder('Search projects, services, and Harbor…')
  await expect(dialog).toBeVisible()
  await expect(search).toBeFocused()

  await search.press('Escape')

  await expect(dialog).toBeHidden()
  await expect(trigger).toBeFocused()
})

test('preserves legacy daemon links separately from appearance settings', async ({ page }) => {
  await page.goto('/#/system', { waitUntil: 'domcontentloaded' })

  await expect(page).toHaveURL(/#\/settings\/daemon$/)
  await expect(page.locator('#system-title')).toHaveText('Daemon')
  await expect(page.getByRole('radiogroup', { name: 'Terminal appearance', exact: true })).toHaveCount(0)
  await expect(page.getByText('Daemon status')).toBeVisible()
  await expect(page.getByText('Snapshot', { exact: true })).toBeVisible()
  await expect(page.getByText('control.v1')).toBeVisible()
  await expect(page.getByText('DNS and ingress')).toHaveCount(0)
  await expect(page.getByText('HTTPS and trust')).toHaveCount(0)
})

test('opens appearance from the settings cog and keeps daemon diagnostics separate', async ({ page }) => {
  await page.goto('/#/overview', { waitUntil: 'domcontentloaded' })
  await page.getByRole('button', { name: 'Settings', exact: true }).click()
  await expect(page).toHaveURL(/#\/settings\/appearance$/)
  await expect(page.getByRole('heading', { name: 'Appearance', exact: true })).toBeVisible()
  await expect(page.getByRole('radio', { name: 'System app appearance', exact: true })).toBeVisible()
  await expect(page.getByRole('radio', { name: 'System terminal appearance', exact: true })).toBeVisible()
  await expect(page.getByText('Daemon status', { exact: true })).toHaveCount(0)
  await expect(page.getByRole('searchbox', { name: 'Search settings' })).toHaveCount(0)
  await page.getByRole('radio', { name: 'Light app appearance', exact: true }).check()
  await expect(page.locator('html')).not.toHaveClass(/dark/)
  await page.reload({ waitUntil: 'domcontentloaded' })
  await expect(page.getByRole('radio', { name: 'Light app appearance', exact: true })).toBeChecked()
  await page.getByRole('link', { name: 'Daemon Connection and diagnostics' }).click()
  await expect(page.getByText('Daemon status', { exact: true })).toBeVisible()
  await page.getByRole('link', { name: 'Appearance App and terminal themes' }).click()
  await expect(page.getByRole('radio', { name: 'Light app appearance', exact: true })).toBeChecked()
  await page.setViewportSize({ width: 430, height: 800 })
  await expect(page.getByRole('heading', { name: 'Appearance', exact: true })).toBeVisible()
  expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBe(true)
  await page.getByRole('button', { name: 'More Harbor actions' }).click()
  await page.getByRole('menuitem', { name: 'Settings', exact: true }).click()
  await expect(page).toHaveURL(/#\/settings\/appearance$/)
})

test('persists terminal appearance and follows app appearance only when selected', async ({ page }) => {
  await page.addInitScript(() => {
    if (!localStorage.getItem('theme')) localStorage.setItem('theme', 'dark')
  })
  await page.goto('/#/settings/appearance', { waitUntil: 'domcontentloaded' })
  const workspace = page.locator('.harbor-workspace')
  const preview = page.getByLabel('Terminal appearance preview')
  await expect(page.getByRole('radio', { name: 'System terminal appearance', exact: true })).toBeChecked()
  await expect(workspace).toHaveAttribute('data-terminal-mode', 'dark')
  const retainedText = await preview.textContent()

  await page.getByRole('radio', { name: 'Light terminal appearance', exact: true }).check()
  await expect(workspace).toHaveAttribute('data-terminal-mode', 'light')
  await expect(preview).toHaveCSS('background-color', 'rgb(246, 248, 250)')
  await expect(preview).toHaveCSS('color', 'rgb(31, 35, 40)')
  expect(await preview.textContent()).toBe(retainedText)
  await page.reload({ waitUntil: 'domcontentloaded' })
  await expect(page.getByRole('radio', { name: 'Light terminal appearance', exact: true })).toBeChecked()

  await page.goto('/#/projects/orders-api')
  await page.getByRole('tab', { name: 'Development output', exact: true }).click()
  await expect(page.getByLabel('Current project development output', { exact: true })).toHaveCSS('background-color', 'rgb(246, 248, 250)')

  await page.goto('/#/settings/appearance')
  await page.getByRole('radio', { name: 'Dark terminal appearance', exact: true }).check()
  await page.getByRole('button', { name: 'Change appearance', exact: true }).click()
  await page.getByRole('menuitem', { name: 'Light', exact: true }).click()
  await expect(workspace).toHaveAttribute('data-terminal-mode', 'dark')
  await expect(preview).toHaveCSS('background-color', 'rgb(11, 11, 11)')

  await page.getByRole('radio', { name: 'System terminal appearance', exact: true }).check()
  await expect(workspace).toHaveAttribute('data-terminal-mode', 'light')
  await page.getByRole('button', { name: 'Change appearance', exact: true }).click()
  await page.getByRole('menuitem', { name: 'Dark', exact: true }).click()
  await expect(workspace).toHaveAttribute('data-terminal-mode', 'dark')
  await page.setViewportSize({ width: 430, height: 800 })
  expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBe(true)
})

test('fails closed for a production browser unless its fixture is explicitly requested', async ({ page }) => {
  await page.goto('/#/overview', { waitUntil: 'domcontentloaded' })

  const result = await page.evaluate(async () => {
    delete window.go
    delete window.runtime

    const bridgeModulePath = '/src/bridge/index.ts'
    const bridgeModule = await import(/* @vite-ignore */ bridgeModulePath) as {
      selectHarborBridge(development: boolean, browserFixture: boolean): {
        bridge: { getSnapshot(): Promise<{ sequence: number }> }
        mode: string
      }
    }

    const unavailable = bridgeModule.selectHarborBridge(false, false)
    let unavailableMessage = ''
    try {
      await unavailable.bridge.getSnapshot()
    }
    catch (error) {
      unavailableMessage = error instanceof Error ? error.message : String(error)
    }

    const fixture = bridgeModule.selectHarborBridge(false, true)
    const snapshot = await fixture.bridge.getSnapshot()
    return {
      fixtureMode: fixture.mode,
      fixtureSequence: snapshot.sequence,
      unavailableMessage,
      unavailableMode: unavailable.mode,
    }
  })

  expect(result).toEqual({
    fixtureMode: 'fixture',
    fixtureSequence: 42,
    unavailableMessage: 'Harbor daemon bindings are not available in this desktop build.',
    unavailableMode: 'unavailable',
  })
})

test('uses native bindings and recovers after the first snapshot read fails', async ({ page }) => {
  await page.addInitScript(() => {
    let snapshotAttempts = 0
    window.go = {
      main: {
        App: {
          async AddProject() {
            return { canceled: true }
          },
          async ApproveProjectRemoval() {
            throw new Error('Project removal approval is not exercised in this connection test')
          },
          async ConfirmProjectRuntimeRepair() {
            throw new Error('Runtime repair is not exercised in this connection test')
          },
          async InspectProjectRuntimeRepair() {
            throw new Error('Runtime repair is not exercised in this connection test')
          },
          async OpenResource() {},
          async ResourceIconURL() { return '' },
          async ProjectActivity(projectId) { return { project_id: projectId } },
          async WaitProjectActivity(projectId) { return { project_id: projectId } },
          async RemoveProject() {
            throw new Error('Project removal is not exercised in this connection test')
          },
          async RemoveOldNetworking() {
            throw new Error('Legacy network removal is not exercised in this connection test')
          },
          async SetupNetwork() {
            throw new Error('Network setup is not exercised in this connection test')
          },
          async StartProject() {
            throw new Error('Project start is not exercised in this connection test')
          },
          async RestartProject() {
            throw new Error('Project restart is not exercised in this connection test')
          },
          async StopProject() {
            throw new Error('Project stop is not exercised in this connection test')
          },
          async Status() {
            return {
              state: 'ready',
              build: { version: 'dev', modified: false },
              protocol: { major: 1, minor: 0 },
              capabilities: ['control.v1'],
              snapshot_schema_version: 1,
              sequence: 1,
            }
          },
          async Snapshot() {
            snapshotAttempts += 1
            if (snapshotAttempts === 1) {
              throw new Error('Harbor daemon is starting')
            }

            return {
              schema_version: 1,
              sequence: 1,
              captured_at: '2026-07-18T14:35:20Z',
              projects: [],
              operations: [],
              recent_resource_ids: [],
            }
          },
        },
      },
    }
    window.runtime = {
      EventsOn: () => () => undefined,
      EventsOff: () => undefined,
    }
  })
  await page.goto('/#/overview', { waitUntil: 'domcontentloaded' })

  const detail = page.locator('.harbor-detail-slot')
  await expect(detail.getByText('Connected to Harbor. Waiting for the first snapshot.')).toBeVisible()
  await expect(detail.getByText('Harbor daemon is starting')).toBeVisible()

  await detail.getByRole('button', { name: 'Try again' }).click()

  await expect(detail.getByRole('heading', { name: 'Overview' })).toBeVisible()
  await expect(detail.getByText('Connected to Harbor. Waiting for the first snapshot.')).toBeHidden()
  await expect(detail.getByText('Harbor daemon is starting')).toBeHidden()
  const guide = detail.getByRole('region', { name: 'Getting started guide', exact: true })
  await expect(guide.getByRole('heading', { name: 'Get a project running', exact: true })).toBeVisible()
  await expect(guide.getByRole('button', { name: 'Add existing', exact: true })).toBeEnabled()
})

test('keeps a missing first snapshot in an explicit waiting state and announces stale state once', async ({ page }) => {
  await page.addInitScript(() => {
    const listeners = new Map<string, (payload: unknown) => void>()
    const testWindow = window as typeof window & {
      emitHarborConnection(payload: { state: 'connecting' | 'connected' | 'disconnected' }): void
      emitHarborSnapshot(payload: unknown): void
    }
    testWindow.emitHarborConnection = (payload) => listeners.get('harbor:connection')?.(payload)
    testWindow.emitHarborSnapshot = (payload) => listeners.get('harbor:snapshot')?.(payload)
    window.go = {
      main: {
        App: {
          async AddProject() {
            return { canceled: true }
          },
          async ApproveProjectRemoval() {
            throw new Error('Project removal approval is not exercised in this connection test')
          },
          async ConfirmProjectRuntimeRepair() {
            throw new Error('Runtime repair is not exercised in this connection test')
          },
          async InspectProjectRuntimeRepair() {
            throw new Error('Runtime repair is not exercised in this connection test')
          },
          async OpenResource() {},
          async ResourceIconURL() { return '' },
          async ProjectActivity(projectId) { return { project_id: projectId } },
          async WaitProjectActivity(projectId) { return { project_id: projectId } },
          async RemoveProject() {
            throw new Error('Project removal is not exercised in this connection test')
          },
          async RemoveOldNetworking() {
            throw new Error('Legacy network removal is not exercised in this connection test')
          },
          async SetupNetwork() {
            throw new Error('Network setup is not exercised in this connection test')
          },
          async StartProject() {
            throw new Error('Project start is not exercised in this connection test')
          },
          async RestartProject() {
            throw new Error('Project restart is not exercised in this connection test')
          },
          async StopProject() {
            throw new Error('Project stop is not exercised in this connection test')
          },
          async Status() {
            return {
              state: 'ready',
              build: { version: 'dev', modified: false },
              protocol: { major: 1, minor: 0 },
              capabilities: ['control.v1'],
              snapshot_schema_version: 1,
              sequence: 1,
            }
          },
          async Snapshot() {
            throw new Error('snapshot endpoint unavailable')
          },
        },
      },
    }
    window.runtime = {
      EventsOn(eventName, callback) {
        listeners.set(eventName, callback as (payload: unknown) => void)
        return () => listeners.delete(eventName)
      },
      EventsOff: (eventName) => listeners.delete(eventName),
    }
  })
  await page.goto('/#/overview', { waitUntil: 'domcontentloaded' })

  const detail = page.locator('.harbor-detail-slot')
  await expect(page.getByText('Connected to Harbor. Waiting for the first snapshot.', { exact: true }).first()).toBeVisible()
  await expect(page.getByText('snapshot endpoint unavailable', { exact: true }).first()).toBeVisible()
  await expect(detail.getByRole('heading', { name: 'Overview' })).toHaveCount(0)
  await expect(page.getByText('No overview yet', { exact: true })).toHaveCount(0)

  await page.getByRole('button', { name: 'Settings', exact: true }).click()
  await expect(page.getByRole('radio', { name: 'Light app appearance', exact: true })).toBeVisible()
  await page.getByRole('radio', { name: 'Light app appearance', exact: true }).check()
  await page.goto('/#/overview')

  await page.evaluate(() => {
    const testWindow = window as typeof window & {
      emitHarborConnection(payload: { state: 'connecting' | 'connected' | 'disconnected' }): void
    }
    testWindow.emitHarborConnection({ state: 'connecting' })
  })
  await expect(page.getByText('Connecting to Harbor', { exact: true }).first()).toBeVisible()
  await expect(page.getByText('snapshot endpoint unavailable', { exact: true }).first()).toBeVisible()

  await page.evaluate(() => {
    const testWindow = window as typeof window & {
      emitHarborConnection(payload: { state: 'connecting' | 'connected' | 'disconnected' }): void
    }
    testWindow.emitHarborConnection({ state: 'connected' })
  })
  await expect(page.getByText('Connected to Harbor. Waiting for the first snapshot.', { exact: true }).first()).toBeVisible()
  await expect(detail.getByRole('heading', { name: 'Overview' })).toHaveCount(0)

  await page.evaluate(() => {
    const testWindow = window as typeof window & { emitHarborSnapshot(payload: unknown): void }
    testWindow.emitHarborSnapshot({
      schema_version: 1,
      sequence: 1,
      captured_at: '2026-07-18T14:35:20Z',
      projects: [],
      operations: [],
      recent_resource_ids: [],
    })
  })
  await expect(detail.getByRole('heading', { name: 'Overview' })).toBeVisible()

  await page.evaluate(() => {
    const testWindow = window as typeof window & {
      emitHarborConnection(payload: { state: 'connecting' | 'connected' | 'disconnected' }): void
    }
    testWindow.emitHarborConnection({ state: 'disconnected' })
  })
  await expect(page.getByText('Harbor daemon is disconnected.', { exact: true })).toHaveCount(2)
  await expect(page.locator('[role="status"]').filter({ hasText: 'Harbor daemon is disconnected.' })).toHaveCount(1)
})

test('keeps search and appearance reachable from mobile navigation', async ({ page }) => {
  await page.setViewportSize({ width: 430, height: 760 })
  await page.goto('/#/overview', { waitUntil: 'domcontentloaded' })
  await page.getByRole('button', { name: 'More Harbor actions' }).click()

  await expect(page.getByRole('menuitem', { name: 'Settings', exact: true })).toBeVisible()
  await expect(page.getByRole('menuitem', { name: 'Dark appearance' })).toBeVisible()
  await page.getByRole('menuitem', { name: 'Search Harbor' }).click()

  await expect(page.getByRole('dialog', { name: 'Command Menu' })).toBeVisible()
})

test('keeps the development fixture marker clear of mobile navigation', async ({ page }) => {
  await page.setViewportSize({ width: 430, height: 760 })
  await page.goto('/#/overview', { waitUntil: 'domcontentloaded' })

  const marker = page.getByText('Development fixture', { exact: true })
  const navigation = page.locator('.harbor-mobile-slot')
  await expect(marker).toBeVisible()
  await expect(navigation).toBeVisible()

  const markerBox = await marker.boundingBox()
  const navigationBox = await navigation.boundingBox()
  expect(markerBox).not.toBeNull()
  expect(navigationBox).not.toBeNull()
  if (!markerBox || !navigationBox) {
    throw new Error('fixture marker and navigation must have measurable bounds')
  }
  expect(markerBox.y + markerBox.height).toBeLessThanOrEqual(navigationBox.y)
})

test('reports resource-open failures without leaving an unhandled action', async ({ page }) => {
  await page.addInitScript(() => {
    window.open = () => {
      throw new Error('The browser rejected the request')
    }
  })
  await page.goto('/#/overview', { waitUntil: 'domcontentloaded' })

  await page.getByRole('button', { name: 'Open Application for Orders API' }).click()

  await expect(page.getByText('Harbor could not open the resource', { exact: true })).toBeVisible()
  await expect(page.getByText('The browser rejected the request', { exact: true })).toBeVisible()
})

test('creates a project through schema-driven choices and keeps it stopped', async ({ page }) => {
  await page.goto('/#/overview', { waitUntil: 'domcontentloaded' })
  await page.getByRole('button', { name: 'New project', exact: true }).click()
  const dialog = page.getByRole('dialog', { name: 'New project' })
  await expect(dialog.getByRole('img', { name: 'GoForj', exact: true })).toBeVisible()
  await expect(dialog.getByTestId('selected-project-stack')).toContainText('GoForj')
  await dialog.getByRole('button', { name: 'Continue', exact: true }).click()
  await expect(dialog.getByRole('alert')).toHaveText('Project name is required.')
  await dialog.getByLabel('Project name', { exact: true }).fill('Project Creation Demo')
  await dialog.getByLabel('Go module', { exact: true }).fill('example.com/project-creation-demo')
  await dialog.getByRole('button', { name: 'Choose project location' }).click()
  await expect(dialog.getByLabel('Project directory', { exact: true })).toHaveValue('/workspace/apps/project-creation-demo')
  await dialog.getByRole('button', { name: 'Continue', exact: true }).click()
  await dialog.getByRole('checkbox', { name: 'Mail', exact: true }).uncheck()
  await expect(dialog.getByRole('checkbox', { name: 'Auth', exact: true })).not.toBeChecked()
  await expect(dialog.getByRole('checkbox', { name: 'OAuth', exact: true })).not.toBeChecked()
  await dialog.getByRole('checkbox', { name: 'OAuth', exact: true }).check()
  await expect(dialog.getByRole('checkbox', { name: 'Mail', exact: true })).toBeChecked()
  await expect(dialog.getByRole('checkbox', { name: 'Auth', exact: true })).toBeChecked()
  await dialog.getByRole('checkbox', { name: 'Database (Postgres)', exact: true }).check()
  await expect(dialog.getByRole('checkbox', { name: 'Database (MySQL)', exact: true })).not.toBeChecked()
  await dialog.getByLabel('Starter kit', { exact: true }).selectOption('vue')
  await dialog.getByRole('button', { name: 'Continue', exact: true }).click()
  await dialog.getByLabel('Atlas agent support', { exact: true }).selectOption('custom')
  await expect(dialog.getByRole('group', { name: 'Agents', exact: true })).toBeVisible()
  await expect(dialog.getByRole('checkbox', { name: 'MCP server config', exact: true })).toBeChecked()
  await dialog.getByLabel('Atlas agent support', { exact: true }).selectOption('skip')
  await expect(dialog.getByRole('group', { name: 'Agents', exact: true })).toBeHidden()
  await dialog.getByRole('button', { name: 'Continue', exact: true }).click()
  await expect(dialog.getByText('Project Creation Demo', { exact: true })).toBeVisible()
  await expect(dialog.getByText('Vue', { exact: true })).toBeVisible()
  await dialog.getByRole('button', { name: 'Create project', exact: true }).click()
  await expect(dialog.getByText('Creating project files', { exact: true })).toBeVisible()
  await dialog.getByText('Command output', { exact: true }).click()
  await expect(dialog.getByLabel('Creation output')).toContainText('Rendering project files')
  await expect(dialog).toBeHidden()
  await expect(page.getByRole('heading', { name: 'Project Creation Demo', exact: true })).toBeVisible()
  await expect(page.getByRole('button', { name: 'Start project', exact: true })).toBeVisible()
})

test('previews CLI help formats from the project creator schema', async ({ page }) => {
  await page.emulateMedia({ colorScheme: 'light' })
  await page.goto('/#/overview', { waitUntil: 'domcontentloaded' })
  await page.getByRole('button', { name: 'New project', exact: true }).click()
  const dialog = page.getByRole('dialog', { name: 'New project' })
  await dialog.getByLabel('Project name', { exact: true }).fill('Help Preview')
  await dialog.getByLabel('Go module', { exact: true }).fill('example.com/help-preview')
  await dialog.getByLabel('Project directory', { exact: true }).fill('/workspace/apps/help-preview')
  await dialog.getByRole('button', { name: 'Continue', exact: true }).click()
  await expect(dialog.getByRole('region', { name: 'Starter kit preview' })).toHaveCount(0)
  await dialog.getByRole('button', { name: 'Continue', exact: true }).click()
  const preview = dialog.getByRole('region', { name: 'CLI help format preview' })
  await expect(preview).toContainText('Application command surface')
  await expect(preview.locator('.harbor-log-viewport')).toHaveCSS('background-color', 'rgb(246, 248, 250)')
  await expect(preview).toContainText('make:migration')
  await dialog.getByLabel('CLI help format', { exact: true }).selectOption('guided')
  await expect(preview).toContainText('Examples')
  await expect(preview).toContainText('Common commands')
  await expect(preview).not.toContainText('make:migration')
  await dialog.getByLabel('CLI help format', { exact: true }).selectOption('external_cli')
  await expect(preview).toContainText('Commands')
  await expect(preview).not.toContainText('Examples')
  await page.setViewportSize({ width: 430, height: 800 })
  await expect(preview).toBeVisible()
  expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBe(true)
  await dialog.getByRole('button', { name: 'Continue', exact: true }).click()
  await expect(dialog.getByText('External CLI', { exact: true })).toBeVisible()
})

test('retries registration without creating project files again', async ({ page }) => {
  await page.goto('/#/overview', { waitUntil: 'domcontentloaded' })
  await page.evaluate(async () => {
    const path = '/src/bridge/index.ts'
    const { harborBridge } = await import(/* @vite-ignore */ path) as { harborBridge: import('../src/bridge/types').HarborBridge }
    const start = harborBridge.startProjectCreation.bind(harborBridge)
    const read = harborBridge.getProjectCreationProgress.bind(harborBridge)
    const retry = harborBridge.retryProjectRegistration.bind(harborBridge)
    let starts = 0
    let retried = false
    const observe = (window as typeof window & { creationStarts: number })
    harborBridge.startProjectCreation = async request => { observe.creationStarts = ++starts; return start(request) }
    harborBridge.getProjectCreationProgress = async (id, cursor) => {
      const update = await read(id, cursor)
      if (update.phase === 'completed' && !retried) {
        return { ...update, phase: 'registration_failed', result: { path: update.path, output: '', registration_error: 'Files are ready. Harbor is temporarily unavailable.' } }
      }
      return update
    }
    harborBridge.retryProjectRegistration = async id => { retried = true; return retry(id) }
  })
  await page.getByRole('button', { name: 'New project', exact: true }).click()
  const dialog = page.getByRole('dialog', { name: 'New project' })
  await dialog.getByLabel('Project name', { exact: true }).fill('Retry Registration Demo')
  await dialog.getByLabel('Go module', { exact: true }).fill('example.com/retry-demo')
  await dialog.getByLabel('Project directory', { exact: true }).fill('/workspace/apps/retry-demo')
  for (let i = 0; i < 3; i++) await dialog.getByRole('button', { name: 'Continue', exact: true }).click()
  await dialog.getByRole('button', { name: 'Create project', exact: true }).click()
  await expect(dialog.getByRole('alert')).toContainText('Harbor is temporarily unavailable')
  await dialog.getByRole('button', { name: 'Retry registration', exact: true }).click()
  await expect(dialog).toBeHidden()
  expect(await page.evaluate(() => (window as typeof window & { creationStarts: number }).creationStarts)).toBe(1)
  await expect(page.getByRole('heading', { name: 'Retry Registration Demo', exact: true })).toBeVisible()
})

test('keeps failed creation output and returns to editable choices', async ({ page }) => {
  await page.goto('/#/overview', { waitUntil: 'domcontentloaded' })
  await page.evaluate(async () => {
    const path = '/src/bridge/index.ts'
    const { harborBridge } = await import(/* @vite-ignore */ path) as { harborBridge: import('../src/bridge/types').HarborBridge }
    const read = harborBridge.getProjectCreationProgress.bind(harborBridge)
    harborBridge.getProjectCreationProgress = async (id, cursor) => ({ ...await read(id, cursor), phase: 'failed', error: 'Dependency download failed. Files were kept. Choose a new or empty directory.', output: 'Registry temporarily unavailable.\n', result: undefined })
  })
  await page.getByRole('button', { name: 'New project', exact: true }).click()
  const dialog = page.getByRole('dialog', { name: 'New project' })
  await dialog.getByLabel('Project name', { exact: true }).fill('Failure Demo')
  await dialog.getByLabel('Go module', { exact: true }).fill('example.com/failure')
  await dialog.getByLabel('Project directory', { exact: true }).fill('/workspace/apps/failure')
  for (let i = 0; i < 3; i++) await dialog.getByRole('button', { name: 'Continue', exact: true }).click()
  await dialog.getByRole('button', { name: 'Create project', exact: true }).click()
  await expect(dialog.getByRole('alert')).toContainText('Files were kept')
  await expect(dialog.getByLabel('Creation output')).toContainText('Registry temporarily unavailable')
  await expect(dialog.getByRole('button', { name: 'Retry registration', exact: true })).toHaveCount(0)
  await dialog.getByRole('button', { name: 'Review choices', exact: true }).click()
  await expect(dialog.getByLabel('Project name', { exact: true })).toHaveValue('Failure Demo')
  await dialog.getByLabel('Project directory', { exact: true }).fill('/workspace/apps/failure-retry')
})

test('keeps command search aligned with service branding and every current resource', async ({ page }) => {
  await page.goto('/#/overview', { waitUntil: 'domcontentloaded' })
  await page.getByRole('button', { name: 'Open command menu' }).click()
  const dialog = page.getByRole('dialog', { name: 'Command Menu' })
  const search = dialog.getByPlaceholder('Search projects, services, and Harbor…')
  await search.fill('mysql')
  await expect(dialog.locator('[data-service-brand="mysql"] img')).toBeVisible()
  await dialog.getByRole('option').filter({ hasText: 'MySQL' }).first().click()
  await expect(page).toHaveURL(/#\/services\/orders-api\/mysql$/)

  const resource = harborWireFixture.snapshot.projects.flatMap(project => project.resources.map(resource => ({ ...resource, project_id: project.id })))[0]!
  await page.evaluate(async resource => {
    const path = '/src/stores/harbor.ts'
    const { useHarborStore } = await import(/* @vite-ignore */ path) as { useHarborStore: typeof import('../src/stores/harbor').useHarborStore }
    const store = useHarborStore()
    store.snapshot = { ...store.snapshot!, recent_resource_ids: store.snapshot!.recent_resource_ids.filter(entry => entry.project_id !== resource.project_id || entry.resource_id !== resource.id) }
  }, resource)
  await page.getByRole('button', { name: 'Open command menu' }).click()
  await search.fill(resource.url)
  await expect(dialog.getByRole('group', { name: 'Resources', exact: true }).getByRole('option').filter({ hasText: resource.name })).toBeVisible()
})

test('opens shared creation and appearance controls from the command menu', async ({ page }) => {
  await page.goto('/#/overview', { waitUntil: 'domcontentloaded' })
  const trigger = page.getByRole('button', { name: 'Open command menu' })
  const menu = page.getByRole('dialog', { name: 'Command Menu' })
  await trigger.click()
  await menu.getByPlaceholder('Search projects, services, and Harbor…').fill('New project')
  await menu.getByRole('option', { name: 'New project', exact: true }).click()
  const wizard = page.getByRole('dialog', { name: 'New project', exact: true })
  await expect(wizard).toBeVisible()
  await expect(menu).toBeHidden()
  await expect(wizard.getByRole('img', { name: 'GoForj', exact: true })).toBeVisible()
  await wizard.getByRole('button', { name: 'Cancel', exact: true }).click()
  await trigger.click()
  await menu.getByPlaceholder('Search projects, services, and Harbor…').fill('terminal themes')
  await menu.getByRole('option').filter({ hasText: 'Appearance' }).click()
  await expect(page).toHaveURL(/#\/settings\/appearance$/)
  await expect(page.getByRole('radiogroup', { name: 'Terminal appearance', exact: true })).toBeVisible()
  await trigger.click()
  await menu.getByPlaceholder('Search projects, services, and Harbor…').fill('Use light appearance')
  await menu.getByRole('option').filter({ hasText: 'Use light appearance' }).click()
  await expect(page.locator('html')).not.toHaveClass(/dark/)
  await trigger.click()
  await menu.getByPlaceholder('Search projects, services, and Harbor…').fill('Connection and diagnostics')
  await menu.getByRole('option').filter({ hasText: 'Daemon' }).click()
  await expect(page).toHaveURL(/#\/settings\/daemon$/)
  await page.evaluate(async () => {
    const path = '/src/stores/harbor.ts'
    const { useHarborStore } = await import(/* @vite-ignore */ path) as { useHarborStore: typeof import('../src/stores/harbor').useHarborStore }
    useHarborStore().connectionState = 'disconnected'
  })
  await trigger.click()
  await menu.getByPlaceholder('Search projects, services, and Harbor…').fill('New project')
  await expect(menu.getByRole('option', { name: 'New project', exact: true })).toHaveAttribute('data-disabled', '')
})
