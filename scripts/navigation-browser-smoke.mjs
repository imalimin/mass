// Navigation regression suite with an in-memory API; no database or provider calls.
import assert from 'node:assert/strict'
import { createServer } from 'node:http'
import { readFile, mkdir } from 'node:fs/promises'
import { createRequire } from 'node:module'
import { resolve, extname } from 'node:path'
import { once } from 'node:events'
process.env.PLAYWRIGHT_BROWSERS_PATH ??= resolve('.cache/playwright')
const require = createRequire(new URL('../frontend/package.json', import.meta.url)), { chromium, expect } = require('@playwright/test')
const spaces = [{ id: 'space-one', name: '研究空间', is_owner: true, created_at: '2026-10-26T00:00:00Z' }, { id: 'space-two', name: '协作空间', is_owner: false, created_at: '2026-10-24T00:00:00Z' }]
const sessions = [
  { id: 'session-one', definition_id: 'definition-one' },
  { id: 'session-two', definition_id: 'definition-one' },
  { id: 'session-three', definition_id: 'definition-two' },
].map(s => ({ ...s, space_id: 'space-one', title: '旧会话名称', version_id: 'version-one', workspace_error: '', created_at: '2026-10-25T00:00:00Z' }))
const usage = { input_tokens: null, output_tokens: null, cache_read_tokens: null, cache_write_tokens: null }
const runs = ['run-one', 'run-two'].map((id, i) => ({ id, session_id: 'session-one', created_at: `2026-10-25T00:00:0${i}Z`, status: 'succeeded', usage, error: '', workspace_snapshot_id: '' }))
const messages = Array.from({ length: 32 }, (_, index) => {
  const sequence = String(index + 1)
  return { id: `message-${sequence}`, run_id: '', source_event_id: `event-${sequence}`, sequence, role: index % 2 ? 'assistant' : 'user', content: `会话消息 ${sequence}`, truncated: false }
})
const pageOf = items => ({ items, next_cursor: null, has_more: false, next_after: '0' })
let signedIn = true, createdSpaces = 0, firstRunRejected = false
const requests = [], unexpected = [], models = [], modelKeys = new Map()
const server = createServer(async (req, res) => {
  const url = new URL(req.url, 'http://localhost'), path = url.pathname
  const json = (body, status = 200) => { res.writeHead(status, { 'Content-Type': 'application/json' }); res.end(JSON.stringify(body)) }
  if (path.startsWith('/api/')) {
    requests.push(path)
    if (path === '/api/v1/auth/me') return json(signedIn ? { id: 'alice', username: 'alice' } : { code: 'unauthenticated' }, signedIn ? 200 : 401)
    if (path === '/api/v1/auth/login') { signedIn = true; return json({ id: 'alice', username: 'alice' }) }
    if (path === '/api/v1/auth/logout') { signedIn = false; res.writeHead(204); return res.end() }
    if (path === '/api/v1/spaces' && req.method === 'POST') {
      let body = ''; for await (const chunk of req) body += chunk
      const { name } = JSON.parse(body)
      if (name === 'rejected-space') return json({ code: 'invalid_request' }, 400)
      const space = { id: `created-space-${++createdSpaces}`, name, is_owner: true, created_at: `2026-10-28T00:00:0${createdSpaces}Z` }
      spaces.push(space); return json(space, 201)
    }
    if (path === '/api/v1/spaces') return json({ ...pageOf(url.searchParams.has('cursor') ? spaces.slice(1) : spaces.slice(0, 1)), next_cursor: !url.searchParams.has('cursor') && spaces.length > 1 ? 'older-page' : null })
    if (path.endsWith('/members') && req.method === 'POST') return json({ code: 'forbidden' }, 403)
    if (path.endsWith('/members')) return json(pageOf([{ id: 'alice', username: 'alice', is_owner: true }]))
    if (/\/models(?:\/[^/]+)?$/.test(path)) {
      if (req.method === 'GET') return json(pageOf(models))
      let raw = ''; for await (const chunk of req) raw += chunk
      const body = JSON.parse(raw), id = path.endsWith('/models') ? `model-${models.length + 1}` : path.split('/').at(-1)
      assert.ok(!('credential_id' in body) && !('endpoint' in body), 'UI retained separate credential binding')
      const previous = models.find(m => m.id === id)
      if (!body.base_url || !/^https:\/\//.test(body.base_url) || (!previous && !body.api_key)) return json({ code: 'invalid_request' }, 400)
      if (previous && previous.revision !== body.revision) return json({ code: 'conflict' }, 409)
      if (body.api_key) modelKeys.set(id, body.api_key)
      const { api_key: _key, ...publicModel } = body
      const value = { ...publicModel, id, base_url: body.base_url.replace(/\/+$/, ''), revision: (previous?.revision ?? 0) + 1 }
      if (previous) models.splice(models.indexOf(previous), 1, value); else models.push(value)
      return json(value, previous ? 200 : 201)
    }
    if (path.endsWith('/model-capabilities')) return json({ provider: 'ark', input_types: ['text'], streaming: true, tool_calling: true })
    if (path.endsWith('/agents')) return json(pageOf([{ id: 'definition-one', name: 'report-agent' }, { id: 'definition-two', name: 'other-agent' }]))
    if (path.endsWith('/skills')) return json(pageOf([{ id: 'definition-one', name: 'extract-skill', archived: false }]))
    if (path.endsWith('/definition-one/versions')) return json(pageOf([
      { id: 'version-one', label: 'v1', created_at: '2026-10-25T00:00:00Z' },
      { id: 'version-two', label: 'v2', created_at: '2026-10-25T00:00:00.000001Z' },
      { id: 'version-three', label: 'v3', created_at: '2026-10-26T00:00:00Z', archived: true },
    ]))
    if (path.endsWith('/definition-one/debug-snapshots')) return json(pageOf([{ id: 'debug-one', debug: true, created_at: '2026-10-27T00:00:00Z' }]))
    if (/\/(versions|debug-snapshots)$/.test(path)) return json(pageOf([]))
    if (path.endsWith('/draft')) return json({ id: path.split('/').at(-2), kind: path.includes('/agents/') ? 'agent' : 'skill', name: path.includes('definition-two') ? 'other-agent' : path.includes('/agents/') ? 'report-agent' : 'extract-skill', revision: 1, default_version_id: 'version-one', archived: false, document: { markdown: '# Navigation fixture', config_yaml: '{}', assets: [] } })
    if (path.endsWith('/sessions') && req.method === 'POST') {
      let body = ''; for await (const chunk of req) body += chunk
      const input = JSON.parse(body)
      assert.equal(input.agent_id, 'definition-one')
      assert.equal(input.version_id, 'version-two')
      assert.ok(!('title' in input))
      const session = { ...sessions[0], id: 'session-new', title: 'session-new', version_id: input.version_id }
      sessions.push(session)
      return json(session, 201)
    }
    if (path.endsWith('/sessions')) return json(pageOf(sessions.filter(s => s.definition_id === path.split('/').at(-2))))
    if (path.endsWith('/messages')) {
      const all = path.includes('session-one') ? messages : [], before = Number(url.searchParams.get('before_sequence'))
      const eligible = all.filter(m => !before || Number(m.sequence) < before), limit = Number(url.searchParams.get('limit')) || 20
      const items = eligible.slice(-limit)
      return json({ items, has_more: eligible.length > limit, next_after: items.at(-1)?.sequence ?? '0' })
    }
    if (path.endsWith('/workspace-snapshots')) return json({ items: [] })
    if (/\/sessions\/session-(one|two|three|new)$/.test(path)) return json(sessions.find(s => path.endsWith(s.id)))
    if (path.endsWith('/session-new/runs') && req.method === 'POST') {
      if (!firstRunRejected) {
        firstRunRejected = true
        return json({ code: 'runtime_unavailable' }, 503)
      }
      const run = { ...runs[0], id: 'run-new', session_id: 'session-new', user_event_id: 'sent-message' }
      runs.push(run)
      return json(run, 202)
    }
    if (path.endsWith('/runs')) return json(pageOf(runs.filter(r => path.endsWith('/' + r.session_id + '/runs'))))
    if (/\/runs\/run-(one|two|new)$/.test(path)) return json(runs.find(r => path.endsWith(r.id)))
    if (path.endsWith('/ui-snapshot')) return json({ session_id: path.includes('/run-new/') ? 'session-new' : 'session-one', cursor: '0', source_sequence: '0', protocol_profile: 'mass-agui-v1', mapper_version: 1, parser_version: 1, closed: true, state: { status: 'succeeded', cancel_requested: false, error: '', usage, messages: [], tools: [], artifacts: [] } })
    unexpected.push(path); return json({ code: 'not_found' }, 404)
  }
  try {
    const asset = path.startsWith('/assets/') ? resolve('frontend/dist', '.' + path) : resolve('frontend/dist/index.html')
    const types = { '.js': 'text/javascript', '.css': 'text/css', '.html': 'text/html' }
    res.writeHead(200, { 'Content-Type': types[extname(asset)] ?? 'application/octet-stream' }); res.end(await readFile(asset))
  } catch { res.writeHead(404); res.end() }
})
server.listen(0, '127.0.0.1'); await once(server, 'listening')
const url = `http://127.0.0.1:${server.address().port}`
let browser, page
try {
  browser = await chromium.launch({ headless: true, channel: 'chromium' })
  page = await browser.newPage({ viewport: { width: 1440, height: 1000 } })
  const errors = []; page.on('pageerror', e => errors.push(e.message))
  const nav = label => page.locator('.app-sidebar').getByRole('link', { name: label, exact: true })
  const heading = text => expect(page.getByRole('heading', { level: 1 })).toHaveText(text)
  // The oldest space is deliberately on the second API page and has a later ID.
  await page.goto(url + '/'); await heading('空间概览'); await expect(page).toHaveURL(url + '/spaces/space-two')
  await expect(page.getByLabel('切换空间')).toHaveValue('space-two')
  await page.goto(url + '/login'); await expect(page).toHaveURL(url + '/spaces/space-two')
  await page.goto(url + '/spaces?space=space-one')
  await expect(page).toHaveURL(url + '/spaces/space-one'); await heading('空间概览')
  await expect(page.getByLabel('新空间名称')).toHaveCount(0)
  await expect(page.getByLabel('已有账号用户名')).toHaveCount(0)
  const settings = page.getByRole('button', { name: '账号设置', exact: true })
  await expect(page.getByRole('button', { name: '退出登录', exact: true })).toHaveCount(0)
  await expect(page.getByLabel('切换空间')).toBeVisible()
  await settings.click(); await expect(page.getByRole('dialog', { name: '账号设置' })).toBeVisible(); await expect(page.getByRole('dialog', { name: '账号设置' }).getByLabel('切换空间')).toHaveCount(0)
  await page.keyboard.press('Escape'); await expect(settings).toBeFocused(); await expect(settings).toHaveAttribute('aria-expanded', 'false')
  await settings.click(); await page.getByRole('heading', { level: 1 }).click(); await expect(page.getByRole('dialog', { name: '账号设置' })).toHaveCount(0)
  await nav('空间成员').click(); await heading('空间成员')
  await expect(page).toHaveURL(url + '/spaces/space-one/members'); await expect(page.getByLabel('已有账号用户名')).toBeVisible()
  await page.getByLabel('已有账号用户名').fill('bob')
  await page.getByRole('button', { name: '添加成员', exact: true }).click()
  await expect(page.getByRole('alert')).toHaveText('没有访问权限，或成员资格已被撤销。')
  await expect(page).toHaveURL(url + '/spaces/space-one/members')
  await expect(page.getByLabel('凭证名称')).toHaveCount(0)
  await expect(nav('供应商凭证')).toHaveCount(0)
  await nav('模型配置').click(); await heading('模型配置'); await expect(page.getByLabel('模型名称', { exact: true })).toBeVisible()
  await expect(page.getByLabel('使用凭证')).toHaveCount(0)
  await page.getByLabel('模型名称', { exact: true }).fill('primary'); await page.getByLabel('Ark 模型 ID').fill('fixture-model'); await page.getByLabel('Base URL', { exact: true }).fill('https://first.example/v1'); await page.getByLabel('API Key', { exact: true }).fill('synthetic-primary-key'); await page.getByRole('button', { name: '创建模型', exact: true }).click()
  await page.getByRole('button', { name: '编辑 primary', exact: true }).click(); await expect(page.getByLabel('Base URL', { exact: true })).toHaveValue('https://first.example/v1')
  await expect(page.getByLabel('API Key（留空保留）')).toHaveValue(''); await page.getByLabel('Base URL', { exact: true }).fill('https://rotated.example/v1'); await page.getByRole('button', { name: '保存模型', exact: true }).click()
  await page.getByRole('button', { name: '编辑 primary', exact: true }).click(); await expect(page.getByLabel('Base URL', { exact: true })).toHaveValue('https://rotated.example/v1')
  assert.equal(modelKeys.get('model-1'), 'synthetic-primary-key')
  await page.getByLabel('API Key（留空保留）').fill('synthetic-rotated-key'); await page.getByRole('button', { name: '保存模型', exact: true }).click(); await page.getByRole('button', { name: '编辑 primary', exact: true }).click()
  await expect(page.getByLabel('API Key（留空保留）')).toHaveValue(''); assert.equal(modelKeys.get('model-1'), 'synthetic-rotated-key')
  await mkdir('.cache', { recursive: true }); await page.screenshot({ path: '.cache/model-connection.png', fullPage: true })
  await page.getByRole('button', { name: '取消修改', exact: true }).click()
  await page.getByLabel('模型名称', { exact: true }).fill('backup'); await page.getByLabel('Ark 模型 ID').fill('backup-model'); await page.getByLabel('Base URL', { exact: true }).fill('https://backup.example/v1'); await page.getByLabel('API Key', { exact: true }).fill('synthetic-backup-key'); await page.getByRole('button', { name: '创建模型', exact: true }).click(); await page.getByRole('button', { name: '编辑 backup', exact: true }).waitFor()
  assert.equal(models[0].base_url, 'https://rotated.example/v1'); assert.equal(models[1].base_url, 'https://backup.example/v1'); assert.equal(modelKeys.get('model-2'), 'synthetic-backup-key')
  await page.reload(); await heading('模型配置'); await expect(nav('模型配置')).toHaveAttribute('aria-current', 'page')
  await page.goBack(); await heading('空间成员'); await page.goForward(); await heading('模型配置')
  await page.goto(url + '/spaces/space-one/credentials'); await expect(page).toHaveURL(url + '/spaces/space-one/models'); await heading('模型配置')
  await nav('Skills').click(); await heading('Skills'); await page.getByRole('link', { name: 'extract-skill', exact: true }).click()
  await expect(page).toHaveURL(url + '/spaces/space-one/skills/definition-one'); await page.getByRole('heading', { name: 'extract-skill · Skill' }).waitFor()
  await expect(page.getByLabel('定义名称')).toHaveCount(0)
  await page.reload(); await page.getByRole('heading', { name: 'extract-skill · Skill' }).waitFor()
  await nav('Agents').click(); await page.getByRole('link', { name: 'report-agent', exact: true }).click(); await page.getByRole('heading', { name: 'report-agent · Agent' }).waitFor()
  await expect(nav('会话与运行')).toHaveCount(0)
  await nav('Agents').click()
  await page.getByRole('link', { name: '打开 other-agent 的会话', exact: true }).click()
  await expect(page.locator('.app-sidebar')).toHaveCount(0)
  await expect(page.getByRole('link', { name: 'session-three', exact: true })).toBeVisible()
  await expect(page.getByRole('link', { name: 'session-one', exact: true })).toHaveCount(0)
  await page.goto(url + '/spaces/space-one/agents/definition-two/sessions/session-one')
  await expect(page.getByText('此会话不属于当前 Agent。')).toBeVisible()
  await page.getByRole('link', { name: '← Agents', exact: true }).click()
  await heading('Agents')
  await page.getByRole('link', { name: '打开 report-agent 的会话', exact: true }).click()
  await expect(page.getByLabel('Agent 版本')).toBeVisible()
  await expect(page.getByLabel('会话名称')).toHaveCount(0)
  await expect(page.getByRole('link', { name: 'session-three', exact: true })).toHaveCount(0)
  await expect(page.getByLabel('Agent 版本')).toHaveValue('version-two')
  await page.getByLabel('Agent 版本').selectOption('version-one')
  await expect(page.getByLabel('Agent 版本')).toHaveValue('version-one')
  await page.getByLabel('Agent 版本').selectOption('version-two')
  await page.getByLabel('输入任务').fill('First message')
  await expect(page.getByRole('link', { name: 'session-new', exact: true })).toHaveCount(0)
  await page.getByLabel('输入任务').press('Enter')
  await expect(page.getByRole('alert')).toContainText('执行服务暂不可用')
  await expect(page.getByLabel('输入任务')).toHaveValue('First message')
  await page.getByLabel('输入任务').press('Enter')
  await expect(page).toHaveURL(url + '/spaces/space-one/agents/definition-one/sessions/session-new')
  assert.equal(sessions.filter(s => s.id === 'session-new').length, 1)
  await expect(page.getByRole('heading', { name: 'session-new', exact: true })).toBeVisible()
  await expect(page.getByText('First message', { exact: true })).toHaveCount(1)
  await expect(page.getByLabel('Agent 版本')).toHaveCount(0)
  await page.getByRole('link', { name: '新建会话', exact: true }).click()
  await expect(page.getByLabel('输入任务')).toHaveValue('')
  await expect(page.getByLabel('Agent 版本')).toHaveValue('version-two')
  await page.goto(url + '/spaces/space-one/agents/definition-one/sessions?version=debug-one')
  await expect(page.getByLabel('Agent 版本')).toHaveValue('debug-one')
  await page.getByRole('link', { name: 'session-one', exact: true }).click()
  await expect(page.getByLabel('Agent 版本')).toHaveCount(0)
  await expect(page).toHaveURL(url + '/spaces/space-one/agents/definition-one/sessions/session-one'); await page.getByRole('heading', { name: 'session-one', exact: true }).waitFor()
  await expect(page.getByRole('link', { name: 'session-one', exact: true })).toHaveAttribute('aria-current', 'page')
  await expect(page.getByText('会话消息 32', { exact: true })).toBeVisible()
  await expect(page.getByText('会话消息 1', { exact: true })).toHaveCount(0)
  await page.getByRole('button', { name: '加载更早的消息', exact: true }).click()
  await expect(page.getByText('会话消息 1', { exact: true })).toHaveCount(1)
  await expect(page.locator('.chat-history article').last()).toContainText('会话消息 32')
  await expect(page.getByRole('button', { name: /回到最新消息/ })).toBeVisible()
  await page.getByRole('button', { name: /回到最新消息/ }).click()
  await expect(page.getByText('会话消息 32', { exact: true })).toBeInViewport()
  await expect(page.getByLabel('新空间名称')).toHaveCount(0)
  await expect(page.getByLabel('查看运行')).toBeHidden()
  await page.getByRole('button', { name: '运行详情', exact: true }).click()
  await page.getByLabel('查看运行').selectOption('run-one'); await expect(page).toHaveURL(url + '/spaces/space-one/agents/definition-one/sessions/session-one?run=run-one')
  await page.getByLabel('查看运行').selectOption('run-two'); await page.goBack(); await expect(page.getByLabel('查看运行')).toHaveValue('run-one')
  await page.reload(); await expect(page.getByLabel('查看运行')).toHaveValue('run-one')
  await page.goto(url + '/spaces?space=space-one&session=session-one&run=run-two')
  await expect(page).toHaveURL(url + '/spaces/space-one/agents/definition-one/sessions/session-one?run=run-two'); await expect(page.getByLabel('查看运行')).toHaveValue('run-two')
  await page.keyboard.press('Escape'); await page.getByRole('link', { name: 'session-two', exact: true }).click()
  await page.getByRole('heading', { name: 'session-two', exact: true }).waitFor(); await expect(page).not.toHaveURL(/\?run=/)
  await page.setViewportSize({ width: 390, height: 844 })
  await expect(page.getByRole('navigation', { name: '会话列表' })).toBeHidden()
  await page.getByRole('button', { name: '会话列表', exact: true }).click()
  await page.getByRole('link', { name: 'session-one', exact: true }).click()
  await expect(page.getByRole('heading', { name: 'session-one', exact: true })).toBeVisible()
  await expect(page.getByRole('navigation', { name: '会话列表' })).toBeHidden()
  assert.ok(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth), 'mobile conversation overflow')
  await expect(page.locator('.app-sidebar')).toHaveCount(0)
  await page.getByRole('link', { name: '← Agents', exact: true }).click()
  await heading('Agents')
  await page.setViewportSize({ width: 1440, height: 1000 })
  await page.getByLabel('切换空间').selectOption('space-two'); await heading('空间概览'); await expect(page.getByRole('heading', { name: 'session-two', exact: true })).toHaveCount(0)
  await expect(nav('供应商凭证')).toHaveCount(0)
  await page.goto(url + '/spaces/space-two/credentials'); await heading('模型配置'); await expect(page).toHaveURL(url + '/spaces/space-two/models'); await expect(page.getByLabel('API Key', { exact: true })).toHaveCount(0)
  assert.ok(!requests.includes('/api/v1/spaces/space-two/credentials'), 'member requested owner credentials')
  await page.getByLabel('切换空间').selectOption('space-one'); await heading('空间概览')
  await page.goto(url + '/spaces?view=all'); await heading('空间概览'); await expect(page).toHaveURL(url + '/spaces/space-two')
  await expect(page.getByRole('region', { name: '空间列表' })).toHaveCount(0)
  await expect(page.getByLabel('切换空间').locator('option')).toHaveCount(2)
  await expect(page.getByLabel('切换空间')).not.toContainText('我的空间')
  await page.getByLabel('切换空间').selectOption('space-one'); await heading('空间概览')
  await mkdir('.cache', { recursive: true }); await page.screenshot({ path: '.cache/navigation-desktop.png', fullPage: true })
  await page.setViewportSize({ width: 390, height: 844 }); await page.reload(); await heading('空间概览')
  assert.ok(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth), 'mobile overview overflow')
  await nav('模型配置').click(); await expect(page.getByLabel('模型名称', { exact: true })).toBeVisible()
  assert.ok(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth), 'mobile models overflow')
  await page.screenshot({ path: '.cache/navigation-mobile.png', fullPage: true })
  await settings.click(); await expect(page.getByRole('dialog', { name: '账号设置' }).getByRole('button', { name: '退出登录', exact: true })).toBeVisible()
  assert.ok(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth), 'mobile settings overflow')
  await page.screenshot({ path: '.cache/account-settings-mobile.png', fullPage: true })
  await page.keyboard.press('Escape')
  await page.getByRole('button', { name: '账号设置', exact: true }).click(); await page.getByRole('button', { name: '退出登录', exact: true }).click(); await page.getByRole('heading', { name: '登录 MASS' }).waitFor()
  await page.getByLabel('用户名', { exact: true }).fill('alice'); await page.getByLabel('密码', { exact: true }).fill('synthetic-navigation-password'); await page.getByRole('button', { name: '登录', exact: true }).click()
  await heading('空间概览'); await expect(page).toHaveURL(url + '/spaces/space-two')
  await settings.click(); await page.getByRole('button', { name: '退出登录', exact: true }).click(); await page.getByRole('heading', { name: '登录 MASS' }).waitFor()
  await page.goto(url + '/spaces/space-one/sessions/session-two'); await page.getByRole('heading', { name: '登录 MASS' }).waitFor()
  await page.getByLabel('用户名', { exact: true }).fill('alice'); await page.getByLabel('密码', { exact: true }).fill('synthetic-navigation-password'); await page.getByRole('button', { name: '登录', exact: true }).click()
  await page.getByRole('heading', { name: 'session-two', exact: true }).waitFor(); await expect(page).toHaveURL(url + '/spaces/space-one/agents/definition-one/sessions/session-two')
  spaces.shift() // Revoke membership while a detail page is open.
  await page.getByText('空间访问权限已失效，已关闭当前空间。', { exact: false }).waitFor({ timeout: 15000 })
  await expect(page).toHaveURL(url + '/spaces/space-two'); await expect(page.getByLabel('输入任务', { exact: true })).toHaveCount(0)
  spaces.splice(0) // A user with no spaces can still create their first one.
  await page.goto(url + '/spaces'); await heading('工作台'); await expect(page.getByLabel('新空间名称')).toHaveCount(0)
  await expect(page.getByLabel('切换空间')).toBeDisabled()
  const openCreate = async () => { await settings.click(); await page.getByRole('button', { name: '创建空间', exact: true }).click(); await expect(page.getByRole('dialog', { name: '创建空间', exact: true })).toBeVisible() }
  await openCreate(); await expect(page.getByLabel('新空间名称')).toBeFocused()
  await page.keyboard.press('Escape'); await expect(page.getByRole('dialog', { name: '创建空间', exact: true })).toHaveCount(0); await expect(settings).toBeFocused()
  await openCreate(); await page.getByRole('button', { name: '取消', exact: true }).click(); await expect(settings).toBeFocused()
  await openCreate(); await page.getByLabel('新空间名称').fill('   '); await expect(page.getByRole('button', { name: '创建空间', exact: true })).toBeDisabled()
  await page.getByLabel('新空间名称').fill('rejected-space'); await page.getByRole('button', { name: '创建空间', exact: true }).click(); await expect(page.getByRole('dialog', { name: '创建空间' }).getByRole('alert')).toBeVisible()
  await page.getByLabel('新空间名称').fill('第一个空间'); await page.getByRole('button', { name: '创建空间', exact: true }).click(); await expect(page).toHaveURL(url + '/spaces/created-space-1')
  await expect(page.getByRole('dialog', { name: '创建空间', exact: true })).toHaveCount(0); await expect(page.getByLabel('切换空间')).toHaveValue('created-space-1')
  await openCreate(); await page.getByLabel('新空间名称').fill('第二个空间'); await page.screenshot({ path: '.cache/create-space-mobile.png', fullPage: true })
  assert.ok(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth), 'mobile create dialog overflow')
  await page.setViewportSize({ width: 1440, height: 1000 }); await page.screenshot({ path: '.cache/create-space-desktop.png', fullPage: true })
  await page.getByRole('button', { name: '创建空间', exact: true }).click(); await expect(page).toHaveURL(url + '/spaces/created-space-2')
  await page.goto(url + '/spaces?view=all'); await expect(page).toHaveURL(url + '/spaces/created-space-1')
  await page.getByLabel('切换空间').selectOption('created-space-2'); await expect(page).toHaveURL(url + '/spaces/created-space-2')
  await expect(page.getByLabel('切换空间').locator('option')).toHaveCount(2)
  assert.deepEqual(errors, []); assert.deepEqual(unexpected, []); assert.ok(!requests.some(path => path.includes('/credentials')), 'UI called removed credential API')
  console.log('Navigation browser flow passed: separate pages, deep links, reload, browser history, legacy links, session/run scoping, role-aware navigation, membership revocation, modal space creation, per-model Base URL and API Key, key retention and rotation, login return and mobile layout. In-memory API only.')
} catch (e) { await mkdir('.cache', { recursive: true }); await page?.screenshot({ path: '.cache/navigation-failure.png', fullPage: true }); throw e }
finally { await browser?.close(); server.closeAllConnections(); await new Promise(done => server.close(done)) }
