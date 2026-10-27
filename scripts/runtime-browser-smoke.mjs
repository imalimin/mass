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
  const context = await browser.newContext({ viewport: { width: 1440, height: 1100 } }); page = await context.newPage(); const errors = []; page.on('pageerror', e => errors.push(e.message))
  await page.goto(server.url); await page.getByLabel('用户名', { exact: true }).fill('alice'); await page.getByLabel('密码', { exact: true }).fill('correct-password-alice'); await page.getByRole('button', { name: '登录', exact: true }).click()
  await page.getByRole('button', { name: '账号设置', exact: true }).click(); await page.getByRole('button', { name: '创建空间', exact: true }).click(); await page.getByLabel('新空间名称').fill('运行验收'); await page.getByRole('button', { name: '创建空间', exact: true }).click()
  await page.locator('.app-sidebar').getByRole('link', { name: '供应商凭证', exact: true }).click()
  await page.getByLabel('凭证名称').fill('运行凭证'); await page.getByLabel('API 密钥', { exact: true }).fill('fixture-provider-key'); await page.getByRole('button', { name: '创建凭证', exact: true }).click(); await page.getByRole('button', { name: '管理 运行凭证', exact: true }).waitFor()
  await page.locator('.app-sidebar').getByRole('link', { name: '模型配置', exact: true }).click()
  await page.getByLabel('模型名称', { exact: true }).fill('运行模型'); await page.getByLabel('Ark 模型 ID').fill('fixture-model'); await page.getByLabel('使用凭证').selectOption({ label: '运行凭证' }); await page.getByRole('button', { name: '创建模型', exact: true }).click(); await page.getByRole('button', { name: '编辑 运行模型', exact: true }).waitFor()
  await page.locator('.app-sidebar').getByRole('link', { name: 'Agents', exact: true }).click()
  await page.getByLabel('定义名称').fill('file-assistant'); await page.getByRole('button', { name: '创建 Agent', exact: true }).click(); const editor = page.locator('[data-editor="AGENTS.md"]'); await editor.locator('.view-lines').click(); await page.keyboard.insertText('# File assistant\nRead and write session files.')
  await page.getByLabel('绑定模型').selectOption({ label: '运行模型' }); await page.getByRole('button', { name: '应用配置选择', exact: true }).click(); const save = page.waitForResponse(r => r.url().endsWith('/draft') && r.request().method() === 'PUT'); await page.getByRole('button', { name: '保存草稿', exact: true }).click(); assert.equal((await save).status(), 200)
  const publishing = page.waitForResponse(r => r.url().endsWith('/versions') && r.request().method() === 'POST'); await page.getByRole('button', { name: '发布版本', exact: true }).click(); assert.equal((await publishing).status(), 201)
  await page.locator('.app-sidebar').getByRole('link', { name: '会话与运行', exact: true }).click(); await page.getByLabel('使用 Agent').selectOption({ label: 'file-assistant' }); await page.getByLabel('会话名称', { exact: true }).fill('文件连续对话'); await page.getByRole('button', { name: '新建会话', exact: true }).click(); await page.getByRole('heading', { name: '文件连续对话', exact: true }).waitFor()
  const send = async text => { await page.getByLabel('输入任务', { exact: true }).fill(text); const accepted = page.waitForResponse(r => /\/sessions\/[^/]+\/runs$/.test(r.url()) && r.request().method() === 'POST'); await page.getByRole('button', { name: '发送任务', exact: true }).click(); const response = await accepted; assert.equal(response.status(), 202, await response.text()); return response.json() }
  const first = await send('write, read and publish'); await expect(page.getByTestId('run-status')).toContainText('已完成', { timeout: 30000 }); await page.getByText('已完成写入、读取和发布 notes.txt', { exact: true }).waitFor()
  const artifactLink = page.locator('.run-output a').filter({ hasText: 'notes.txt' }).first(); await artifactLink.waitFor(); const artifact = await context.request.get(new URL(await artifactLink.getAttribute('href'), server.url).href); assert.equal(artifact.status(), 200); assert.equal(await artifact.text(), 'hello workspace')
  await page.getByText('Trace 与上下文', { exact: true }).click(); await page.getByText('model.request.recorded', { exact: false }).first().waitFor(); await page.getByText('model.response.completed', { exact: false }).first().waitFor()
  await send('second read'); await expect(page.getByTestId('run-status')).toContainText('已完成', { timeout: 30000 }); await page.getByText('已读取已有文件：hello workspace', { exact: true }).waitFor()
  const calls = server.modelCalls(); await page.reload(); await page.getByRole('heading', { name: '文件连续对话', exact: true }).waitFor(); await page.getByText('已读取已有文件：hello workspace', { exact: true }).waitFor(); assert.equal(server.modelCalls(), calls, 'replay called the model')
  await send('wait for cancellation'); await expect(page.getByTestId('run-status')).toContainText('运行中'); await page.getByRole('button', { name: '取消运行', exact: true }).click(); await expect(page.getByTestId('run-status')).toContainText('已取消', { timeout: 30000 })
  await send('wait for restart'); await expect(page.getByTestId('run-status')).toContainText('运行中'); await server.restart(); await expect(page.getByTestId('run-status')).toContainText('失败', { timeout: 30000 }); await expect(page.getByTestId('run-status')).toContainText('interrupted')
  await page.getByRole('button', { name: '恢复最近已提交快照', exact: true }).click(); await expect(page.getByText('工作目录尚未确认持久化，已阻止下一轮运行。', { exact: true })).toHaveCount(0, { timeout: 15000 })
  await send('second read after restart'); await expect(page.getByTestId('run-status')).toContainText('已完成', { timeout: 30000 }); await page.getByText('已读取已有文件：hello workspace', { exact: true }).last().waitFor()
  await mkdir('.cache', { recursive: true }); await page.screenshot({ path: '.cache/d04-desktop.png', fullPage: true }); await page.setViewportSize({ width: 390, height: 844 }); await page.getByTestId('run-status').scrollIntoViewIfNeeded(); await page.evaluate(() => new Promise(r => requestAnimationFrame(() => requestAnimationFrame(r)))); assert.ok(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth), 'mobile overflow'); await page.screenshot({ path: '.cache/d04-mobile.png' })
  const wrong = await context.request.get(`${server.url}/api/v1/spaces/${first.space_id}/runs/${first.id}/events?after=01`); assert.equal(wrong.status(), 400)
  assert.deepEqual(errors, []); console.log('D04 browser flow passed: real Eino/HTTP and TOS protocol fixtures, task/file/artifact/snapshot, continuous conversation, replay without execution, cancellation, SIGKILL recovery and mobile layout.')
} catch (e) { await mkdir('.cache', { recursive: true }); await page?.screenshot({ path: '.cache/d04-failure.png', fullPage: true }); throw e }
finally { await browser?.close(); await server.stop() }
