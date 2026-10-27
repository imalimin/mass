import assert from 'node:assert/strict'
import { mkdir } from 'node:fs/promises'
import { createRequire } from 'node:module'
import { resolve } from 'node:path'
import { startFixtureServer } from './fixture-server.mjs'

process.env.PLAYWRIGHT_BROWSERS_PATH ??= resolve('.cache/playwright')
const require = createRequire(new URL('../frontend/package.json', import.meta.url))
const { chromium, expect } = require('@playwright/test')
const server = await startFixtureServer()
let browser
try {
  browser = await chromium.launch({ headless: true, channel: 'chromium' })
  const alice = await browser.newContext({ viewport: { width: 1440, height: 1100 } })
  const bob = await browser.newContext({ viewport: { width: 1440, height: 1100 } })
  const a = await alice.newPage(), b = await bob.newPage(), errors = []
  for (const page of [a, b]) page.on('pageerror', error => errors.push(error.message))
  const login = async (page, name) => {
    await page.goto(server.url); await page.getByLabel('用户名', { exact: true }).fill(name)
    await page.getByLabel('密码', { exact: true }).fill(`correct-password-${name}`)
    await page.getByRole('button', { name: '登录', exact: true }).click(); await page.getByText('已连接 MASS 服务', { exact: true }).waitFor()
  }
  await login(a, 'alice'); await login(b, 'bob')
  const nav = (page, name) => page.locator('.app-sidebar').getByRole('link', { name, exact: true })
  const createSpace = async (page, name) => { await page.getByRole('button', { name: '账号设置', exact: true }).click(); await page.getByRole('button', { name: '创建空间', exact: true }).click(); await page.getByLabel('新空间名称').fill(name); await page.getByRole('button', { name: '创建空间', exact: true }).click(); await page.getByRole('heading', { name, exact: true }).waitFor(); await nav(page, '空间成员').click() }
  await createSpace(a, '研究空间'); await createSpace(b, '独立空间')
  await expect(a.getByText('alice', { exact: false }).filter({ hasText: '不可移除' })).toBeVisible()
  await expect(a.getByRole('button', { name: '移除 alice', exact: true })).toHaveCount(0)
  await a.getByLabel('已有账号用户名').fill('bob'); await a.getByRole('button', { name: '添加成员', exact: true }).click()
  await a.getByRole('button', { name: '移除 bob', exact: true }).waitFor()
  await b.getByLabel('切换空间').selectOption({ label: '研究空间' }); await nav(b, '空间成员').click()
  await b.getByRole('heading', { name: '空间成员', level: 1 }).waitFor()
  await expect(b.getByLabel('已有账号用户名')).toHaveCount(0)
  await expect(b.getByRole('heading', { name: '供应商凭证', exact: true })).toHaveCount(0)
  await nav(a, '供应商凭证').click()
  await a.getByLabel('凭证名称').fill('测试 Ark 密钥'); await a.getByLabel('API 密钥', { exact: true }).fill('fixture-provider-key')
  await a.getByRole('button', { name: '创建凭证', exact: true }).click(); await a.getByRole('button', { name: '管理 测试 Ark 密钥', exact: true }).waitFor()
  await expect(a.getByLabel('API 密钥', { exact: true })).toHaveValue('')
  await nav(a, '模型配置').click()
  await a.getByLabel('模型名称').fill('协作模型'); await a.getByLabel('Ark 模型 ID').fill('ep-test')
  await a.getByLabel('使用凭证').selectOption({ label: '测试 Ark 密钥' }); await a.getByRole('button', { name: '创建模型', exact: true }).click()
  await a.getByRole('button', { name: '编辑 协作模型', exact: true }).waitFor()
  await nav(b, '模型配置').click(); await b.getByText('协作模型', { exact: false }).waitFor(); await nav(b, '空间成员').click()
  await a.getByRole('button', { name: '编辑 协作模型', exact: true }).click(); await a.getByLabel('模型名称').fill('协作模型 v2'); await a.getByRole('button', { name: '保存模型', exact: true }).click()
  await a.getByRole('button', { name: '编辑 协作模型 v2', exact: true }).waitFor()
  await mkdir('.cache', { recursive: true }); await a.screenshot({ path: '.cache/d02-desktop.png', fullPage: true })
  await nav(a, '空间成员').click(); await a.getByRole('button', { name: '移除 bob', exact: true }).click(); await expect(a.getByRole('button', { name: '移除 bob', exact: true })).toHaveCount(0)
  await b.getByText('空间访问权限已失效，已关闭当前空间。', { exact: false }).waitFor({ timeout: 15000 })
  await expect(b.getByText('协作模型', { exact: false })).toHaveCount(0)
  await expect(b.getByRole('link', { name: '研究空间', exact: false })).toHaveCount(0)
  await nav(a, '供应商凭证').click(); await a.getByRole('button', { name: '管理 测试 Ark 密钥', exact: true }).click(); await a.getByLabel('启用凭证').uncheck(); await a.getByRole('button', { name: '保存凭证', exact: true }).click()
  await expect(a.getByText('已禁用', { exact: true })).toBeVisible()
  await a.setViewportSize({ width: 390, height: 844 }); await a.reload(); await nav(a, '模型配置').click()
  await a.getByRole('button', { name: '编辑 协作模型 v2', exact: true }).waitFor()
  assert.ok(await a.evaluate(() => document.documentElement.scrollWidth <= innerWidth), 'mobile layout overflow')
  await a.screenshot({ path: '.cache/d02-mobile.png', fullPage: true })
  await a.getByRole('button', { name: '账号设置', exact: true }).click(); await a.getByRole('button', { name: '退出登录', exact: true }).click(); await a.getByRole('heading', { name: '登录 MASS' }).waitFor()
  await a.reload(); await a.getByRole('heading', { name: '登录 MASS' }).waitFor()
  const response = await alice.request.get(server.url + '/api/v1/spaces'); assert.equal(response.status(), 401)
  assert.deepEqual(errors, [])
  console.log('D02 browser flow passed: two accounts/spaces, membership, encrypted credential/model forms, revocation cleanup, logout and mobile layout.')
} finally { await browser?.close(); await server.stop() }
