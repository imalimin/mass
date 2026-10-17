import assert from 'node:assert/strict'
import { mkdir } from 'node:fs/promises'
import { createRequire } from 'node:module'
import { resolve } from 'node:path'
import { startFixtureServer } from './fixture-server.mjs'

process.env.PLAYWRIGHT_BROWSERS_PATH ??= resolve('.cache/playwright')
const require = createRequire(new URL('../frontend/package.json', import.meta.url))
const { chromium } = require('@playwright/test')
const server = await startFixtureServer()
let browser
try {
  browser = await chromium.launch({ headless: true, channel: 'chromium' })
  const page = await browser.newPage({ viewport: { width: 1440, height: 1000 } })
  const errors = []
  page.on('pageerror', error => errors.push(error.message))
  await page.goto(server.url)
  await page.getByText('已连接 MASS 服务', { exact: true }).waitFor()
  await page.getByRole('heading', { name: '从一个协作空间开始' }).waitFor()
  await mkdir('.cache', { recursive: true })
  await page.screenshot({ path: '.cache/d00-desktop.png', fullPage: true })
  await page.getByRole('link', { name: '◈ 空间' }).click()
  assert.equal(new URL(page.url()).pathname, '/spaces')
  await page.reload()
  await page.getByText('已连接 MASS 服务', { exact: true }).waitFor()
  await page.setViewportSize({ width: 390, height: 844 })
  await page.reload()
  await page.getByText('已连接 MASS 服务', { exact: true }).waitFor()
  await page.getByRole('heading', { name: '从一个协作空间开始' }).waitFor()
  assert.ok(await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth), 'mobile layout must fit viewport')
  assert.equal(await page.locator('aside').count(), 1)
  await page.screenshot({ path: '.cache/d00-mobile.png', fullPage: true })
  assert.deepEqual(errors, [], 'browser runtime errors')
  console.log('D00 browser smoke passed: hydration, same-origin API, navigation/reload, mobile layout, no page errors.')
} finally { await browser?.close(); await server.stop() }
