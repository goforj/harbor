import { expect, test } from '@playwright/test'
import { harborWireFixture } from '../src/bridge/harbor.fixture'

test('navigates from the Harbor overview to the project list', async ({ page }) => {
  await page.goto('/#/overview')

  await expect(page).toHaveTitle('Overview · GoForj Harbor')
  await expect(page.getByRole('button', { name: 'Overview', exact: true })).toBeVisible()
  await expect(page.getByRole('button', { name: 'Projects', exact: true })).toBeVisible()
  await expect(page.getByRole('button', { name: 'Services', exact: true })).toBeVisible()
  await expect(page.getByRole('button', { name: 'System', exact: true })).toBeVisible()

  await page.getByRole('button', { name: 'Projects', exact: true }).click()

  await expect(page).toHaveURL(/#\/projects(?:\/|$)/)
  await expect(page.getByText('Orders API', { exact: true }).first()).toBeVisible()
  await expect(page.getByText('Billing', { exact: true }).first()).toBeVisible()
})

test('adds the selected project and opens its detail immediately', async ({ page }) => {
  await page.goto('/#/overview')

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
          async OpenResource() {},
          async RemoveProject() {
            throw new Error('Project removal is not exercised in this setup test')
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
            snapshot = { ...snapshot, sequence: revision }
            status.sequence = revision
            return result
          },
          async StartProject() {
            throw new Error('Project start is not exercised in this setup test')
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

  await page.goto('/#/overview')

  await expect(page.getByText('This action is safe to run again.', { exact: false })).toBeVisible()
  await page.getByRole('button', { name: 'Set up networking', exact: true }).click()

  await expect(page.getByText('Harbor networking is ready.', { exact: true })).toBeVisible()
  await expect(page.getByText('The local network foundation was verified or completed.', { exact: true })).toBeVisible()
  await expect.poll(() => page.evaluate(() => (window as typeof window & { networkSetupCalls: number }).networkSetupCalls)).toBe(1)
})

test('starts and stops projects from their selected detail view', async ({ page }) => {
  await page.goto('/#/projects/reports')

  await page.getByRole('button', { name: 'Start project', exact: true }).click()
  await expect(page.getByRole('button', { name: 'Starting…', exact: true })).toBeDisabled()

  await page.goto('/#/projects/orders-api')
  await page.getByRole('button', { name: 'Stop project', exact: true }).click()
  await expect(page.getByRole('button', { name: 'Stopping…', exact: true })).toBeDisabled()
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
          async OpenResource() {},
          async RemoveProject() { throw new Error('Quarantined project removal is disabled') },
          async SetupNetwork() { throw new Error('Network setup is not exercised in this recovery test') },
          async StartProject() { throw new Error('Quarantined project start is disabled') },
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

  await page.goto('/#/projects/reports')

  const recoveryAlert = page.getByRole('alert')
  await expect(recoveryAlert.getByText('Project recovery required', { exact: true })).toBeVisible()
  await expect(recoveryAlert.getByText('Harbor restarted before it could record the managed process identity.', { exact: true })).toBeVisible()
  await expect(page.getByRole('button', { name: 'Recovery required', exact: true })).toBeDisabled()
  await expect(page.getByRole('button', { name: 'Starting…', exact: true })).toHaveCount(0)
  await expect(page.getByRole('heading', { name: 'Current activity' })).toBeVisible()
  await expect(page.getByRole('region', { name: 'Project summary' }).getByText('recovery required', { exact: true })).toBeVisible()
})

test('confirms project removal and explains when desktop approval is unavailable', async ({ page }) => {
  await page.goto('/#/projects/orders-api')

  const remove = page.getByRole('button', { name: 'Remove project', exact: true })
  await remove.click()

  const dialog = page.getByRole('alertdialog')
  await expect(dialog.getByRole('heading', { name: 'Remove Orders API?' })).toBeVisible()
  await expect(dialog.getByText('The project files at /workspace/apps/orders-api will stay on disk.', { exact: false })).toBeVisible()
  await dialog.getByRole('button', { name: 'Remove project', exact: true }).click()

  await expect(page.getByText('Administrator approval required', { exact: true })).toBeVisible()
  await expect(page.getByText('Approval is not available from the desktop app yet.', { exact: false })).toBeVisible()
  await expect(page.getByRole('button', { name: 'Awaiting approval', exact: true })).toBeDisabled()
  await expect(page.getByRole('heading', { name: 'Orders API' })).toBeVisible()
})

test('refreshes and leaves project detail after an immediate removal', async ({ page }) => {
  await page.goto('/#/projects/reports')

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
          async OpenResource() {},
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
          async SetupNetwork() {
            throw new Error('Network setup is not exercised in this removal test')
          },
          async StartProject(projectId, intentId) {
            const result = structuredClone(initialStart)
            result.operation.project_id = projectId
            result.operation.intent_id = intentId
            return result
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
  await page.goto('/#/projects/reports')

  await page.getByRole('button', { name: 'Remove project', exact: true }).click()
  await page.getByRole('alertdialog').getByRole('button', { name: 'Remove project', exact: true }).click()

  await expect(page).toHaveURL(/#\/projects$/)
  await expect(page.getByText('Reports', { exact: true })).toHaveCount(0)
})

test('disables removal honestly across projects while another request is in flight', async ({ page }) => {
  await page.goto('/#/projects/orders-api')
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
  await page.goto('/#/projects/orders-api')

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

test('routes services with project and service identities', async ({ page }) => {
  await page.goto('/#/services/orders-api/mysql')

  await expect(page.getByRole('heading', { name: 'MySQL' })).toBeVisible()
  await expect(page.getByText('orders-api', { exact: true }).first()).toBeVisible()
  await expect(page).toHaveURL(/#\/services\/orders-api\/mysql$/)
})

test('searches authoritative project metadata that is not rendered in item labels', async ({ page }) => {
  await page.goto('/#/overview')
  await page.keyboard.press('Control+k')

  const dialog = page.getByRole('dialog', { name: 'Command Menu' })
  await expect(dialog).toBeVisible()
  await dialog.getByPlaceholder('Search projects, services, and Harbor…').fill('/workspace/apps/billing')
  await expect(dialog.getByText('Billing', { exact: true })).toBeVisible()
})

test('opens and closes the command menu with accessible keyboard focus', async ({ page }) => {
  await page.goto('/#/overview')

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

test('renders only status and snapshot facts on the system page', async ({ page }) => {
  await page.goto('/#/system')

  await expect(page.locator('#system-title')).toBeVisible()
  await expect(page.getByText('Daemon status')).toBeVisible()
  await expect(page.getByText('Snapshot', { exact: true })).toBeVisible()
  await expect(page.getByText('control.v1')).toBeVisible()
  await expect(page.getByText('DNS and ingress')).toHaveCount(0)
  await expect(page.getByText('HTTPS and trust')).toHaveCount(0)
})

test('fails closed for a production browser unless its fixture is explicitly requested', async ({ page }) => {
  await page.goto('/#/overview')

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
          async OpenResource() {},
          async RemoveProject() {
            throw new Error('Project removal is not exercised in this connection test')
          },
          async SetupNetwork() {
            throw new Error('Network setup is not exercised in this connection test')
          },
          async StartProject() {
            throw new Error('Project start is not exercised in this connection test')
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
  await page.goto('/#/overview')

  const detail = page.locator('.harbor-detail-slot')
  await expect(detail.getByText('Connected to Harbor. Waiting for the first snapshot.')).toBeVisible()
  await expect(detail.getByText('Harbor daemon is starting')).toBeVisible()

  await detail.getByRole('button', { name: 'Try again' }).click()

  await expect(detail.getByRole('heading', { name: 'Overview' })).toBeVisible()
  await expect(detail.getByText('Connected to Harbor. Waiting for the first snapshot.')).toBeHidden()
  await expect(detail.getByText('Harbor daemon is starting')).toBeHidden()
  await expect(detail.getByText('Add your first project', { exact: true })).toBeVisible()
  await expect(detail.getByRole('button', { name: 'Choose a project folder', exact: true })).toBeVisible()
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
          async OpenResource() {},
          async RemoveProject() {
            throw new Error('Project removal is not exercised in this connection test')
          },
          async SetupNetwork() {
            throw new Error('Network setup is not exercised in this connection test')
          },
          async StartProject() {
            throw new Error('Project start is not exercised in this connection test')
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
  await page.goto('/#/overview')

  const detail = page.locator('.harbor-detail-slot')
  await expect(page.getByText('Connected to Harbor. Waiting for the first snapshot.', { exact: true }).first()).toBeVisible()
  await expect(page.getByText('snapshot endpoint unavailable', { exact: true }).first()).toBeVisible()
  await expect(detail.getByRole('heading', { name: 'Overview' })).toHaveCount(0)
  await expect(page.getByText('No overview yet', { exact: true })).toHaveCount(0)

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
  await page.goto('/#/overview')
  await page.getByRole('button', { name: 'More Harbor actions' }).click()

  await expect(page.getByRole('menuitem', { name: 'System', exact: true })).toBeVisible()
  await expect(page.getByRole('menuitem', { name: 'Dark appearance' })).toBeVisible()
  await page.getByRole('menuitem', { name: 'Search Harbor' }).click()

  await expect(page.getByRole('dialog', { name: 'Command Menu' })).toBeVisible()
})

test('keeps the development fixture marker clear of mobile navigation', async ({ page }) => {
  await page.setViewportSize({ width: 430, height: 760 })
  await page.goto('/#/overview')

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
  await page.goto('/#/overview')

  await page.getByRole('button', { name: 'Open Application for Orders API' }).click()

  await expect(page.getByText('Harbor could not open the resource', { exact: true })).toBeVisible()
  await expect(page.getByText('The browser rejected the request', { exact: true })).toBeVisible()
})
