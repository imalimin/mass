import assert from 'node:assert/strict'
import { mkdir } from 'node:fs/promises'
import { createRequire } from 'node:module'
import { resolve } from 'node:path'
import { startFixtureServer } from './fixture-server.mjs'
process.env.PLAYWRIGHT_BROWSERS_PATH ??= resolve('.cache/playwright')
const require = createRequire(new URL('../frontend/package.json', import.meta.url)), { chromium, expect } = require('@playwright/test')
const server = await startFixtureServer({ storage: true, model: true, commandPath: '/usr/bin:/bin' })
let browser, page
try {
  browser = await chromium.launch({ headless: true, channel: 'chromium' })
  const context = await browser.newContext({ viewport: { width: 1440, height: 1100 } }); page = await context.newPage()
  const errors = []; page.on('pageerror', e => errors.push(e.message))
  await page.goto(server.url); await page.getByLabel('用户名', { exact: true }).fill('alice'); await page.getByLabel('密码', { exact: true }).fill('correct-password-alice'); await page.getByRole('button', { name: '登录', exact: true }).click()
  await page.getByRole('button', { name: '账号设置', exact: true }).click(); await page.getByRole('button', { name: '创建空间', exact: true }).click(); await page.getByLabel('新空间名称').fill('工具验收'); await page.getByRole('button', { name: '创建空间', exact: true }).click()
  await page.locator('.app-sidebar').getByRole('link', { name: '模型配置', exact: true }).click()
  await page.getByLabel('模型名称', { exact: true }).fill('工具模型'); await page.getByLabel('Ark 模型 ID').fill('fixture-model'); await page.getByLabel('上下文容量').fill('262144'); await page.getByLabel('Base URL', { exact: true }).fill(server.modelBaseURL); await page.getByLabel('API Key', { exact: true }).fill('fixture-provider-key'); await page.getByRole('button', { name: '创建模型', exact: true }).click(); await page.getByRole('button', { name: '编辑 工具模型', exact: true }).waitFor()
  await page.locator('.app-sidebar').getByRole('link', { name: 'Skills', exact: true }).click()
  const edit = async (label, value) => { const root = page.locator(`[data-editor="${label}"]`); await root.locator('.monaco-editor').waitFor(); await root.locator('.view-lines').click(); await page.keyboard.press(process.platform === 'darwin' ? 'Meta+A' : 'Control+A'); await page.keyboard.insertText(value) }
  const save = async () => { const response = page.waitForResponse(r => r.url().endsWith('/draft') && r.request().method() === 'PUT'); await page.getByRole('button', { name: '保存草稿', exact: true }).click(); const r = await response; assert.equal(r.status(), 200, `${await r.text()} ${r.request().postDataJSON().document.config_yaml}`) }
  const publish = async () => { const response = page.waitForResponse(r => r.url().endsWith('/versions') && r.request().method() === 'POST'); await page.getByRole('button', { name: '发布版本', exact: true }).click(); const r = await response; assert.equal(r.status(), 201, await r.text()); return r.json() }
  await page.getByRole('button', { name: '创建 Skill', exact: true }).click()
  const createSkill = page.getByRole('dialog', { name: '创建 Skill' })
  await createSkill.getByLabel('名称', { exact: true }).fill('rules')
  await createSkill.getByRole('button', { name: '创建', exact: true }).click(); await edit('SKILL.md', '---\nname: rules\ndescription: Multiplier rules.\n---\n# Rules\nRead the multiplier from rules.txt.'); await save()
  await page.getByText('添加资源', { exact: true }).click()
  await page.getByLabel('资源文件（最多 8 MiB）').setInputFiles({ name: 'rules.txt', mimeType: 'text/plain', buffer: Buffer.from('2') })
  await expect(page.getByLabel('资源路径')).toHaveValue('rules.txt')
  const uploaded = page.waitForResponse(r => r.url().endsWith('/resources')); await page.getByRole('button', { name: '上传资源', exact: true }).click(); assert.equal((await uploaded).status(), 200); await publish()
  await page.locator('.app-sidebar').getByRole('link', { name: 'Agents', exact: true }).click(); await page.getByRole('button', { name: '创建 Agent', exact: true }).click()
  const createAgent = page.getByRole('dialog', { name: '创建 Agent' })
  await createAgent.getByLabel('名称', { exact: true }).fill('tools-assistant')
  await createAgent.getByRole('button', { name: '创建', exact: true }).click(); await edit('AGENTS.md', '# Tools assistant\nProcess the accepted attachment with the selected tools.')
  await page.getByLabel('绑定模型').selectOption({ label: '工具模型' }); await page.getByLabel('Skill 版本：rules', { exact: true }).selectOption('v1')
  const checks = page.locator('.tool-options input'); assert.equal(await checks.count(), 7); for (const check of await checks.all()) await check.check()
  await page.getByLabel('输入 Token 上限').fill('100000'); const agentVersion = await publish()
  await page.locator('.app-sidebar').getByRole('link', { name: 'Agents', exact: true }).click(); await page.getByRole('link', { name: '打开 tools-assistant 的会话', exact: true }).click()
  await page.getByLabel('Agent 版本').selectOption(agentVersion.id)
  await page.getByLabel('添加任务附件').setInputFiles({ name: 'records.json', mimeType: 'application/json', buffer: Buffer.from('[{"amount":2},{"amount":3}]') }); await page.getByLabel('移除附件 records.json').waitFor()
  await page.evaluate(() => {
    const Native = window.EventSource; window.__massToolInterrupted = false
    window.EventSource = class extends Native { constructor(url, options) { super(url, options); this.addEventListener('message', e => { if (!window.__massToolInterrupted && JSON.parse(e.data).type === 'TOOL_CALL_ARGS') { window.__massToolInterrupted = true; this.close(); this.dispatchEvent(new Event('error')) } }) } }
  })
  await page.getByLabel('输入任务', { exact: true }).fill('D05 process attachment using all tools')
  const accepted = page.waitForResponse(r => /\/sessions\/[^/]+\/runs$/.test(r.url()) && r.request().method() === 'POST'); await page.getByRole('button', { name: '发送任务', exact: true }).click(); const response = await accepted; assert.equal(response.status(), 202, await response.text()); const run = await response.json()
  await expect(page.getByTestId('run-status')).toContainText('已完成', { timeout: 60000 }); await page.getByText('核心工具处理完成，汇总结果为 10。', { exact: true }).waitFor()
  await expect(page.getByRole('complementary', { name: '当前运行计划' })).toContainText('修订 2'); await expect(page.getByRole('complementary', { name: '当前运行计划' })).toContainText('已完成 · Create summary')
  assert.ok(await page.evaluate(() => window.__massToolInterrupted), 'tool parameter stream was not interrupted')
  await page.getByText(/^工具调用 · /).click()
  const names = await page.locator('.tool-activity > details > summary').allTextContents(); assert.equal(new Set(names.map(n => n.split(' · ')[0])).size, 7)
  const artifactLink = page.locator('.run-output a').filter({ hasText: 'summary.json' }).first(); const artifact = await context.request.get(new URL(await artifactLink.getAttribute('href'), server.url).href); assert.equal(artifact.status(), 200); assert.equal(await artifact.text(), '{"total":10,"label":"final"}')
  const attachmentLink = page.locator('.chat-history a').filter({ hasText: 'records.json' }).first(); await attachmentLink.waitFor(); const attachment = await context.request.get(new URL(await attachmentLink.getAttribute('href'), server.url).href); assert.equal(attachment.status(), 200); assert.equal(await attachment.text(), '[{"amount":2},{"amount":3}]')
  const base = `${server.url}/api/v1/spaces/${run.space_id}/runs/${run.id}`
  const plan = await context.request.get(`${base}/plan`); assert.equal((await plan.json()).revision, 2)
  let after = '0', ioCount = 0, inputResolved = false
  for (;;) { const response = await context.request.get(`${base}/trace?after_sequence=${after}&limit=100`); assert.equal(response.status(), 200); const trace = await response.json(); ioCount += trace.events.filter(e => e.event_type === 'tool.io.recorded').length; inputResolved ||= trace.events.some(e => e.event_type === 'tool.execution.input_resolved'); if (!trace.has_more) break; after = trace.next_after }
  assert.ok(!inputResolved && ioCount === 0, 'code IO was persisted')
  const calls = server.modelCalls(); assert.equal(calls, 12); await page.reload(); await page.getByText('核心工具处理完成，汇总结果为 10。', { exact: true }).waitFor(); await expect(page.getByRole('complementary', { name: '当前运行计划' })).toContainText('修订 2'); assert.equal(server.modelCalls(), calls, 'replay executed tools/model')
  await page.getByRole('button', { name: '运行详情', exact: true }).click()
  await page.getByText('Trace 与上下文', { exact: true }).click(); await page.getByText('model.attempt.started', { exact: false }).first().waitFor()
  await page.keyboard.press('Escape')
  await mkdir('.cache', { recursive: true }); await page.screenshot({ path: '.cache/d05-desktop.png', fullPage: true }); await page.setViewportSize({ width: 390, height: 844 }); await page.getByRole('complementary', { name: '当前运行计划' }).scrollIntoViewIfNeeded(); await page.evaluate(() => new Promise(r => requestAnimationFrame(() => requestAnimationFrame(r)))); assert.ok(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth), 'mobile overflow'); await page.screenshot({ path: '.cache/d05-mobile.png' })
  if (process.platform === 'darwin') {
    await page.getByLabel('输入任务', { exact: true }).fill('run a host command')
    const starting = page.waitForResponse(r => /\/sessions\/[^/]+\/runs$/.test(r.url()) && r.request().method() === 'POST')
    await page.getByRole('button', { name: '发送任务', exact: true }).click()
    const runResponse = await starting; assert.equal(runResponse.status(), 202); const commandRun = await runResponse.json()
    const commandRunURL = `${server.url}/api/v1/spaces/${run.space_id}/runs/${commandRun.id}`
    await expect.poll(async () => (await (await context.request.get(commandRunURL)).json()).status).toMatch(/^(succeeded|failed|cancelled)$/)
    const completed = await (await context.request.get(commandRunURL)).json(); assert.equal(completed.status, 'succeeded', JSON.stringify(completed.failure_detail))
    await page.getByText('已运行宿主命令并发布 command.txt', { exact: true }).waitFor()
    const report = page.locator('.run-output a').filter({ hasText: 'command.txt' }).first(); await report.waitFor()
    const file = await context.request.get(new URL(await report.getAttribute('href'), server.url).href); assert.equal(file.status(), 200); assert.equal(await file.text(), 'hello workspace')
    const trace = await context.request.get(`${server.url}/api/v1/diagnostics?run_id=${commandRun.id}`)
    assert.equal(trace.status(), 200)
    const commands = (await trace.json()).items.flatMap(record => record.tools ?? []).filter(tool => tool.name === 'run_command')
    assert.deepEqual(commands.map(tool => [tool.status, tool.result.data.exit_code]), [['failed', 7], ['completed', 0]])
    assert.equal(commands[0].result.data.stderr, 'problem')
    console.log('Host command browser flow passed: Agent opt-in, failure details, workspace artifact and session JSONL.')
  }
  assert.deepEqual(errors, []); console.log('D05 browser passed: all 7 core tools through actual Eino/HTTP, accepted attachment and fixed Skill, shell command IO, plan, immutable artifact after deletion, replay without execution and mobile layout.')
} catch (e) { await mkdir('.cache', { recursive: true }); await page?.screenshot({ path: '.cache/d05-failure.png', fullPage: true }); throw e }
finally { await browser?.close(); await server.stop() }
