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
  await page.getByLabel('新空间名称').fill('工具验收'); await page.getByRole('button', { name: '创建空间', exact: true }).click()
  await page.getByLabel('凭证名称').fill('工具凭证'); await page.getByLabel('API 密钥', { exact: true }).fill('fixture-provider-key'); await page.getByRole('button', { name: '创建凭证', exact: true }).click(); await page.getByRole('button', { name: '管理 工具凭证', exact: true }).waitFor()
  await page.getByLabel('模型名称', { exact: true }).fill('工具模型'); await page.getByLabel('Ark 模型 ID').fill('fixture-model'); await page.getByLabel('上下文容量').fill('262144'); await page.getByLabel('使用凭证').selectOption({ label: '工具凭证' }); await page.getByRole('button', { name: '创建模型', exact: true }).click(); await page.getByRole('button', { name: '编辑 工具模型', exact: true }).waitFor()
  await page.getByRole('button', { name: 'Agent 与 Skill', exact: true }).click()
  const edit = async (label, value) => { const root = page.locator(`[data-editor="${label}"]`); await root.locator('.monaco-editor').waitFor(); await root.locator('.view-lines').click(); await page.keyboard.press(process.platform === 'darwin' ? 'Meta+A' : 'Control+A'); await page.keyboard.insertText(value) }
  const save = async () => { const response = page.waitForResponse(r => r.url().endsWith('/draft') && r.request().method() === 'PUT'); await page.getByRole('button', { name: '保存草稿', exact: true }).click(); const r = await response; assert.equal(r.status(), 200, `${await r.text()} ${r.request().postDataJSON().document.config_yaml}`) }
  const publish = async () => { const response = page.waitForResponse(r => r.url().endsWith('/versions') && r.request().method() === 'POST'); await page.getByRole('button', { name: '发布版本', exact: true }).click(); const r = await response; assert.equal(r.status(), 201, await r.text()); return r.json() }
  await page.getByLabel('定义名称').fill('rules'); await page.getByRole('button', { name: '创建 Skill', exact: true }).click(); await edit('SKILL.md', '# Rules\nRead the multiplier from assets/rules.txt.'); await save()
  await page.getByLabel('资源文件（最多 8 MiB）').setInputFiles({ name: 'rules.txt', mimeType: 'text/plain', buffer: Buffer.from('2') })
  const uploaded = page.waitForResponse(r => r.url().endsWith('/resources')); await page.getByRole('button', { name: '上传资源', exact: true }).click(); assert.equal((await uploaded).status(), 200); await publish()
  await page.getByRole('button', { name: 'Agents', exact: true }).click(); await page.getByLabel('定义名称').fill('tools-assistant'); await page.getByRole('button', { name: '创建 Agent', exact: true }).click(); await edit('AGENTS.md', '# Tools assistant\nProcess the accepted attachment with the selected tools.')
  await page.getByLabel('绑定模型').selectOption({ label: '工具模型' }); await page.getByLabel('Skill 版本：rules', { exact: true }).selectOption('v1')
  const checks = page.locator('.tool-options input'); assert.equal(await checks.count(), 14); for (const check of await checks.all()) await check.check()
  await page.getByLabel('输入 Token 上限').fill('100000'); await page.getByRole('button', { name: '应用配置选择', exact: true }).click(); await save(); const agentVersion = await publish()
  await page.getByRole('button', { name: '会话与运行', exact: true }).click(); await page.getByLabel('使用 Agent').selectOption({ label: 'tools-assistant' }); await page.getByLabel('定义版本').selectOption(agentVersion.id); await page.getByLabel('会话名称', { exact: true }).fill('十四工具组合'); await page.getByRole('button', { name: '新建会话', exact: true }).click(); await page.getByRole('heading', { name: '十四工具组合', exact: true }).waitFor()
  const attachmentUpload = page.waitForResponse(r => r.url().endsWith('/attachments') && r.request().method() === 'POST')
  await page.getByLabel('添加任务附件').setInputFiles({ name: 'records.json', mimeType: 'application/json', buffer: Buffer.from('[{"amount":2},{"amount":3}]') }); assert.equal((await attachmentUpload).status(), 201); await page.getByLabel('移除附件 records.json').waitFor()
  await page.evaluate(() => {
    const Native = window.EventSource; window.__massToolInterrupted = false
    window.EventSource = class extends Native { constructor(url, options) { super(url, options); this.addEventListener('message', e => { if (!window.__massToolInterrupted && JSON.parse(e.data).type === 'TOOL_CALL_ARGS') { window.__massToolInterrupted = true; this.close(); this.dispatchEvent(new Event('error')) } }) } }
  })
  await page.getByLabel('输入任务', { exact: true }).fill('D05 process attachment using all tools')
  const accepted = page.waitForResponse(r => /\/sessions\/[^/]+\/runs$/.test(r.url()) && r.request().method() === 'POST'); await page.getByRole('button', { name: '发送任务', exact: true }).click(); const response = await accepted; assert.equal(response.status(), 202, await response.text()); const run = await response.json()
  await expect(page.getByTestId('run-status')).toContainText('已完成', { timeout: 60000 }); await page.getByText('十四工具处理完成，汇总结果为 10。', { exact: true }).waitFor()
  await expect(page.getByRole('complementary', { name: '当前运行计划' })).toContainText('修订 2'); await expect(page.getByRole('complementary', { name: '当前运行计划' })).toContainText('已完成 · Create summary')
  assert.ok(await page.evaluate(() => window.__massToolInterrupted), 'tool parameter stream was not interrupted')
  const names = await page.locator('.run-output > details:not(.trace) > summary').allTextContents(); assert.equal(new Set(names.map(n => n.split(' · ')[0])).size, 14)
  const artifactLink = page.locator('.run-output a').filter({ hasText: 'summary.json' }).first(); const artifact = await context.request.get(new URL(await artifactLink.getAttribute('href'), server.url).href); assert.equal(artifact.status(), 200); assert.equal(await artifact.text(), '{"total":10,"label":"final"}')
  const attachmentLink = page.locator('.chat-history a').filter({ hasText: 'records.json' }).first(); await attachmentLink.waitFor(); const attachment = await context.request.get(new URL(await attachmentLink.getAttribute('href'), server.url).href); assert.equal(attachment.status(), 200); assert.equal(await attachment.text(), '[{"amount":2},{"amount":3}]')
  const base = `${server.url}/api/v1/spaces/${run.space_id}/runs/${run.id}`
  const plan = await context.request.get(`${base}/plan`); assert.equal((await plan.json()).revision, 2)
  let after = '0', ioCount = 0, inputResolved = false
  for (;;) { const response = await context.request.get(`${base}/trace?after_sequence=${after}&limit=100`); assert.equal(response.status(), 200); const trace = await response.json(); ioCount += trace.events.filter(e => e.event_type === 'tool.io.recorded').length; inputResolved ||= trace.events.some(e => e.event_type === 'tool.execution.input_resolved'); if (!trace.has_more) break; after = trace.next_after }
  assert.ok(inputResolved && ioCount >= 14, 'missing durable code IO')
  const calls = server.modelCalls(); assert.equal(calls, 16); await page.reload(); await page.getByText('十四工具处理完成，汇总结果为 10。', { exact: true }).waitFor(); await expect(page.getByRole('complementary', { name: '当前运行计划' })).toContainText('修订 2'); assert.equal(server.modelCalls(), calls, 'replay executed tools/model')
  await page.getByText('Trace 与上下文', { exact: true }).click(); await page.getByText('model.request.recorded', { exact: false }).first().waitFor()
  await mkdir('.cache', { recursive: true }); await page.screenshot({ path: '.cache/d05-desktop.png', fullPage: true }); await page.setViewportSize({ width: 390, height: 844 }); await page.getByRole('complementary', { name: '当前运行计划' }).scrollIntoViewIfNeeded(); await page.evaluate(() => new Promise(r => requestAnimationFrame(() => requestAnimationFrame(r)))); assert.ok(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth), 'mobile overflow'); await page.screenshot({ path: '.cache/d05-mobile.png' })
  assert.deepEqual(errors, []); console.log('D05 browser passed: all 14 tools through actual Eino/HTTP, accepted attachment and fixed Skill, JS file/network/CSV IO, plan, immutable artifact after deletion, replay without execution and mobile layout.')
} catch (e) { await mkdir('.cache', { recursive: true }); await page?.screenshot({ path: '.cache/d05-failure.png', fullPage: true }); throw e }
finally { await browser?.close(); await server.stop() }
