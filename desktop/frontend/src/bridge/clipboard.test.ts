import { afterEach, describe, expect, it, vi } from 'vitest'
import { copyText } from './clipboard'

describe('copyText', () => {
  afterEach(() => {
    delete window.runtime
    Object.defineProperty(navigator, 'clipboard', {
      configurable: true,
      value: undefined,
    })
  })

  it('prefers the native desktop clipboard when it is available', async () => {
    const ClipboardSetText = vi.fn().mockResolvedValue(true)
    const writeText = vi.fn().mockResolvedValue(undefined)
    window.runtime = { ClipboardSetText }
    Object.defineProperty(navigator, 'clipboard', {
      configurable: true,
      value: { writeText },
    })

    await copyText('https://orders.test')

    expect(ClipboardSetText).toHaveBeenCalledWith('https://orders.test')
    expect(writeText).not.toHaveBeenCalled()
  })

  it('reports when the native clipboard rejects a value', async () => {
    window.runtime = { ClipboardSetText: vi.fn().mockResolvedValue(false) }

    await expect(copyText('orders.test')).rejects.toThrow(
      'The native clipboard rejected the value.',
    )
  })

  it('uses the browser clipboard when the native helper is absent', async () => {
    const writeText = vi.fn().mockResolvedValue(undefined)
    window.runtime = {}
    Object.defineProperty(navigator, 'clipboard', {
      configurable: true,
      value: { writeText },
    })

    await copyText('/workspace/apps/orders-api')

    expect(writeText).toHaveBeenCalledWith('/workspace/apps/orders-api')
  })

  it('fails clearly when neither clipboard API is available', async () => {
    await expect(copyText('orders.test')).rejects.toThrow(
      'Clipboard access is unavailable.',
    )
  })
})
