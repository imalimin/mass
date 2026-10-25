// Navigation regression suite with an in-memory API; no database or provider calls.
import assert from 'node:assert/strict'
import { createServer } from 'node:http'
import { readFile, mkdir } from 'node:fs/promises'
import { createRequire } from 'node:module'
import { resolve, extname } from 'node:path'
import { once } from 'node:events'
process.env.PLAYWRIGHT_BROWSERS_PATH ??= resolve('.cache/playwright')
const require = createRequire(new URL('../frontend/package.json', import.meta.url)), { chromium, expect } = require('@playwright/test')
const spaces = [{ id: 'space-one', name: '研究空间', is_owner: true }, { id: 'space-two', name: '协作空间', is_owner: false }]
const sessions = [{ id: 'session-one', space_id: 'space-one', title: '周报会话', version_id: 'version-one', workspace_error: '' }, { id: 'session-two', space_id: 'space-one', title: '分析会话', version_id: 'version-one', workspace_error: '' }]
const usage = { input_tokens: null, output_tokens: null, cache_read_tokens: null, cache_write_tokens: null }
const runs = ['run-one', 'run-two'].map((id, i) => ({ id, session_id: 'session-one', created_at: `2026-10-25T00:00:0${i}Z`, status: 'succeeded', usage, error: '', workspace_snapshot_id: '' }))
const pageOf = items => ({ items, next_cursor: null, has_more: false, next_after: '0' })
let signedIn = true
const requests = [], unexpected = []
const server = createServer(async (req, res) => {
  const url = new URL(req.url, 'http://localhost'), path = url.pathname
  const json = (body, status = 200) => { res.writeHead(status, { 'Content-Type': 'application/json' }); res.end(JSON.stringify(body)) }
  if (path.startsWith('/api/')) {
    requests.push(path)
    if (path === '/api/v1/auth/me') return json(signedIn ? { id: 'alice', username: 'alice' } : { code: 'unauthenticated' }, signedIn ? 200 : 401)
    if (path === '/api/v1/auth/login') { signedIn = true; return json({ id: 'alice', username: 'alice' }) }
    if (path === '/api/v1/auth/logout') { signedIn = false; res.writeHead(204); return res.end() }
    if (path === '/api/v1/spaces') return json(pageOf(spaces))
    if (path.endsWith('/members')) return json(pageOf([{ id: 'alice', username: 'alice', is_owner: true }]))
    if (path.endsWith('/credentials') || path.endsWith('/models')) return json(pageOf([]))
    if (path.endsWith('/model-capabilities')) return json({ provider: 'ark', endpoint: 'https://example.test', input_types: ['text'], streaming: true, tool_calling: true })
    if (/\/(agents|skills)$/.test(path)) return json(pageOf([{ id: 'definition-one', name: path.endsWith('/agents') ? 'report-agent' : 'extract-skill', archived: false }]))
    if (/\/(versions|debug-snapshots)$/.test(path)) return json(pageOf([]))
    if (path.endsWith('/draft')) return json({ id: 'definition-one', kind: path.includes('/agents/') ? 'agent' : 'skill', name: path.includes('/agents/') ? 'report-agent' : 'extract-skill', revision: 1, archived: false, document: { markdown: '# Navigation fixture', config_yaml: '{}', assets: [] } })
    if (path.endsWith('/sessions')) return json(pageOf(sessions))
    if (path.endsWith('/messages')) return json(pageOf([]))
    if (path.endsWith('/workspace-snapshots')) return json({ items: [] })
    if (/\/sessions\/session-(one|two)$/.test(path)) return json(sessions.find(s => path.endsWith(s.id)))
    if (path.endsWith('/runs')) return json(pageOf(path.includes('session-one') ? runs : []))
    if (/\/runs\/run-(one|two)$/.test(path)) return json(runs.find(r => path.endsWith(r.id)))
    if (path.endsWith('/ui-snapshot')) return json({ session_id: 'session-one', cursor: '0', source_sequence: '0', protocol_profile: 'mass-agui-v1', mapper_version: 1, parser_version: 1, closed: true, state: { status: 'succeeded', cancel_requested: false, error: '', usage, messages: [], tools: [], artifacts: [] } })
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
  await page.goto(url + '/spaces?space=space-one')
  await expect(page).toHaveURL(url + '/spaces/space-one'); await heading('空间概览')
  await expect(page.getByLabel('新空间名称')).toHaveCount(0)
  await expect(page.getByLabel('已有账号用户名')).toHaveCount(0)
  const settings = page.getByRole('button', { name: '账号设置', exact: true })
  await expect(page.getByRole('button', { name: '退出登录', exact: true })).toHaveCount(0)
  await expect(page.getByLabel('切换空间')).toHaveCount(0)
  await settings.click(); await expect(page.getByRole('dialog', { name: '账号设置' })).toBeVisible()
  await page.keyboard.press('Escape'); await expect(settings).toBeFocused(); await expect(settings).toHaveAttribute('aria-expanded', 'false')
  await settings.click(); await page.getByRole('heading', { level: 1 }).click(); await expect(page.getByRole('dialog', { name: '账号设置' })).toHaveCount(0)
  await nav('空间成员').click(); await heading('空间成员')
  await expect(page).toHaveURL(url + '/spaces/space-one/members'); await expect(page.getByLabel('已有账号用户名')).toBeVisible()
  await expect(page.getByLabel('凭证名称')).toHaveCount(0)
  await nav('供应商凭证').click(); await heading('供应商凭证'); await expect(page.getByLabel('凭证名称')).toBeVisible()
  await expect(page.getByLabel('模型名称', { exact: true })).toHaveCount(0)
  await nav('模型配置').click(); await heading('模型配置'); await expect(page.getByLabel('模型名称', { exact: true })).toBeVisible()
  await page.reload(); await heading('模型配置'); await expect(nav('模型配置')).toHaveAttribute('aria-current', 'page')
  await page.goBack(); await heading('供应商凭证'); await page.goForward(); await heading('模型配置')
  await nav('Skills').click(); await heading('Skills'); await page.getByRole('link', { name: 'extract-skill', exact: true }).click()
  await expect(page).toHaveURL(url + '/spaces/space-one/skills/definition-one'); await page.getByRole('heading', { name: 'extract-skill · Skill' }).waitFor()
  await expect(page.getByLabel('定义名称')).toHaveCount(0)
  await page.reload(); await page.getByRole('heading', { name: 'extract-skill · Skill' }).waitFor()
  await nav('Agents').click(); await page.getByRole('link', { name: 'report-agent', exact: true }).click(); await page.getByRole('heading', { name: 'report-agent · Agent' }).waitFor()
  await nav('会话与运行').click(); await page.getByRole('link', { name: '周报会话', exact: true }).click()
  await expect(page).toHaveURL(url + '/spaces/space-one/sessions/session-one'); await page.getByRole('heading', { name: '周报会话', exact: true }).waitFor()
  await expect(page.getByLabel('新空间名称')).toHaveCount(0)
  await page.getByLabel('查看运行').selectOption('run-one'); await expect(page).toHaveURL(url + '/spaces/space-one/sessions/session-one?run=run-one')
  await page.getByLabel('查看运行').selectOption('run-two'); await page.goBack(); await expect(page.getByLabel('查看运行')).toHaveValue('run-one')
  await page.reload(); await expect(page.getByLabel('查看运行')).toHaveValue('run-one')
  await page.goto(url + '/spaces?space=space-one&session=session-one&run=run-two')
  await expect(page).toHaveURL(url + '/spaces/space-one/sessions/session-one?run=run-two'); await expect(page.getByLabel('查看运行')).toHaveValue('run-two')
  await page.getByRole('link', { name: '← 返回会话列表', exact: true }).click(); await page.getByRole('link', { name: '分析会话', exact: true }).click()
  await page.getByRole('heading', { name: '分析会话', exact: true }).waitFor(); await expect(page).not.toHaveURL(/\?run=/)
  await page.getByRole('button', { name: '账号设置', exact: true }).click(); await page.getByLabel('切换空间').selectOption('space-two'); await heading('空间概览'); await expect(page.getByRole('heading', { name: '分析会话', exact: true })).toHaveCount(0)
  await expect(nav('供应商凭证')).toHaveCount(0)
  await page.goto(url + '/spaces/space-two/credentials'); await page.getByRole('heading', { name: '需要管理员权限' }).waitFor()
  assert.ok(!requests.includes('/api/v1/spaces/space-two/credentials'), 'member requested owner credentials')
  await page.getByRole('button', { name: '账号设置', exact: true }).click(); await page.getByLabel('切换空间').selectOption('space-one'); await heading('空间概览')
  await mkdir('.cache', { recursive: true }); await page.screenshot({ path: '.cache/navigation-desktop.png', fullPage: true })
  await page.setViewportSize({ width: 390, height: 844 }); await page.reload(); await heading('空间概览')
  assert.ok(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth), 'mobile overview overflow')
  await nav('模型配置').click(); await expect(page.getByLabel('模型名称', { exact: true })).toBeVisible()
  assert.ok(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth), 'mobile models overflow')
  await page.screenshot({ path: '.cache/navigation-mobile.png', fullPage: true })
  await settings.click(); await expect(page.getByLabel('切换空间')).toBeVisible()
  assert.ok(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth), 'mobile settings overflow')
  await page.screenshot({ path: '.cache/account-settings-mobile.png', fullPage: true })
  await page.keyboard.press('Escape')
  await page.getByRole('button', { name: '账号设置', exact: true }).click(); await page.getByRole('button', { name: '退出登录', exact: true }).click(); await page.getByRole('heading', { name: '登录 MASS' }).waitFor()
  await page.goto(url + '/spaces/space-one/sessions/session-two'); await page.getByRole('heading', { name: '登录 MASS' }).waitFor()
  await page.getByLabel('用户名', { exact: true }).fill('alice'); await page.getByLabel('密码', { exact: true }).fill('synthetic-navigation-password'); await page.getByRole('button', { name: '登录', exact: true }).click()
  await page.getByRole('heading', { name: '分析会话', exact: true }).waitFor(); await expect(page).toHaveURL(url + '/spaces/space-one/sessions/session-two')
  spaces.shift() // Revoke membership while a detail page is open.
  await page.getByText('空间访问权限已失效，已关闭当前空间。', { exact: false }).waitFor({ timeout: 15000 })
  await expect(page).toHaveURL(url + '/spaces'); await expect(page.getByLabel('输入任务', { exact: true })).toHaveCount(0)
  assert.deepEqual(errors, []); assert.deepEqual(unexpected, [])
  console.log('Navigation browser flow passed: separate pages, deep links, reload, browser history, legacy links, session/run scoping, role-aware navigation, membership revocation, login return and mobile layout. In-memory API only.')
} catch (e) { await mkdir('.cache', { recursive: true }); await page?.screenshot({ path: '.cache/navigation-failure.png', fullPage: true }); throw e }
finally { await browser?.close(); server.closeAllConnections(); await new Promise(done => server.close(done)) }
