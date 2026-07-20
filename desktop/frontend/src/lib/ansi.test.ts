import { describe, expect, it } from 'vitest'
import { ansiToHtml } from './ansi'

describe('ansiToHtml', () => {
  it('renders standard and bright terminal colors across resets', () => {
    expect(ansiToHtml('\u001b[31mfailed\u001b[0m plain \u001b[92mready\u001b[39m')).toBe(
      '<span style="color:#cd3131">failed</span> plain <span style="color:#23d18b">ready</span>',
    )
  })

  it('renders text emphasis, backgrounds, and selective resets', () => {
    expect(ansiToHtml('\u001b[1;4;44mbooting\u001b[22;24m next')).toBe(
      '<span style="background-color:#2472c8;font-weight:700;text-decoration-line:underline">booting</span><span style="background-color:#2472c8"> next</span>',
    )
  })

  it('renders indexed, true-color, and inverse output', () => {
    expect(ansiToHtml('\u001b[38;5;214mwarning\u001b[0m \u001b[38;2;12;34;56mcustom\u001b[7m inverse')).toBe(
      '<span style="color:rgb(255 175 0)">warning</span> <span style="color:rgb(12 34 56)">custom</span><span style="color:#09090b;background-color:rgb(12 34 56)"> inverse</span>',
    )
  })

  it('escapes process output before returning renderer-owned HTML', () => {
    const html = ansiToHtml('\u001b[31m<script data-value="x">alert(\'bad\')</script> & done\u001b[0m')

    expect(html).toBe('<span style="color:#cd3131">&lt;script data-value=&quot;x&quot;&gt;alert(&#39;bad&#39;)&lt;/script&gt; &amp; done</span>')
    expect(html).not.toContain('<script')
  })

  it('discards hyperlinks and cursor commands while preserving their visible labels', () => {
    expect(ansiToHtml('\u001b]8;;https://example.com\u001b\\Harbor\u001b]8;;\u001b\\\u001b[2K ready')).toBe('Harbor ready')
  })

  it('drops incomplete control sequences until more output arrives', () => {
    expect(ansiToHtml('ready\n\u001b[38;2;255')).toBe('ready\n')
  })
})
