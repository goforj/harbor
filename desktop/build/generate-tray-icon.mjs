import { readFile, writeFile } from 'node:fs/promises'
import { createRequire } from 'node:module'

const require = createRequire(new URL('../frontend/package.json', import.meta.url))
const { chromium } = require('@playwright/test')

// Reuse the canonical anchor paths at menu bar scale without the application tile.
const artwork = (await readFile(new URL('appicon-symbol.svg', import.meta.url), 'utf8'))
  .replace('viewBox="0 0 1024 1024"', 'viewBox="0 0 24 24"')
  .replace('stroke="#0d0d0d"', 'stroke="#000000"')
  .replace(/\s+transform="[^"]*"/, '')

const browser = await chromium.launch({ headless: true })
try {
  const page = await browser.newPage()
  const png = await page.evaluate(async (svg) => {
    const image = new Image()
    image.src = `data:image/svg+xml;base64,${btoa(svg)}`
    await image.decode()
    const canvas = document.createElement('canvas')
    canvas.width = canvas.height = 48
    canvas.getContext('2d').drawImage(image, 0, 0, 48, 48)
    return canvas.toDataURL('image/png').split(',')[1]
  }, artwork)
  await writeFile(new URL('tray-template.png', import.meta.url), Buffer.from(png, 'base64'))
} finally {
  await browser.close()
}
