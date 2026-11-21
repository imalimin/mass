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
  await page.locator('.app-sidebar').getByRole('link', { name: '模型配置', exact: true }).click()
  await page.getByLabel('模型名称', { exact: true }).fill('运行模型'); await page.getByLabel('Ark 模型 ID').fill('fixture-model'); await page.getByLabel('Base URL', { exact: true }).fill(server.modelBaseURL); await page.getByLabel('API Key', { exact: true }).fill('fixture-provider-key'); await page.getByRole('button', { name: '创建模型', exact: true }).click(); await page.getByRole('button', { name: '编辑 运行模型', exact: true }).waitFor()
  await page.locator('.app-sidebar').getByRole('link', { name: 'Agents', exact: true }).click()
  await page.getByLabel('定义名称').fill('file-assistant'); await page.getByRole('button', { name: '创建 Agent', exact: true }).click(); const editor = page.locator('[data-editor="AGENTS.md"]'); await editor.locator('.view-lines').click(); await page.keyboard.insertText('# File assistant\nRead and write session files.')
  await page.getByLabel('绑定模型').selectOption({ label: '运行模型' })
  const snapshotResponse = page.waitForResponse(r => r.url().endsWith('/debug-snapshots') && r.request().method() === 'POST'); await page.getByRole('button', { name: '创建调试快照', exact: true }).click(); const snapshot = await (await snapshotResponse).json()
  await page.getByRole('link', { name: '使用此版本开始调试', exact: true }).click()
  await expect(page.getByLabel('Agent 版本')).toHaveValue(snapshot.id)
  const agentID = snapshot.definition_id, spaceID = new URL(page.url()).pathname.split('/')[2]
  // Edit the mutable draft in a separate tab while the debug Session stays pinned.
  const draft = await (await context.request.get(`${server.url}/api/v1/spaces/${spaceID}/agents/${agentID}/draft`)).json()
  const updated = await context.request.put(`${server.url}/api/v1/spaces/${spaceID}/agents/${agentID}/draft`, { headers: { Origin: server.url }, data: { expected_revision: draft.revision, document: { ...draft.document, markdown: '# Changed draft must not enter this Session' } } }); assert.equal(updated.status(), 200)
  // Force a mid-message transport close, not a page reload or a Run replay.
  await page.evaluate(() => {
    const Native = window.EventSource; let interrupted = false
    window.EventSource = class extends Native { constructor(url, options) { super(url, options); this.addEventListener('message', e => { if (!interrupted && JSON.parse(e.data).type === 'TEXT_MESSAGE_START') { interrupted = true; this.close(); this.dispatchEvent(new Event('error')) } }) } }
  })
  const subscriptions = []; page.on('request', r => { if (r.url().includes('/events?after=')) subscriptions.push(r.url()) })
  await page.getByLabel('输入任务', { exact: true }).fill('write, read and publish')
  const sessionResponse = page.waitForResponse(r => r.url().endsWith('/sessions') && r.request().method() === 'POST')
  const accepted = page.waitForResponse(r => /\/sessions\/[^/]+\/runs$/.test(r.url()) && r.request().method() === 'POST'); await page.getByRole('button', { name: '发送任务', exact: true }).click(); const run = await (await accepted).json()
  const session = await (await sessionResponse).json(); assert.equal(session.version_id, snapshot.id)
  await expect(page.getByRole('heading', { name: session.id, exact: true })).toBeVisible()
  await expect(page.getByTestId('run-status')).toContainText('已完成', { timeout: 30000 }); await page.getByText('已完成写入、读取和发布 notes.txt', { exact: true }).waitFor()
  await expect(page.locator('.chat-history .assistant')).toHaveCount(1)
  assert.ok(subscriptions.some(url => !url.endsWith('after=0')), 'no resumed UI cursor')
  await page.getByRole('button', { name: '运行详情', exact: true }).click()
  await page.getByText('Trace 与上下文', { exact: true }).click(); await page.getByRole('region', { name: '模型调用统计' }).waitFor(); await expect(page.getByRole('region', { name: '模型调用统计' })).toContainText('冷启动'); await expect(page.getByRole('region', { name: '模型调用统计' })).toContainText('正常追加')
  await page.getByLabel('日志类型').selectOption('context.'); await page.getByText('context.projected', { exact: false }).first().click(); await page.getByRole('button', { name: '检查上下文来源', exact: true }).first().click()
  const inspection = page.getByRole('region', { name: '上下文来源检查' }); await inspection.waitFor(); await expect(inspection).not.toContainText('Changed draft must not enter this Session')
  await expect(inspection.getByText('历史模型请求', { exact: false })).toHaveCount(0)
  const artifact = page.locator('.run-output a').filter({ hasText: 'notes.txt' }).first(), artifactURL = new URL(await artifact.getAttribute('href'), server.url).href; assert.equal((await context.request.get(artifactURL)).status(), 200)
  await mkdir('.cache', { recursive: true }); await page.getByText('Trace 与上下文', { exact: true }).scrollIntoViewIfNeeded(); await page.screenshot({ path: '.cache/d08-desktop.png', fullPage: true }); await page.setViewportSize({ width: 390, height: 844 }); await inspection.scrollIntoViewIfNeeded(); assert.ok(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth)); await page.screenshot({ path: '.cache/d08-mobile.png' })
  const calls = server.modelCalls(); await page.reload(); await page.getByText('已完成写入、读取和发布 notes.txt', { exact: true }).waitFor(); assert.equal(server.modelCalls(), calls)
  // A new space must unmount the old private transcript and all subscriptions.
  await page.getByRole('link', { name: '← Agents', exact: true }).click()
  await page.getByRole('button', { name: '账号设置', exact: true }).click(); await page.getByRole('button', { name: '创建空间', exact: true }).click(); await page.getByLabel('新空间名称').fill('other-space'); await page.getByRole('button', { name: '创建空间', exact: true }).click(); await expect(page.getByRole('heading', { level: 1 })).toHaveText('创意'); await expect(page.getByText('已完成写入、读取和发布 notes.txt', { exact: true })).toHaveCount(0)
  const oldCount = subscriptions.length; await page.waitForTimeout(1200); assert.equal(subscriptions.length, oldCount)
  // Watch a running shared Session as Bob, then revoke access while SSE is open.
  const membership = await context.request.post(`${server.url}/api/v1/spaces/${session.space_id}/members`, { headers: { Origin: server.url }, data: { username: 'bob' } }); assert.equal(membership.status(), 204); const members = await (await context.request.get(`${server.url}/api/v1/spaces/${session.space_id}/members`)).json(); const member = members.items.find(m => m.username === 'bob'); assert.ok(member)
  const bob = await browser.newContext(); const b = await bob.newPage(); await b.goto(`${server.url}/spaces?space=${session.space_id}&session=${session.id}`); await b.getByLabel('用户名', { exact: true }).fill('bob'); await b.getByLabel('密码', { exact: true }).fill('correct-password-bob'); await b.getByRole('button', { name: '登录', exact: true }).click()
  await b.getByLabel('输入任务', { exact: true }).fill('wait for revocation'); await b.getByRole('button', { name: '发送任务', exact: true }).click(); await expect(b.getByTestId('run-status')).toContainText('运行中'); await b.getByText('正在等待取消或重启。', { exact: true }).waitFor()
  const removed = await context.request.delete(`${server.url}/api/v1/spaces/${session.space_id}/members/${member.id}`, { headers: { Origin: server.url, 'Content-Type': 'application/json' } }); assert.equal(removed.status(), 204)
  await b.getByText('空间访问权限已失效，已关闭当前空间。', { exact: false }).waitFor(); await expect(b.getByText('正在等待取消或重启。', { exact: true })).toHaveCount(0)
  assert.equal((await bob.request.get(artifactURL)).status(), 403)
  await bob.close()
  assert.deepEqual(errors, []); console.log('D08 workbench passed: debug snapshot to fixed Session, draft isolation, mid-message SSE reconnect, complete Trace/provenance/metrics, artifact download, reload without execution and space cleanup.')
} catch (e) { await mkdir('.cache', { recursive: true }); await page?.screenshot({ path: '.cache/d08-failure.png', fullPage: true }); throw e }
finally { await browser?.close(); await server.stop() }
