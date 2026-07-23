import { flushPromises, mount } from '@vue/test-utils'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { harborBridge } from '@/bridge'
import ResourceFavicon from './ResourceFavicon.vue'

const props = {
  name: 'Orders',
  url: 'http://orders.test:8080',
  projectId: 'orders',
  resourceId: 'application',
}

function deferred<T>() {
  let resolve!: (value: T | PromiseLike<T>) => void
  let reject!: (reason?: unknown) => void
  const promise = new Promise<T>((nextResolve, nextReject) => {
    resolve = nextResolve
    reject = nextReject
  })
  return { promise, reject, resolve }
}

describe('ResourceFavicon', () => {
  afterEach(() => {
    vi.restoreAllMocks()
  })

  it.each([
    ['an empty lookup', () => Promise.resolve('')],
    ['a failed lookup', () => Promise.reject(new Error('resource unavailable'))],
    ['a cross-origin declared icon', () => Promise.resolve('http://other.test/icon.svg')],
  ])('keeps the local fallback for %s without probing favicon URLs', async (_scenario, lookup) => {
    vi.spyOn(harborBridge, 'getResourceIconURL').mockImplementationOnce(lookup)

    const wrapper = mount(ResourceFavicon, { props })
    expect(wrapper.find('img').exists()).toBe(false)

    await flushPromises()

    expect(wrapper.find('img').exists()).toBe(false)
    expect(wrapper.get('span').text()).toBe('O')
  })

  it('renders one declared same-origin icon and falls back when it fails to load', async () => {
    vi.spyOn(harborBridge, 'getResourceIconURL').mockResolvedValueOnce('http://orders.test:8080/assets/icon.svg')

    const wrapper = mount(ResourceFavicon, { props })
    await flushPromises()

    const icon = wrapper.get('img')
    expect(wrapper.findAll('img')).toHaveLength(1)
    expect(icon.attributes('src')).toBe('http://orders.test:8080/assets/icon.svg')

    await icon.trigger('error')

    expect(wrapper.find('img').exists()).toBe(false)
    expect(wrapper.get('span').text()).toBe('O')
  })

  it('ignores a stale successful lookup after the resource changes', async () => {
    const firstLookup = deferred<string>()
    const secondLookup = deferred<string>()
    vi.spyOn(harborBridge, 'getResourceIconURL')
      .mockImplementationOnce(() => firstLookup.promise)
      .mockImplementationOnce(() => secondLookup.promise)

    const wrapper = mount(ResourceFavicon, { props })
    await wrapper.setProps({ url: 'http://billing.test:8080' })
    secondLookup.resolve('http://billing.test:8080/assets/icon.svg')
    await flushPromises()

    firstLookup.resolve('http://orders.test:8080/assets/icon.svg')
    await flushPromises()

    expect(wrapper.get('img').attributes('src')).toBe('http://billing.test:8080/assets/icon.svg')
  })

  it('ignores a stale failed lookup after the resource changes', async () => {
    const firstLookup = deferred<string>()
    const secondLookup = deferred<string>()
    vi.spyOn(harborBridge, 'getResourceIconURL')
      .mockImplementationOnce(() => firstLookup.promise)
      .mockImplementationOnce(() => secondLookup.promise)

    const wrapper = mount(ResourceFavicon, { props })
    await wrapper.setProps({ url: 'http://billing.test:8080' })
    secondLookup.resolve('http://billing.test:8080/assets/icon.svg')
    await flushPromises()

    firstLookup.reject(new Error('resource unavailable'))
    await flushPromises()

    expect(wrapper.get('img').attributes('src')).toBe('http://billing.test:8080/assets/icon.svg')
  })

  it('ignores an error event from a stale image URL', async () => {
    vi.spyOn(harborBridge, 'getResourceIconURL').mockResolvedValueOnce('http://orders.test:8080/assets/icon.svg')

    const wrapper = mount(ResourceFavicon, { props })
    await flushPromises()

    const icon = wrapper.get('img')
    icon.element.src = 'http://orders.test:8080/assets/old-icon.svg'
    await icon.trigger('error')

    expect(wrapper.findAll('img')).toHaveLength(1)
    expect(wrapper.find('span').exists()).toBe(false)
  })
})
