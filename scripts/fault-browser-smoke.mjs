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
  await page.getByLabel('新空间名称').fill('运行验收'); await page.getByRole('button', { name: '创建空间', exact: true }).click()
  await page.locator('.app-sidebar').getByRole('link', { name: '供应商凭证', exact: true }).click()
  await page.getByLabel('凭证名称').fill('运行凭证'); await page.getByLabel('API 密钥', { exact: true }).fill('fixture-provider-key'); await page.getByRole('button', { name: '创建凭证', exact: true }).click(); await page.getByRole('button', { name: '管理 运行凭证', exact: true }).waitFor()
  await page.locator('.app-sidebar').getByRole('link', { name: '模型配置', exact: true }).click()
  await page.getByLabel('模型名称', { exact: true }).fill('运行模型'); await page.getByLabel('Ark 模型 ID').fill('fixture-model'); await page.getByLabel('使用凭证').selectOption({ label: '运行凭证' }); await page.getByRole('button', { name: '创建模型', exact: true }).click(); await page.getByRole('button', { name: '编辑 运行模型', exact: true }).waitFor()
  await page.locator('.app-sidebar').getByRole('link', { name: 'Agents', exact: true }).click()
  await page.getByLabel('定义名称').fill('file-assistant'); await page.getByRole('button', { name: '创建 Agent', exact: true }).click(); const editor = page.locator('[data-editor="AGENTS.md"]'); await editor.locator('.view-lines').click(); await page.keyboard.insertText('# File assistant\nRead and write session files.')
  await page.getByLabel('绑定模型').selectOption({ label: '运行模型' }); await page.getByRole('button', { name: '应用配置选择', exact: true }).click(); const save = page.waitForResponse(r => r.url().endsWith('/draft') && r.request().method() === 'PUT'); await page.getByRole('button', { name: '保存草稿', exact: true }).click(); assert.equal((await save).status(), 200)
  const publishing = page.waitForResponse(r => r.url().endsWith('/versions') && r.request().method() === 'POST'); await page.getByRole('button', { name: '发布版本', exact: true }).click(); assert.equal((await publishing).status(), 201)
  await page.locator('.app-sidebar').getByRole('link', { name: '会话与运行', exact: true }).click(); await page.getByLabel('使用 Agent').selectOption({ label: 'file-assistant' }); await page.getByLabel('会话名称', { exact: true }).fill('文件连续对话'); await page.getByRole('button', { name: '新建会话', exact: true }).click(); await page.getByRole('heading', { name: '文件连续对话', exact: true }).waitFor()
  const space = new URL(page.url()).pathname.split('/')[2], headers = { Origin: server.url, 'Content-Type': 'application/json' }
  const agents = await (await context.request.get(`${server.url}/api/v1/spaces/${space}/agents`)).json(), agent = agents.items.find(a => a.name === 'file-assistant')
  const create = async title => { const response = await context.request.post(`${server.url}/api/v1/spaces/${space}/sessions`, { headers, data: { idempotency_key: crypto.randomUUID(), agent_id: agent.id, title } }); assert.equal(response.status(), 201); return response.json() }
  const submit = async (session, text) => { const response = await context.request.post(`${server.url}/api/v1/spaces/${space}/sessions/${session.id}/runs`, { headers, data: { idempotency_key: crypto.randomUUID(), text } }); assert.equal(response.status(), 202); return response.json() }
  const read = async run => (await context.request.get(`${server.url}/api/v1/spaces/${space}/runs/${run.id}`)).json()
  const wait = async (run, predicate) => { for (let i = 0; i < 600; i++) { const value = await read(run); if (predicate(value)) return value; await new Promise(r => setTimeout(r, 50)) }; throw Error('run did not reach expected state') }
  const trace = async run => { let after = '0', through, result = []; for (;;) { const response = await context.request.get(`${server.url}/api/v1/spaces/${space}/runs/${run.id}/trace?after_sequence=${after}&limit=100${through ? `&through_sequence=${through}` : ''}`); assert.equal(response.status(), 200); const page = await response.json(); through = page.through_sequence; result.push(...page.events); if (!page.has_more) return result; after = page.next_after } }
  const a = await create('crash-a'), b = await create('crash-b'), queuedSession = await create('queued-after-crash'), cancelledSession = await create('cancelled-before-crash')
  const first = await submit(a, 'D09 crash after write'), second = await submit(b, 'D09 crash after write')
  await wait(first, v => v.status === 'running'); await wait(second, v => v.status === 'running')
  for (const run of [first, second]) { for (let i = 0; i < 200; i++) { const events = await trace(run); if (events.some(e => e.event_type === 'tool.execution.completed') && events.some(e => e.event_type === 'model.response.delta')) break; if (i === 199) throw Error('write was not confirmed before crash'); await new Promise(r => setTimeout(r, 25)) } }
  const queued = await submit(queuedSession, 'write, read and publish'), cancelled = await submit(cancelledSession, 'must never execute')
  assert.equal((await read(queued)).status, 'queued'); assert.equal((await read(cancelled)).status, 'queued')
  const cancelResponse = await context.request.post(`${server.url}/api/v1/spaces/${space}/runs/${cancelled.id}/cancel`, { headers }); assert.equal(cancelResponse.status(), 200); assert.equal((await read(cancelled)).status, 'cancelled')
  const callsBefore = server.modelCalls(); assert.equal(callsBefore, 4)
  await server.restart()
  for (const run of [first, second]) { const v = await wait(run, v => v.status === 'failed'); assert.equal(v.error, 'interrupted'); assert.ok(v.snapshot_error); const events = await trace(run); assert.equal(events.filter(e => e.event_type === 'run.finished').length, 1); assert.equal(events.filter(e => e.event_type === 'tool.execution.completed').length, 1) }
  const finished = await wait(queued, v => v.status === 'succeeded'); assert.ok(finished.workspace_snapshot_id); assert.equal((await read(cancelled)).status, 'cancelled'); assert.equal(server.modelCalls(), callsBefore + 4, 'unknown effects were replayed')
  const cancelledTrace = await trace(cancelled); assert.equal(cancelledTrace.filter(e => e.event_type === 'model.attempt.started').length, 0)
  const artifacts = await (await context.request.get(`${server.url}/api/v1/spaces/${space}/runs/${queued.id}/artifacts`)).json(); assert.equal(artifacts.items.length, 1)
  assert.equal(await (await context.request.get(server.url + artifacts.items[0].download_url)).text(), 'hello workspace')
  const other = await (await context.request.post(`${server.url}/api/v1/spaces`, { headers, data: { name: 'isolated-space' } })).json()
  const events = await trace(queued), objectID = events.find(e => e.event_type === 'model.response.completed').data.content_id
  for (const path of [`/runs/${queued.id}`, `/runs/${queued.id}/trace`, `/runs/${queued.id}/metrics`, `/runs/${queued.id}/ui-snapshot`, `/workspace-snapshots/${finished.workspace_snapshot_id}`, `/payloads/${objectID}`, `/artifacts/${artifacts.items[0].id}/download`]) {
    const response = await context.request.get(`${server.url}/api/v1/spaces/${other.id}${path}`); assert.ok([403, 404].includes(response.status()), `cross-space read allowed: ${path}`)
  }
  await page.goto(`${server.url}/spaces?space=${space}&session=${queuedSession.id}&run=${queued.id}`); await page.getByText('已完成写入、读取和发布 notes.txt', { exact: true }).waitFor(); const calls = server.modelCalls(); await page.reload(); await page.getByText('已完成写入、读取和发布 notes.txt', { exact: true }).waitFor(); assert.equal(server.modelCalls(), calls)
  await mkdir('.cache', { recursive: true }); await page.screenshot({ path: '.cache/d09-recovery.png', fullPage: true }); assert.deepEqual(errors, [])
  console.log('D09 process fault passed: two confirmed writes before SIGKILL, interrupted without replay, queued resumes, queued cancellation remains closed, one terminal per run, cross-space endpoints deny, history reload without execution.')
} catch (e) { await mkdir('.cache', { recursive: true }); await page?.screenshot({ path: '.cache/d09-failure.png', fullPage: true }); throw e }
finally { await browser?.close(); await server.stop() }
