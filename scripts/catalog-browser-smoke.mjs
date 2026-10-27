import assert from 'node:assert/strict'
import { mkdir } from 'node:fs/promises'
import { createRequire } from 'node:module'
import { resolve } from 'node:path'
import { startFixtureServer } from './fixture-server.mjs'
process.env.PLAYWRIGHT_BROWSERS_PATH ??= resolve('.cache/playwright')
const require = createRequire(new URL('../frontend/package.json', import.meta.url)), { chromium, expect } = require('@playwright/test')
const server = await startFixtureServer({ storage: true })
let browser, page
try {
  browser = await chromium.launch({ headless: true, channel: 'chromium' })
  const context = await browser.newContext({ viewport: { width: 1440, height: 1100 } })
  page = await context.newPage(); const errors = []; page.on('pageerror', e => errors.push(e.message))
  await page.goto(server.url); await page.getByLabel('用户名', { exact: true }).fill('alice'); await page.getByLabel('密码', { exact: true }).fill('correct-password-alice'); await page.getByRole('button', { name: '登录', exact: true }).click()
  await page.getByRole('button', { name: '账号设置', exact: true }).click(); await page.getByRole('button', { name: '创建空间', exact: true }).click(); await page.getByLabel('新空间名称').fill('定义验收'); await page.getByRole('button', { name: '创建空间', exact: true }).click()
  await page.locator('.app-sidebar').getByRole('link', { name: '供应商凭证', exact: true }).click()
  await page.getByLabel('凭证名称').fill('测试凭证'); await page.getByLabel('API 密钥', { exact: true }).fill('fixture-provider-key'); await page.getByRole('button', { name: '创建凭证', exact: true }).click()
  await page.getByRole('button', { name: '管理 测试凭证', exact: true }).waitFor()
  await page.locator('.app-sidebar').getByRole('link', { name: '模型配置', exact: true }).click()
  await page.getByLabel('模型名称', { exact: true }).fill('发布模型'); await page.getByLabel('Ark 模型 ID').fill('ep-fixture'); await page.getByLabel('使用凭证').selectOption({ label: '测试凭证' }); await page.getByRole('button', { name: '创建模型', exact: true }).click()
  await page.getByRole('button', { name: '编辑 发布模型', exact: true }).waitFor()
  await page.locator('.app-sidebar').getByRole('link', { name: 'Skills', exact: true }).click()
  const edit = async (label, value, target = page) => {
    const root = target.locator(`[data-editor="${label}"]`); await root.locator('.monaco-editor').waitFor()
    await root.locator('.view-lines').click(); await target.keyboard.press(process.platform === 'darwin' ? 'Meta+A' : 'Control+A'); await target.keyboard.insertText(value)
  }
  const save = async (target = page) => { const response = target.waitForResponse(r => r.url().endsWith('/draft') && r.request().method() === 'PUT'); await target.getByRole('button', { name: '保存草稿', exact: true }).click(); assert.equal((await response).status(), 200) }
  const publish = async label => { await page.getByLabel('发布版本号').fill(label); const res = page.waitForResponse(r => r.url().endsWith('/versions') && r.request().method() === 'POST'); await page.getByRole('button', { name: '发布版本', exact: true }).click(); const r = await res; assert.equal(r.status(), 201, await r.text()); await page.getByRole('heading', { name: `不可变版本：${label}`, exact: true }).waitFor(); return r.json() }
  const create = async (kind, name) => { await page.locator('.app-sidebar').getByRole('link', { name: kind === 'Skill' ? 'Skills' : 'Agents', exact: true }).click(); await page.getByLabel('定义名称').fill(name); await page.getByRole('button', { name: `创建 ${kind}`, exact: true }).click(); await page.getByRole('heading', { name: `${name} · ${kind}`, exact: true }).waitFor() }
  await create('Skill', 'extract'); await edit('SKILL.md', '# Extract v1\nUse the original method.'); await save()
  await page.getByLabel('资源文件（最多 8 MiB）').setInputFiles({ name: 'template.txt', mimeType: 'text/plain', buffer: Buffer.from('original template') })
  const upload = page.waitForResponse(r => r.url().endsWith('/resources')); await page.getByRole('button', { name: '上传资源', exact: true }).click(); const uploadResult = await upload; assert.equal(uploadResult.status(), 200, await uploadResult.text())
  const skill1 = await publish('v1')
  await create('Skill', 'report'); await edit('SKILL.md', '# Report v1'); await save(); const skill2 = await publish('v1')
  await page.locator('.app-sidebar').getByRole('link', { name: 'Agents', exact: true }).click(); await create('Agent', 'assistant'); await edit('AGENTS.md', '# Assistant v1')
  await page.getByLabel('绑定模型').selectOption({ label: '发布模型' }); await page.getByLabel('Skill 版本：extract', { exact: true }).selectOption('v1'); await page.getByLabel('Skill 版本：report', { exact: true }).selectOption('v1'); await page.getByLabel('run_code', { exact: true }).check(); await page.getByText('run_code 允许 JavaScript', { exact: false }).waitFor()
  await page.getByRole('button', { name: '应用配置选择', exact: true }).click(); await save(); const agent1 = await publish('v1')
  assert.deepEqual(agent1.bundle.bindings.map(b => b.version_id).sort(), [skill1.id, skill2.id].sort())
  const snapshotResponse = page.waitForResponse(r => r.url().endsWith('/debug-snapshots') && r.request().method() === 'POST'); await page.getByRole('button', { name: '创建调试快照', exact: true }).click(); assert.equal((await snapshotResponse).status(), 201)
  await page.getByText('调试快照已固定，可在会话与运行中选择它。', { exact: true }).waitFor()
  await page.locator('.app-sidebar').getByRole('link', { name: 'Skills', exact: true }).click(); await page.getByRole('link', { name: 'extract', exact: true }).click(); await edit('SKILL.md', '# Extract v2\nUse the upgraded method.'); await save(); const skillV2 = await publish('v2')
  await page.getByLabel('查看版本').selectOption(skill1.id); await expect(page.getByTestId('version-markdown')).toContainText('Extract v1'); await page.getByText('assistant · v1', { exact: true }).waitFor()
  const download = await context.request.get(new URL(await page.locator('.version-detail a').first().getAttribute('href'), server.url).href)
  assert.equal(download.status(), 200); assert.equal(await download.text(), 'original template')
  await page.locator('.app-sidebar').getByRole('link', { name: 'Agents', exact: true }).click(); await page.getByRole('link', { name: 'assistant', exact: true }).click()
  await page.getByLabel('Skill 版本：extract', { exact: true }).selectOption('v2'); await page.getByRole('button', { name: '应用配置选择', exact: true }).click(); await edit('AGENTS.md', '# Assistant v2'); await save(); const agent2 = await publish('v2'); assert.equal(agent2.bundle.bindings.find(b => b.skill_id === skill1.definition_id).version_id, skillV2.id)
  await page.getByLabel('对比基准').selectOption(agent1.id); await page.locator('[data-editor="版本差异"] .monaco-diff-editor').waitFor()
  await page.getByRole('button', { name: '设为默认版本', exact: true }).click(); await expect(page.getByLabel('查看版本').locator('option:checked')).toHaveText('v2（默认）')
  await page.getByLabel('查看版本').selectOption(agent1.id); await expect(page.getByTestId('version-markdown')).toHaveText('# Assistant v1')
  // A second browser editor changes the same draft; the first must retain its work.
  const second = await context.newPage(); await second.goto(server.url); await second.locator('.app-sidebar').getByRole('link', { name: 'Agents', exact: true }).click(); await second.getByRole('link', { name: 'assistant', exact: true }).click()
  await edit('AGENTS.md', '# Concurrent server change', second); await save(second)
  await edit('AGENTS.md', '# My unsaved content'); const conflict = page.waitForResponse(r => r.url().endsWith('/draft') && r.request().method() === 'PUT'); await page.getByRole('button', { name: '保存草稿', exact: true }).click(); assert.equal((await conflict).status(), 409); await page.getByText('草稿已被其他编辑者修改。', { exact: false }).waitFor(); await page.locator('[data-editor="草稿冲突差异"] .monaco-diff-editor').waitFor()
  await page.getByRole('button', { name: '载入服务器草稿', exact: true }).click(); await second.close()
  await mkdir('.cache', { recursive: true }); await page.screenshot({ path: '.cache/d03-desktop.png', fullPage: true })
  await page.setViewportSize({ width: 390, height: 844 }); await page.getByRole('heading', { name: 'assistant · Agent', exact: true }).scrollIntoViewIfNeeded(); await page.evaluate(() => new Promise(resolve => requestAnimationFrame(() => requestAnimationFrame(resolve)))); assert.ok(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth), 'mobile overflow'); await page.screenshot({ path: '.cache/d03-mobile.png' })
  assert.deepEqual(errors, [])
  console.log('D03 browser flow passed: two Skills, Agent v1/v2, resources, immutable old content, debug snapshot, default version, Monaco diff and edit conflict.')
} catch (e) { await page?.screenshot({ path: '.cache/d03-failure.png', fullPage: true }); throw e }
finally { await browser?.close(); await server.stop() }
