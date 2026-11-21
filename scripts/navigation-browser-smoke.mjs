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
sessions.push({ ...sessions[0], id: 'session-shared', title: 'session-shared', space_id: 'space-two', agent_source_space_id: 'space-one', agent_name: 'report-agent' })
const usage = { input_tokens: null, output_tokens: null, cache_read_tokens: null, cache_write_tokens: null }
const creativitySnapshot = '02c1a2c3-dc3c-4ab2-8724-d7f2a68bf755'
const publishModel = { id: 'd8626872-6a61-44ae-95f7-50db3ec58caf', name: '发布测试模型' }
const runs = ['run-one', 'run-two'].map((id, i) => ({ id, session_id: 'session-one', created_at: `2026-10-25T00:00:0${i}Z`, status: 'succeeded', usage, error: '', workspace_snapshot_id: '' }))
const messages = Array.from({ length: 32 }, (_, index) => {
  const sequence = String(index + 1)
  return { id: `message-${sequence}`, run_id: '', source_event_id: `event-${sequence}`, sequence, role: index % 2 ? 'assistant' : 'user', content: `会话消息 ${sequence}`, truncated: false }
})
const pageOf = items => ({ items, next_cursor: null, has_more: false, next_after: '0' })
let signedIn = true, createdSpaces = 0, firstRunRejected = false, agentVisibility = 'space'
let publishCalls = 0, pendingModelSelection = false, debugKey, debugSubmittedRun = false, debugSaved = false, debugFailed = false
const directDebug = Promise.withResolvers()
const requests = [], unexpected = [], models = [], modelKeys = new Map(), creativePrompts = []
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
    if (/\/evaluation\/(datasets|experiments)$/.test(path)) return json(pageOf([]))
    if (path.endsWith('/members') && req.method === 'POST') return json({ code: 'forbidden' }, 403)
    if (path.endsWith('/members')) return json(pageOf([{ id: 'alice', username: 'alice', is_owner: true }]))
    if (/\/models(?:\/[^/]+)?$/.test(path)) {
      if (req.method === 'GET') return json(pageOf(pendingModelSelection && url.searchParams.has('available') ? [publishModel] : models))
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
    if (path.endsWith('/agents/definition-one/visibility') && req.method === 'PUT') {
      let body = ''; for await (const chunk of req) body += chunk
      agentVisibility = JSON.parse(body).visibility
      return json({ visibility: agentVisibility })
    }
    if (path.endsWith('/draft')) return json({ id: path.split('/').at(-2), kind: path.includes('/agents/') ? 'agent' : 'skill', name: path.includes('definition-two') ? 'other-agent' : path.includes('/agents/') ? 'report-agent' : 'extract-skill', revision: 1, default_version_id: 'version-one', archived: false, visibility: agentVisibility, document: { markdown: '# Navigation fixture', config_yaml: '{}', assets: [] } })
    if (path.endsWith('/creativity')) return json({ available: path.includes('/space-one/') })
    if (path.endsWith('/creativity/sessions') && req.method === 'POST') {
      let body = ''; for await (const chunk of req) body += chunk
      const input = JSON.parse(body)
      if (input.target_agent_id) {
        assert.equal(input.target_agent_id, 'definition-one')
        assert.ok(input.idempotency_key)
        const session = { ...sessions[0], id: 'session-iterate', title: 'session-iterate', creativity: { target_agent_id: input.target_agent_id, target_version_id: 'version-two' } }
        sessions.push(session)
        return json(session, 201)
      }
      assert.deepEqual(Object.keys(input), ['idempotency_key']); assert.ok(input.idempotency_key)
      const session = { ...sessions[0], id: 'session-creative', title: 'session-creative', creativity: {} }
      sessions.push(session)
      return json(session, 201)
    }
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
    if (path.endsWith('/sessions')) return json(pageOf(sessions.filter(s => s.space_id === path.split('/')[4] && s.definition_id === path.split('/').at(-2))))
    if (path.endsWith('/messages')) {
      const all = path.includes('session-one') ? messages : [], before = Number(url.searchParams.get('before_sequence'))
      const eligible = all.filter(m => !before || Number(m.sequence) < before), limit = Number(url.searchParams.get('limit')) || 20
      const items = eligible.slice(-limit)
      return json({ items, has_more: eligible.length > limit, next_after: items.at(-1)?.sequence ?? '0' })
    }
    if (path.endsWith('/workspace-snapshots')) return json({ items: [] })
    if (path.endsWith('/workspace/files')) {
      const entry = url.searchParams.get('path')
      if (entry === '.') return json({ directory: true, entries: [{ name: 'AGENTS.md', directory: false }, { name: 'skills', directory: true }] })
      if (entry === 'AGENTS.md') return json({ directory: false, text: '# Navigation fixture' })
      if (entry === 'skills') return json({ directory: true, entries: [{ name: 'report-writer', directory: true }] })
      if (entry === 'skills/report-writer') return json({ directory: true, entries: [{ name: 'SKILL.md', directory: false }] })
      if (entry === 'skills/report-writer/SKILL.md') return json({ directory: false, text: '# Report writer' })
    }
    if (/\/sessions\/session-(one|two|three|new|creative|shared|iterate|debug|debug-direct)$/.test(path)) return json(sessions.find(s => path.endsWith(s.id)))
    if (path.endsWith('/session-creative/runs') && req.method === 'POST') {
      let body = ''; for await (const chunk of req) body += chunk
      creativePrompts.push(JSON.parse(body))
      const run = { ...runs[0], id: 'run-creative', session_id: 'session-creative', user_event_id: 'creative-message', workspace_snapshot_committed: true, workspace_snapshot_id: creativitySnapshot }
      runs.push(run)
      return json(run, 202)
    }
    if (path.endsWith('/session-creative/draft-debug')) {
      let body = ''; for await (const chunk of req) body += chunk
      const input = JSON.parse(body)
      assert.equal(input.workspace_snapshot_id, creativitySnapshot)
      assert.ok(input.idempotency_key)
      const id = debugSaved ? 'session-debug-direct' : 'session-debug'
      if (!debugSaved) {
        debugKey ??= input.idempotency_key
        assert.equal(input.idempotency_key, debugKey)
        if (!input.model_id) {
          pendingModelSelection = true
          return json({ code: 'publish_model_required' }, 400)
        }
        assert.equal(input.model_id, publishModel.id)
        if (!debugFailed) {
          debugFailed = true
          return json({ code: 'runtime_unavailable' }, 503)
        }
      } else {
        assert.equal(input.model_id, undefined)
        assert.notEqual(input.idempotency_key, debugKey)
        await directDebug.promise
      }
      debugSaved = true
      const session = { ...sessions[0], id, agent_name: 'report-agent', draft_revision: 2 }
      sessions.push(session)
      return json(session, 201)
    }
    if (path.endsWith('/session-creative/publish')) {
      const manifest = { model_id: publishModel.id, model_locked: debugSaved || publishCalls > 0, agent: { name: 'report-agent', action: 'create' }, skills: [{ name: 'report-writer', action: 'update' }] }
      if (req.method === 'GET') {
        assert.equal(url.searchParams.get('workspace_snapshot_id'), creativitySnapshot)
        if (!url.searchParams.has('model_id') && !manifest.model_locked) {
          pendingModelSelection = true
          return json({ code: 'publish_model_required' }, 400)
        }
        assert.ok(manifest.model_locked || url.searchParams.get('model_id') === publishModel.id)
        return json(manifest)
      }
      let body = ''; for await (const chunk of req) body += chunk
      assert.deepEqual(JSON.parse(body), { workspace_snapshot_id: creativitySnapshot, ...(manifest.model_locked ? {} : { model_id: publishModel.id }) })
      const skills = [{ ...manifest.skills[0], label: 'v2', status: 'published' }]
      if (++publishCalls === 1) return json({
        code: 'content_unavailable', message: 'Agent report-agent：Content unavailable',
        details: { result: { ...manifest, model_locked: true, agent: { ...manifest.agent, status: 'draft_saved' }, skills } },
      }, 503)
      pendingModelSelection = false
      return json({ ...manifest, agent: { ...manifest.agent, label: 'v1', status: 'published' }, skills })
    }
    if (path.endsWith('/session-new/runs') && req.method === 'POST') {
      if (!firstRunRejected) {
        firstRunRejected = true
        return json({ code: 'runtime_unavailable' }, 503)
      }
      const run = { ...runs[0], id: 'run-new', session_id: 'session-new', user_event_id: 'sent-message' }
      runs.push(run)
      return json(run, 202)
    }
    if (/\/session-debug(-direct)?\/runs$/.test(path) && req.method === 'POST') debugSubmittedRun = true
    if (path.endsWith('/runs')) return json(pageOf(runs.filter(r => path.endsWith('/' + r.session_id + '/runs'))))
    if (/\/runs\/run-(one|two|new|creative)$/.test(path)) return json(runs.find(r => path.endsWith(r.id)))
    if (path.endsWith('/ui-snapshot')) return json({ session_id: runs.find(r => path.includes(`/${r.id}/`))?.session_id, cursor: '0', source_sequence: '0', protocol_profile: 'mass-agui-v1', mapper_version: 1, parser_version: 1, closed: true, state: { status: 'succeeded', cancel_requested: false, error: '', usage, messages: [], tools: [], artifacts: [] } })
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
  const historyRail = page.getByRole('navigation', { name: '会话列表' })
  const expectHistoryOnLeft = async () => {
    const conversationPage = page.locator('.conversation-page')
    await expect(historyRail).toBeVisible()
    await expect(conversationPage).toBeVisible()
    const rail = await historyRail.boundingBox(), conversation = await conversationPage.boundingBox()
    assert.ok(rail && conversation && rail.x + rail.width <= conversation.x, 'conversation history is not a separate left column')
  }
  // The oldest space is deliberately on the second API page and has a later ID.
  await page.goto(url + '/'); await heading('创意'); await expect(page).toHaveURL(url + '/spaces/space-two')
  await expect(page.getByRole('button', { name: '发送', exact: true })).toBeDisabled()
  await expect(page.getByLabel('切换空间')).toHaveValue('space-two')
  const beforeSharedSession = requests.length
  await page.goto(url + '/spaces/space-two/agents/definition-one/sessions/session-shared')
  await heading('session-shared'); await expect(page.getByLabel('输入任务')).toBeVisible()
  assert.ok(!requests.slice(beforeSharedSession).some(path => path.endsWith('/draft')), 'shared session requested a draft')
  await page.goto(url + '/login'); await expect(page).toHaveURL(url + '/spaces/space-two')
  await page.goto(url + '/spaces?space=space-one')
  await expect(page).toHaveURL(url + '/spaces/space-one'); await heading('创意')
  await page.getByLabel('新建 Agent 的要求').fill('做一个报告 Agent')
  await page.getByRole('button', { name: '发送', exact: true }).click()
  await expect(page).toHaveURL(url + '/spaces/space-one/creativity/sessions/session-creative')
  await expect(page.getByText('做一个报告 Agent', { exact: true })).toHaveCount(1)
  await expect(page.getByRole('navigation', { name: '会话列表' })).toHaveCount(0)
  await page.reload(); await heading('session-creative')
  await page.getByLabel('输入任务').fill('继续调试')
  await page.getByRole('button', { name: 'skills', exact: true }).click()
  await page.getByRole('button', { name: 'report-writer', exact: true }).click()
  await page.getByRole('button', { name: 'SKILL.md', exact: true }).click()
  await expect(page.getByRole('region', { name: '只读文件预览' })).toContainText('# Report writer')
  await page.getByRole('button', { name: 'AGENTS.md' }).click()
  await expect(page.getByRole('region', { name: '只读文件预览' })).toContainText('# Navigation fixture')
  await page.getByRole('button', { name: '关闭预览', exact: true }).click()
  await expect(page.getByRole('region', { name: '只读文件预览' })).toHaveCount(0)
  await expect(page.getByLabel('输入任务')).toHaveValue('继续调试')
  await expect(page.getByRole('button', { name: 'SKILL.md', exact: true })).toBeVisible()
  await page.getByRole('button', { name: '去发布', exact: true }).click()
  const publishDialog = page.getByRole('dialog', { name: '发布 Agent 和 Skills' })
  await expect(publishDialog.getByLabel('发布模型')).toBeVisible()
  await expect(publishDialog.getByRole('button', { name: '确认发布' })).toBeDisabled()
  await expect(publishDialog.getByRole('list')).toHaveCount(0)
  await publishDialog.getByRole('button', { name: '关闭', exact: true }).click()
  await page.getByRole('button', { name: '去试试', exact: true }).click()
  const debugDialog = page.getByRole('dialog', { name: '草稿调试' })
  await expect(debugDialog.getByLabel('调试模型')).toBeVisible()
  await debugDialog.getByLabel('调试模型').selectOption(publishModel.id)
  await expect(debugDialog.getByRole('alert')).toBeVisible()
  await debugDialog.getByRole('button', { name: '重试', exact: true }).click()
  await expect(page).toHaveURL(url + '/spaces/space-one/agents/definition-one/sessions/session-debug')
  await expect(page.getByRole('navigation', { name: '会话列表' })).toHaveCount(0)
  await expect(page.getByText('草稿调试 · r2')).toBeVisible()
  await expect(page.getByLabel('输入任务')).toBeVisible()
  assert.equal(debugSubmittedRun, false)
  await page.getByRole('link', { name: '← 返回创意会话' }).click(); await heading('session-creative')
  try {
    await page.getByRole('button', { name: '去试试', exact: true }).click()
    await expect(debugDialog).toBeVisible()
    await expect(debugDialog.getByRole('status')).toHaveText('正在校验并准备调试会话…')
  } finally {
    directDebug.resolve()
  }
  await expect(page).toHaveURL(url + '/spaces/space-one/agents/definition-one/sessions/session-debug-direct')
  await expect(page.getByRole('dialog')).toHaveCount(0)
  await expect(page.getByRole('navigation', { name: '会话列表' })).toHaveCount(0)
  await page.reload(); await heading('session-debug-direct')
  await expect(page.getByRole('navigation', { name: '会话列表' })).toHaveCount(0)
  assert.equal(debugSubmittedRun, false)
  await page.getByRole('link', { name: '← 返回创意会话' }).click(); await heading('session-creative')
  await page.goto(url + '/spaces/space-one/agents/definition-one/sessions/session-one')
  const debugHistory = page.getByRole('navigation', { name: '会话列表' })
  await debugHistory.getByRole('link', { name: 'session-debug 草稿调试', exact: true }).click()
  await heading('session-debug')
  await expect(debugHistory).toBeVisible()
  await page.goto(url + '/spaces/space-one/creativity/sessions/session-creative'); await heading('session-creative')
  await page.getByLabel('输入任务').fill('继续调试')
  await page.getByRole('button', { name: 'skills', exact: true }).click()
  await page.getByRole('button', { name: 'report-writer', exact: true }).click()
  await page.getByRole('button', { name: '去发布', exact: true }).click()
  await expect(publishDialog).toContainText('report-writer — 编辑并发新版')
  await expect(publishDialog.locator('input, textarea')).toHaveCount(0)
  await publishDialog.getByRole('button', { name: '确认发布' }).click()
  await expect(publishDialog.getByRole('alert')).toContainText('Agent report-agent')
  await expect(publishDialog).toContainText('report-agent — 草稿已保存，未发布')
  await expect(publishDialog).toContainText('report-writer — 已发布 · v2')
  await expect(publishDialog.getByLabel('发布模型')).toHaveCount(0)
  await expect(publishDialog).toContainText('本次发布已固定模型：')
  await publishDialog.getByRole('button', { name: '重试发布' }).click()
  await expect(publishDialog).toContainText('report-agent — 已发布 · v1')
  await expect(publishDialog).toContainText('report-writer — 已发布 · v2')
  await expect(publishDialog).toContainText('发布完成')
  await publishDialog.getByRole('button', { name: '关闭', exact: true }).click()
  await expect(page.getByLabel('输入任务')).toHaveValue('继续调试')
  await expect(page.getByRole('button', { name: 'SKILL.md', exact: true })).toBeVisible()
  assert.equal(creativePrompts.length, 1)
  assert.equal(creativePrompts[0].text, '做一个报告 Agent')
  assert.ok(creativePrompts[0].idempotency_key)
  await page.getByRole('link', { name: '← 创意' }).click(); await heading('创意')
  await nav('空间概览').click(); await heading('空间概览')
  await expect(page).toHaveURL(url + '/spaces/space-one/overview')
  await nav('创意').click(); await heading('创意')
  await expect(page.getByLabel('新空间名称')).toHaveCount(0)
  await expect(page.getByLabel('已有账号用户名')).toHaveCount(0)
  const settings = page.getByRole('button', { name: '账号设置', exact: true })
  await expect(page.getByRole('button', { name: '退出登录', exact: true })).toHaveCount(0)
  await expect(page.getByLabel('切换空间')).toBeVisible()
  await settings.click(); await expect(page.getByRole('dialog', { name: '账号设置' })).toBeVisible(); await expect(page.getByRole('dialog', { name: '账号设置' }).getByLabel('切换空间')).toHaveCount(0)
  await page.keyboard.press('Escape'); await expect(settings).toBeFocused(); await expect(settings).toHaveAttribute('aria-expanded', 'false')
  await settings.click(); await page.getByRole('heading', { level: 1 }).click(); await expect(page.getByRole('dialog', { name: '账号设置' })).toHaveCount(0)
  await expect(page.locator('.app-sidebar .nav-caption').getByText('评测', { exact: true })).toBeVisible()
  await nav('评测集').click(); await heading('评测集'); await expect(page).toHaveURL(url + '/spaces/space-one/evaluation/datasets')
  await expect(nav('评测集')).toHaveAttribute('aria-current', 'page'); await expect(nav('实验')).not.toHaveAttribute('aria-current', 'page')
  await page.getByRole('link', { name: '新建评测集', exact: true }).click(); await heading('评测集'); await expect(nav('评测集')).toHaveAttribute('aria-current', 'page')
  await page.reload(); await heading('评测集'); await expect(page.getByLabel('评测集名称', { exact: true })).toBeVisible()
  await nav('实验').click(); await heading('实验'); await expect(page).toHaveURL(url + '/spaces/space-one/evaluation/experiments')
  await expect(nav('实验')).toHaveAttribute('aria-current', 'page'); await expect(nav('评测集')).not.toHaveAttribute('aria-current', 'page')
  await expect(page.getByRole('heading', { name: '发起实验', exact: true })).toBeVisible()
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
  await expect(page.getByLabel('权限范围')).toHaveValue('space')
  await page.getByLabel('权限范围').selectOption('public')
  await expect(page.getByText('权限范围已更新')).toBeVisible()
  await expect(page.getByLabel('权限范围')).toHaveValue('public')
  await expect(nav('会话与运行')).toHaveCount(0)
  await page.getByRole('button', { name: '在创意中迭代', exact: true }).click()
  await expect(page).toHaveURL(url + '/spaces/space-one/creativity/sessions/session-iterate')
  await expect(page.getByText('正在迭代：report-agent')).toBeVisible()
  await expect(page.getByRole('navigation', { name: '会话列表' })).toHaveCount(0)
  await expect(page.getByLabel('输入任务')).toHaveValue('')
  await expect(page.getByRole('button', { name: '去发布', exact: true })).toBeDisabled()
  await expect(page.getByRole('button', { name: '去试试', exact: true })).toBeDisabled()
  await page.getByRole('button', { name: 'AGENTS.md' }).click()
  await expect(page.getByRole('region', { name: '只读文件预览' })).toContainText('# Navigation fixture')
  await page.reload()
  await expect(page.getByText('正在迭代：report-agent')).toBeVisible()
  await page.getByRole('link', { name: '← 创意' }).click()
  await nav('Agents').click()
  await page.getByRole('link', { name: '打开 other-agent 的会话', exact: true }).click()
  await expect(page.locator('.app-sidebar')).toHaveCount(0)
  await expect(page.getByRole('link', { name: 'session-three', exact: true })).toBeVisible()
  await expectHistoryOnLeft()
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
  await page.getByRole('button', { name: 'AGENTS.md', exact: true }).click()
  await expect(page.getByRole('region', { name: '只读文件预览' })).toBeVisible()
  await expectHistoryOnLeft()
  await page.getByRole('button', { name: '关闭预览', exact: true }).click()
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
  await expectHistoryOnLeft()
  await page.getByRole('link', { name: 'session-one', exact: true }).click()
  await expect(page.getByRole('heading', { name: 'session-one', exact: true })).toBeVisible()
  await expectHistoryOnLeft()
  assert.ok(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth), 'mobile conversation overflow')
  await expect(page.locator('.app-sidebar')).toHaveCount(0)
  await page.getByRole('link', { name: '← Agents', exact: true }).click()
  await heading('Agents')
  await page.setViewportSize({ width: 1440, height: 1000 })
  await page.getByLabel('切换空间').selectOption('space-two'); await heading('创意'); await expect(page.getByRole('heading', { name: 'session-two', exact: true })).toHaveCount(0)
  await expect(nav('供应商凭证')).toHaveCount(0)
  await page.goto(url + '/spaces/space-two/credentials'); await heading('模型配置'); await expect(page).toHaveURL(url + '/spaces/space-two/models'); await expect(page.getByLabel('API Key', { exact: true })).toHaveCount(0)
  assert.ok(!requests.includes('/api/v1/spaces/space-two/credentials'), 'member requested owner credentials')
  await page.getByLabel('切换空间').selectOption('space-one'); await heading('创意')
  await page.goto(url + '/spaces?view=all'); await heading('创意'); await expect(page).toHaveURL(url + '/spaces/space-two')
  await expect(page.getByRole('region', { name: '空间列表' })).toHaveCount(0)
  await expect(page.getByLabel('切换空间').locator('option')).toHaveCount(2)
  await expect(page.getByLabel('切换空间')).not.toContainText('我的空间')
  await page.getByLabel('切换空间').selectOption('space-one'); await heading('创意')
  await mkdir('.cache', { recursive: true }); await page.screenshot({ path: '.cache/navigation-desktop.png', fullPage: true })
  await page.setViewportSize({ width: 390, height: 844 }); await page.reload(); await heading('创意')
  assert.ok(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth), 'mobile overview overflow')
  await nav('评测集').click(); await heading('评测集')
  await nav('实验').click(); await heading('实验')
  assert.ok(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth), 'mobile evaluation navigation overflow')
  await nav('模型配置').click(); await expect(page.getByLabel('模型名称', { exact: true })).toBeVisible()
  assert.ok(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth), 'mobile models overflow')
  await page.screenshot({ path: '.cache/navigation-mobile.png', fullPage: true })
  await settings.click(); await expect(page.getByRole('dialog', { name: '账号设置' }).getByRole('button', { name: '退出登录', exact: true })).toBeVisible()
  assert.ok(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth), 'mobile settings overflow')
  await page.screenshot({ path: '.cache/account-settings-mobile.png', fullPage: true })
  await page.keyboard.press('Escape')
  await page.getByRole('button', { name: '账号设置', exact: true }).click(); await page.getByRole('button', { name: '退出登录', exact: true }).click(); await page.getByRole('heading', { name: '登录 MASS' }).waitFor()
  await page.getByLabel('用户名', { exact: true }).fill('alice'); await page.getByLabel('密码', { exact: true }).fill('synthetic-navigation-password'); await page.getByRole('button', { name: '登录', exact: true }).click()
  await heading('创意'); await expect(page).toHaveURL(url + '/spaces/space-two')
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
  console.log('Navigation browser flow passed: Creativity entry, first prompt, workspace preview, deep links, reload, legacy links, session/run scoping, role-aware navigation, membership revocation, space creation, model settings, login return and mobile layout. In-memory API only.')
} catch (e) { await mkdir('.cache', { recursive: true }); await page?.screenshot({ path: '.cache/navigation-failure.png', fullPage: true }); throw e }
finally { await browser?.close(); server.closeAllConnections(); await new Promise(done => server.close(done)) }
