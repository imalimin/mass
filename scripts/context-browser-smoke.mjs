import assert from 'node:assert/strict'
import { mkdir } from 'node:fs/promises'
import { createRequire } from 'node:module'
import { resolve } from 'node:path'
import { startFixtureServer } from './fixture-server.mjs'
process.env.PLAYWRIGHT_BROWSERS_PATH ??= resolve('.cache/playwright')
const require = createRequire(new URL('../frontend/package.json', import.meta.url)), { chromium, expect } = require('@playwright/test')
const server = await startFixtureServer({ storage: true, model: true })
let browser, page
try {
  browser = await chromium.launch({ headless: true, channel: 'chromium' })
  const context = await browser.newContext({ viewport: { width: 1440, height: 1100 } }); page = await context.newPage()
  const errors = []; page.on('pageerror', e => errors.push(e.message))
  await page.goto(server.url); await page.getByLabel('用户名', { exact: true }).fill('alice'); await page.getByLabel('密码', { exact: true }).fill('correct-password-alice'); await page.getByRole('button', { name: '登录', exact: true }).click()
  await page.getByRole('button', { name: '账号设置', exact: true }).click(); await page.getByRole('button', { name: '创建空间', exact: true }).click(); await page.getByLabel('新空间名称').fill('上下文验收'); await page.getByRole('button', { name: '创建空间', exact: true }).click()
  await page.locator('.app-sidebar').getByRole('link', { name: '供应商凭证', exact: true }).click()
  await page.getByLabel('凭证名称').fill('测试凭证'); await page.getByLabel('API 密钥', { exact: true }).fill('fixture-provider-key'); await page.getByRole('button', { name: '创建凭证', exact: true }).click(); await page.getByRole('button', { name: '管理 测试凭证', exact: true }).waitFor()
  await page.locator('.app-sidebar').getByRole('link', { name: '模型配置', exact: true }).click()
  await page.getByLabel('模型名称', { exact: true }).fill('测试模型'); await page.getByLabel('Ark 模型 ID').fill('fixture-model'); await page.getByLabel('上下文容量').fill('16384'); await page.getByLabel('使用凭证').selectOption({ label: '测试凭证' }); await page.getByRole('button', { name: '创建模型', exact: true }).click(); await page.getByRole('button', { name: '编辑 测试模型', exact: true }).waitFor()
  await page.locator('.app-sidebar').getByRole('link', { name: 'Skills', exact: true }).click()
  await page.locator('.app-sidebar').getByRole('link', { name: 'Agents', exact: true }).click(); await page.getByLabel('定义名称').fill('context-assistant'); await page.getByRole('button', { name: '创建 Agent', exact: true }).click()
  const editor = page.locator('[data-editor="AGENTS.md"]'); await editor.locator('.monaco-editor').waitFor(); await editor.locator('.view-lines').click(); await page.keyboard.press(process.platform === 'darwin' ? 'Meta+A' : 'Control+A'); await page.keyboard.insertText('# Assistant\nPreserve task history.')
  await page.getByLabel('绑定模型').selectOption({ label: '测试模型' }); for (const check of await page.locator('.tool-options input').all()) await check.uncheck()
  await page.getByLabel('输入 Token 上限').fill('8000'); await page.getByRole('button', { name: '应用配置选择', exact: true }).click()
  let response = page.waitForResponse(r => r.url().endsWith('/draft') && r.request().method() === 'PUT'); await page.getByRole('button', { name: '保存草稿', exact: true }).click(); assert.equal((await response).status(), 200)
  response = page.waitForResponse(r => r.url().endsWith('/versions') && r.request().method() === 'POST'); await page.getByRole('button', { name: '发布版本', exact: true }).click(); const version = await (await response).json()
  await page.locator('.app-sidebar').getByRole('link', { name: '会话与运行', exact: true }).click(); await page.getByLabel('使用 Agent').selectOption({ label: 'context-assistant' }); await page.getByLabel('定义版本').selectOption(version.id); await page.getByLabel('会话名称', { exact: true }).fill('阶段摘要'); await page.getByRole('button', { name: '新建会话', exact: true }).click(); await page.getByRole('heading', { name: '阶段摘要', exact: true }).waitFor()
  const send = async text => {
    await page.getByLabel('输入任务', { exact: true }).fill(text)
    const response = page.waitForResponse(r => /\/sessions\/[^/]+\/runs$/.test(r.url()) && r.request().method() === 'POST')
    await page.getByRole('button', { name: '发送任务', exact: true }).click(); const r = await response; assert.equal(r.status(), 202); const run = await r.json()
    await expect(page.getByTestId('run-status')).toContainText('已完成', { timeout: 30000 }); return run
  }
  const summaries = () => server.modelRequests().filter(r => r.messages[0].content.startsWith('Summarize this historical transcript'))
  let run
  for (let i = 0; i < 6 && summaries().length === 0; i++) run = await send('D06 long ' + 'user fact. '.repeat(60))
  assert.equal(summaries().length, 1)
  run = await send('D06 continue briefly'); run = await send('D06 continue briefly again'); assert.equal(summaries().length, 1)
  await expect(page.locator('.chat-history')).not.toContainText('D06 saved history:'); await expect(page.locator('.run-output')).not.toContainText('D06 saved history:')
  const requests = server.modelRequests().filter(r => !r.messages[0].content.startsWith('Summarize this historical transcript'))
  for (let i = 1; i < requests.length; i++) {
    const a = requests[i - 1].messages, b = requests[i].messages
    if (a.some(m => m.content?.startsWith('[MASS saved history summary')) === b.some(m => m.content?.startsWith('[MASS saved history summary'))) assert.deepEqual(b.slice(0, a.length), a, 'wire prefix changed')
  }
  const base = `${server.url}/api/v1/spaces/${run.space_id}/runs/${run.id}`
  const contexts = await (await context.request.get(`${base}/contexts?limit=100`)).json(); const projected = contexts.events.find(e => e.event_type === 'context.projected')
  const inspection = await (await context.request.get(`${base}/contexts/${projected.data.id}`)).json(); assert.equal(inspection.projection.spec.context_epoch, 2); assert.equal(inspection.requests.length, 1); assert.ok(inspection.source_events.length > 0)
  await page.getByText('Trace 与上下文', { exact: true }).click(); await page.getByText('context.projected', { exact: false }).first().click(); await page.getByRole('button', { name: '检查上下文来源', exact: true }).click()
  await expect(page.getByRole('region', { name: '上下文来源检查' })).toContainText('Epoch 2')
  await page.getByText('实际模型请求 · 1', { exact: true }).click(); await page.getByRole('button', { name: /查看实际请求/ }).click(); await expect(page.locator('.payload-view')).toContainText('messages')
  await page.setViewportSize({ width: 390, height: 844 }); await page.getByRole('region', { name: '上下文来源检查' }).scrollIntoViewIfNeeded(); assert.ok(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth)); await mkdir('.cache', { recursive: true }); await page.screenshot({ path: '.cache/d06-inspection-mobile.png' }); await page.setViewportSize({ width: 1440, height: 1100 });
  const calls = server.modelCalls(); await page.reload(); await page.getByRole('heading', { name: '阶段摘要', exact: true }).waitFor(); assert.equal(server.modelCalls(), calls)
  await mkdir('.cache', { recursive: true }); await page.screenshot({ path: '.cache/d06-desktop.png', fullPage: true }); await page.setViewportSize({ width: 390, height: 844 }); assert.ok(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth)); await page.screenshot({ path: '.cache/d06-mobile.png' })
  assert.deepEqual(errors, []); console.log('D06 browser passed: actual Eino HTTP serialization, one summary reused across turns, stable wire prefixes, source inspection, no summary chat pollution, reload without calls, mobile layout.')
} catch (e) { await mkdir('.cache', { recursive: true }); await page?.screenshot({ path: '.cache/d06-failure.png', fullPage: true }); throw e }
finally { await browser?.close(); await server.stop() }
