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
  const context = await browser.newContext({ viewport: { width: 1440, height: 1000 } })
  page = await context.newPage()
  // Loopback is a secure context. Simulate the Web Crypto API exposed on LAN HTTP.
  await page.addInitScript(() => Object.defineProperty(crypto, 'randomUUID', { value: undefined }))
  const errors = []
  page.on('pageerror', error => errors.push(error.message))
  await page.goto(server.url)
  await page.getByLabel('用户名', { exact: true }).fill('alice')
  await page.getByLabel('密码', { exact: true }).fill('correct-password-alice')
  await page.getByRole('button', { name: '登录', exact: true }).click()
  await expect(page.getByRole('button', { name: '账号设置', exact: true })).toBeVisible()
  const api = async (method, path, data) => {
    const response = await context.request.fetch(server.url + '/api/v1' + path, { method, data, headers: { Origin: server.url, 'Content-Type': 'application/json' } })
    assert.ok(response.ok(), `${method} ${path}: ${response.status()} ${await response.text()}`)
    return response.status() === 204 ? undefined : response.json()
  }
  const space = await api('POST', '/spaces', { name: '测评验收' }), root = `/spaces/${space.id}`
  const model = await api('POST', `${root}/models`, { name: '测评模型', provider: 'ark', model_id: 'fixture', base_url: server.modelBaseURL, api_key: 'fixture-key', context_window: 32768, max_output_tokens: 4096, temperature: 1, enabled: true })
  const agent = await api('POST', `${root}/agents`, { name: 'evaluation-assistant' })
  const saved = await api('PUT', `${root}/agents/${agent.id}/draft`, { expected_revision: agent.revision, document: { markdown: 'Answer user requests.', config_yaml: JSON.stringify({ schema_version: 1, name: agent.name, model: model.id, tools: [], skills: [], context: { max_input_tokens: 20000, max_tokens: 1024, compression: 'summary' }, limits: { max_steps: 6, timeout_seconds: 30 } }) } })
  const version = await api('POST', `${root}/agents/${agent.id}/versions`, { expected_revision: saved.revision, label: 'v1' })
  await page.goto(`${server.url}${root}`)
  await page.locator('.app-sidebar').getByRole('link', { name: '评测集', exact: true }).click()
  await page.getByRole('link', { name: '新建评测集', exact: true }).click()
  const datasetDialog = page.getByRole('dialog', { name: '新建评测集' })
  await datasetDialog.getByLabel('评测集名称', { exact: true }).fill('多轮文本验收')
  await page.getByLabel('第 1 轮用户输入', { exact: true }).fill('D06 first')
  await page.getByRole('button', { name: '添加一轮', exact: true }).click()
  await page.getByLabel('第 2 轮用户输入', { exact: true }).fill('D06 second')
  const creating = page.waitForResponse(r => r.request().method() === 'POST' && r.url().endsWith('/evaluation/datasets'))
  await datasetDialog.getByRole('button', { name: '创建 v1', exact: true }).click()
  const dataset = await (await creating).json()
  await expect(page.getByRole('heading', { name: '编辑评测集 · v1' })).toBeVisible()
  await expect(page.getByLabel('第 2 轮用户输入', { exact: true })).toHaveValue('D06 second')
  await page.getByRole('button', { name: '图片（暂不支持）' }).first().isDisabled().then(value => assert.ok(value))
  await page.getByRole('button', { name: '添加用例', exact: true }).click()
  await page.getByLabel('第 1 轮用户输入', { exact: true }).nth(1).fill('D06 independent')
  const saving = page.waitForResponse(r => r.request().method() === 'POST' && r.url().endsWith('/versions'))
  await page.getByRole('button', { name: '保存新版本', exact: true }).click()
  assert.equal((await (await saving).json()).version, 2)
  await expect(page.getByRole('heading', { name: '编辑评测集 · v2' })).toBeVisible()
  await page.locator('.app-sidebar').getByRole('link', { name: '实验', exact: true }).click()
  await page.getByRole('button', { name: '发起实验', exact: true }).click()
  const experimentDialog = page.getByRole('dialog', { name: '发起实验' })
  await experimentDialog.getByLabel('评测集', { exact: true }).selectOption(dataset.id)
  await page.getByLabel('Agent', { exact: true }).selectOption(agent.id)
  await page.getByLabel('发布版本', { exact: true }).selectOption(version.id)
  const concurrency = page.getByLabel('并发用例数', { exact: true })
  for (const invalid of ['0', '-1', '1.5', '1e3', '']) {
    await concurrency.fill(invalid)
    assert.equal(await concurrency.evaluate(input => input.validity.valid), false, `accepted invalid concurrency ${invalid}`)
  }
  const largeConcurrency = '90071992547409931234567890'
  await concurrency.fill(largeConcurrency)
  await expect(concurrency).toHaveValue(largeConcurrency)
  assert.ok(await concurrency.evaluate(input => input.validity.valid))
  const launchURL = server.url + '/api/v1' + root + '/evaluation/experiments'
  let firstLaunch
  await page.route(launchURL, async route => {
    const response = await route.fetch()
    firstLaunch = { body: route.request().postDataJSON(), response: await response.json(), status: response.status() }
    await route.fulfill({ status: 503, json: { code: 'runtime_unavailable' } })
  }, { times: 1 })
  const launchButton = experimentDialog.getByRole('button', { name: '发起实验', exact: true })
  await launchButton.click()
  await expect(page.getByRole('alert')).toContainText('执行服务暂不可用')
  assert.equal(firstLaunch.status, 202)
  assert.match(firstLaunch.body.idempotency_key, /^[0-9a-f]{32}$/)

  const retrying = page.waitForResponse(r => r.request().method() === 'POST' && r.url() === launchURL)
  await launchButton.click()
  const retry = await retrying
  assert.equal(retry.status(), 202)
  assert.deepEqual(retry.request().postDataJSON(), firstLaunch.body)
  const experiment = await retry.json()
  assert.equal(experiment.id, firstLaunch.response.id)
  await expect(page.getByRole('heading', { name: '实验 · 已结束' })).toBeVisible({ timeout: 30000 })
  await expect(page.getByRole('status')).toContainText('成功 2 · 失败 0')
  const result = await api('GET', `${root}/evaluation/experiments/${experiment.id}`)
  assert.equal(result.concurrency, 65535)
  assert.equal(result.dataset_version, 2)
  assert.equal(result.result['1'].run_ids.length, 2)
  assert.notEqual(result.result['1'].session_id, result.result['2'].session_id)
  assert.equal(result.result['1'].input_tokens, 200)
  assert.equal(result.result['1'].output_tokens, 20)
  assert.equal(result.result['1'].step_count, 2)
  await page.getByRole('button', { name: '用例 1', exact: true }).click()
  await expect(page.getByText('D06 first', { exact: true })).toBeVisible()
  await expect(page.getByText('D06 second', { exact: true })).toBeVisible()
  await mkdir('.cache', { recursive: true })
  await page.screenshot({ path: '.cache/evaluation-desktop.png', fullPage: true })
  await page.reload()
  await expect(page.getByRole('heading', { name: '实验 · 已结束' })).toBeVisible()
  await page.setViewportSize({ width: 390, height: 844 })
  await page.locator('.evaluation-table').scrollIntoViewIfNeeded()
  await page.screenshot({ path: '.cache/evaluation-mobile.png' })
  assert.ok(await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth + 1), 'mobile page overflows')
  await page.getByRole('link', { name: '查看会话', exact: true }).first().click()
  await expect(page.getByText('测评会话 · 只读回放', { exact: true })).toBeVisible()
  await expect(page.getByLabel('输入任务', { exact: true })).toHaveCount(0)
  assert.deepEqual((await api('GET', `${root}/agents/${agent.id}/sessions`)).items, [])
  await page.goto(`${server.url}${root}/evaluation/datasets/${dataset.id}?version=1`)
  await expect(page.getByRole('heading', { name: '查看评测集 · v1' })).toBeVisible()
  await expect(page.getByLabel('第 1 轮用户输入', { exact: true })).toHaveCount(1)
  await expect(page.getByLabel('第 1 轮用户输入', { exact: true })).toBeDisabled()
  await server.restart()
  const retained = await api('GET', `${root}/evaluation/experiments/${experiment.id}`)
  assert.equal(retained.status, 'completed')
  assert.deepEqual(retained.result, result.result)
  assert.deepEqual(errors, [])
  console.log('Evaluation browser smoke passed: versioned inputs, multi-turn runs, metrics, hidden replay, mobile layout and restart history.')
} catch (error) {
  if (page) {
    await mkdir('.cache', { recursive: true })
    await page.screenshot({ path: '.cache/evaluation-failure.png', fullPage: true })
    console.error(await page.locator('body').ariaSnapshot())
  }
  throw error
} finally { await browser?.close(); await server.stop() }
