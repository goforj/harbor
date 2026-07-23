import { flushPromises, mount } from '@vue/test-utils'
import { createPinia, setActivePinia } from 'pinia'
import { createMemoryHistory, createRouter } from 'vue-router'
import { describe, expect, it, vi } from 'vitest'
import { harborBridge } from '@/bridge'
import type { NetworkResolverPolicyMigrationOperation } from '@/domain/harbor'
import { useHarborStore } from '@/stores/harbor'
import OverviewView from './OverviewView.vue'

function deferred<T>() {
  let resolve!: (value: T) => void
  const promise = new Promise<T>((resolvePromise) => {
    resolve = resolvePromise
  })
  return { promise, resolve }
}

function completedOldNetworkingRemoval(): NetworkResolverPolicyMigrationOperation {
  return {
    operation: {
      id: 'operation-old-networking-removal',
      intent_id: 'intent-old-networking-removal',
      kind: 'network.resolver.policy-migration',
      state: 'succeeded',
      phase: 'completed',
      requested_at: '2026-07-23T12:00:00Z',
      started_at: '2026-07-23T12:00:01Z',
      finished_at: '2026-07-23T12:00:02Z',
    },
    revision: 43,
  }
}

async function mountOverview() {
  const pinia = createPinia()
  setActivePinia(pinia)
  const store = useHarborStore()
  await store.initialize()
  store.$patch({
    daemonStatus: store.daemonStatus
      ? {
          ...store.daemonStatus,
          capabilities: [
            ...store.daemonStatus.capabilities,
            'control.network-setup.v1',
            'control.network-resolver-policy-migration.v1',
          ],
        }
      : null,
    networkSetupError: 'Harbor detected old networking.',
  })
  const router = createRouter({
    history: createMemoryHistory(),
    routes: [{ path: '/', component: OverviewView }],
  })
  await router.push('/')
  await router.isReady()
  const wrapper = mount(OverviewView, { attachTo: document.body, global: { plugins: [pinia, router] } })
  await flushPromises()
  return { store, wrapper }
}

describe('OverviewView', () => {
  it('offers old networking removal only after setup fails and disables both actions while it runs', async () => {
    const { store, wrapper } = await mountOverview()
    const pending = deferred<NetworkResolverPolicyMigrationOperation>()
    const removal = vi.spyOn(harborBridge, 'removeOldNetworking').mockReturnValueOnce(pending.promise)
    const buttons = wrapper.findAll('button')
    const setup = buttons.find((button) => button.text().includes('Set up secure networking'))
    const remove = buttons.find((button) => button.text().includes('Remove old networking'))

    expect(setup).toBeDefined()
    expect(remove).toBeDefined()
    await remove!.trigger('click')
    expect(removal).toHaveBeenCalledOnce()
    expect(store.removingOldNetworking).toBe(true)
    expect(setup!.attributes('disabled')).toBeDefined()
    expect(remove!.attributes('disabled')).toBeDefined()
    pending.resolve(completedOldNetworkingRemoval())
    await flushPromises()
    expect(store.removingOldNetworking).toBe(false)

    store.$patch({ networkSetupResult: { operation: completedOldNetworkingRemoval().operation, revision: 43 } })
    await wrapper.vm.$nextTick()
    expect(wrapper.text()).not.toContain('Remove old networking')
    wrapper.unmount()
  })

  it('does not render old networking removal without the advertised capability', async () => {
    const { store, wrapper } = await mountOverview()
    store.$patch({
      daemonStatus: {
        ...store.daemonStatus!,
        capabilities: ['control.network-setup.v1'],
      },
    })
    await wrapper.vm.$nextTick()

    expect(wrapper.text()).not.toContain('Remove old networking')
    wrapper.unmount()
  })

  it('removes a stale recovery action when the selected background service cannot perform it', async () => {
    const { wrapper } = await mountOverview()
    vi.spyOn(harborBridge, 'removeOldNetworking').mockRejectedValueOnce(
      new Error('Harbor daemon does not support network resolver policy migration; upgrade or restart harbord'),
    )
    vi.spyOn(harborBridge, 'getStatus').mockRejectedValueOnce(new Error('daemon session changed'))
    const remove = wrapper.findAll('button').find((button) => button.text().includes('Remove old networking'))

    await remove!.trigger('click')
    await flushPromises()

    expect(wrapper.text()).toContain('Harbor’s background service cannot remove old networking. Restart harbord if you manage it; Harbor will reconnect after it restarts. Harbor did not stop the background service.')
    expect(wrapper.findAll('button').some((button) => button.text().includes('Remove old networking'))).toBe(false)
    wrapper.unmount()
  })

  it('keeps failed removal feedback with its retry action instead of the setup message', async () => {
    const { store, wrapper } = await mountOverview()
    store.$patch({
      networkSetupError: null,
      oldNetworkingRemovalError: 'Old networking is still active.',
    })
    await wrapper.vm.$nextTick()

    expect(wrapper.find('p[role="alert"]').text()).toBe('Old networking is still active.')
    expect(wrapper.findAll('button').some((button) => button.text().includes('Remove old networking'))).toBe(true)
    wrapper.unmount()
  })
})
