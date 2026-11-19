// Local delivery rehearsal: real MariaDB dump/restore, packaged Go binary,
// official TOS/model adapters against loopback fixtures. No cloud credentials.
import assert from 'node:assert/strict'
import { spawn, execFileSync } from 'node:child_process'
import { once } from 'node:events'
import { createHash, randomUUID } from 'node:crypto'
import { mkdir, mkdtemp, readFile, writeFile, rm } from 'node:fs/promises'
import { createRequire } from 'node:module'
import { tmpdir } from 'node:os'
import { join, resolve } from 'node:path'
import { startFixtureServer } from './fixture-server.mjs'

process.env.PLAYWRIGHT_BROWSERS_PATH ??= resolve('.cache/playwright')
const require = createRequire(new URL('../frontend/package.json', import.meta.url)), { chromium, expect } = require('@playwright/test')
const hash = bytes => createHash('sha256').update(bytes).digest('hex')
const platform = `${process.platform === 'win32' ? 'windows' : process.platform}-${process.arch === 'x64' ? 'amd64' : process.arch}`
const archive = resolve(`.cache/release/mass-${platform}.tar.gz`), originalHash = hash(await readFile(archive))
// Rebuild both frontend and Go binary; an identical source tree/toolchain/epoch
// must produce the same package, not just a second tar of the first binary.
execFileSync('make', ['release'], { stdio: 'pipe', timeout: 180000 })
assert.equal(hash(await readFile(archive)), originalHash, 'release is not reproducible')
const directory = await mkdtemp(join(tmpdir(), 'mass-d10-'))
let server, browser, page, target, targetExit, targetErrors = ''
try {
  execFileSync('tar', ['-xzf', archive, '-C', directory])
  const manifest = JSON.parse(await readFile(join(directory, 'manifest.json'), 'utf8'))
  for (const [name, digest] of Object.entries(manifest.sha256)) assert.equal(hash(await readFile(join(directory, name))), digest, name)
  const binary = join(directory, 'bin/mass'), info = JSON.parse(execFileSync(binary, ['build-info'], { env: { PATH: '' } }))
  assert.equal(info.schema, 17); assert.equal(info.revision, execFileSync('git', ['rev-parse', 'HEAD'], { encoding: 'utf8' }).trim())
  assert.equal((await readFile(archive+'.sha256', 'utf8')).split(' ')[0], originalHash)
  server = await startFixtureServer({ storage: true, model: true, binary, template: join(directory, 'mass.yaml'), emptyDatabase: true })
  const m = server.maintenance
  for (const path of ['/', '/spaces', '/health/live', '/health/ready', '/api/v1/system']) assert.equal((await fetch(server.url+path)).status, 200, path)
  const system = await (await fetch(server.url+'/api/v1/system')).json(); assert.equal(system.version, info.version)
  browser = await chromium.launch({ headless: true, channel: 'chromium' }); const context = await browser.newContext({ viewport: { width: 1440, height: 1100 } })
  page = await context.newPage(); const errors = []; page.on('pageerror', e => errors.push(e.message))
  await page.goto(server.url); await page.getByLabel('用户名', { exact: true }).fill('alice'); await page.getByLabel('密码', { exact: true }).fill('correct-password-alice'); await page.getByRole('button', { name: '登录', exact: true }).click()
  await page.getByRole('button', { name: '账号设置', exact: true }).click(); await page.getByRole('button', { name: '创建空间', exact: true }).click(); await page.getByLabel('新空间名称').fill('交付恢复验收'); await page.getByRole('button', { name: '创建空间', exact: true }).click()
  await page.locator('.app-sidebar').getByRole('link', { name: '模型配置', exact: true }).click()
  await page.getByLabel('模型名称', { exact: true }).fill('恢复模型'); await page.getByLabel('Ark 模型 ID').fill('fixture-model'); await page.getByLabel('Base URL', { exact: true }).fill(server.modelBaseURL); await page.getByLabel('API Key', { exact: true }).fill('fixture-provider-key'); await page.getByRole('button', { name: '创建模型', exact: true }).click(); await page.getByRole('button', { name: '编辑 恢复模型', exact: true }).waitFor()
  await page.locator('.app-sidebar').getByRole('link', { name: 'Skills', exact: true }).click()
  const edit = async (label, value) => { await page.locator(`[data-editor="${label}"] .view-lines`).click(); await page.keyboard.press(process.platform === 'darwin' ? 'Meta+A' : 'Control+A'); await page.keyboard.insertText(value) }
  const save = async () => { const response = page.waitForResponse(r => r.url().endsWith('/draft') && r.request().method() === 'PUT'); await page.getByRole('button', { name: '保存草稿', exact: true }).click(); assert.equal((await response).status(), 200) }
  const publish = async () => { const response = page.waitForResponse(r => r.url().endsWith('/versions') && r.request().method() === 'POST'); await page.getByRole('button', { name: '发布版本', exact: true }).click(); const r = await response; assert.equal(r.status(), 201); return r.json() }
  await page.getByLabel('定义名称').fill('retained-skill'); await page.getByRole('button', { name: '创建 Skill', exact: true }).click(); await edit('SKILL.md', '# Retained skill\nPreserve notes across runs.'); await save()
  await page.getByText('添加资源', { exact: true }).click()
  await page.getByLabel('资源文件（最多 8 MiB）').setInputFiles({ name: 'rules.txt', mimeType: 'text/plain', buffer: Buffer.from('retained rule bytes') }); const upload = page.waitForResponse(r => r.url().endsWith('/resources')); await page.getByRole('button', { name: '上传资源', exact: true }).click(); assert.equal((await upload).status(), 200)
  const skill = await publish()
  await page.locator('.app-sidebar').getByRole('link', { name: 'Agents', exact: true }).click(); await page.getByLabel('定义名称').fill('delivery-agent'); await page.getByRole('button', { name: '创建 Agent', exact: true }).click(); await edit('AGENTS.md', '# Delivery agent\nRead and write session files.')
  await page.getByLabel('绑定模型').selectOption({ label: '恢复模型' }); await page.getByLabel('Skill 版本：retained-skill', { exact: true }).selectOption('v1'); const agent = await publish(); assert.equal(agent.bundle.bindings[0].version_id, skill.id)
  await page.locator('.app-sidebar').getByRole('link', { name: 'Agents', exact: true }).click(); await page.getByRole('link', { name: '打开 delivery-agent 的会话', exact: true }).click()
  const firstAccepted = page.waitForResponse(r => /\/sessions\/[^/]+\/runs$/.test(r.url()) && r.request().method() === 'POST')
  await page.getByLabel('输入任务', { exact: true }).fill('write, read and publish'); await page.getByRole('button', { name: '发送任务', exact: true }).click()
  const first = await (await firstAccepted).json(); await expect(page.getByTestId('run-status')).toContainText('已完成', { timeout: 15000 })
  const space = first.space_id, session = first.session_id, headers = { Origin: server.url, 'Content-Type': 'application/json' }, base = `${server.url}/api/v1/spaces/${space}`
  const send = async text => { const r = await context.request.post(`${base}/sessions/${session}/runs`, { headers, data: { idempotency_key: randomUUID(), text } }); assert.equal(r.status(), 202); const run = await r.json(); for (let i=0;i<600;i++) { const v = await (await context.request.get(`${base}/runs/${run.id}`)).json(); if (v.status === 'succeeded') return v; if (['failed','cancelled'].includes(v.status)) throw Error(`Run failed: ${JSON.stringify(v)}`); await new Promise(r => setTimeout(r,50)) }; throw Error('run timed out') }
  await page.goto(`${server.url}/spaces?space=${space}&session=${session}&run=${first.id}`); await expect(page.getByTestId('run-status')).toContainText('已完成', { timeout: 15000 }); await page.getByText('Trace 与上下文', { exact: true }).click(); await page.getByText('model.attempt.started', { exact: false }).first().waitFor()
  const artifact = (await (await context.request.get(`${base}/runs/${first.id}/artifacts`)).json()).items[0]
  assert.equal(await (await context.request.get(server.url+artifact.download_url)).text(), 'hello workspace')
  const diagnostics = await (await context.request.get(`${server.url}/api/v1/diagnostics?run_id=${first.id}`)).json()
  assert.equal(diagnostics.items.at(-1).status, 'succeeded')
  assert.ok([...m.tos.objects.keys()].some(key => key.endsWith(`/${session}/session.jsonl`)))
  const history = await (await context.request.get(`${base}/runs/${first.id}/ui-snapshot`)).json(), calls = server.modelCalls()
  // Provision only an independently randomized, empty test database.
  target = spawn('sh', ['scripts/go.sh', 'run', './scripts/test-database'], { env: { ...process.env, MASS_TEST_EMPTY_DATABASE: '1' }, stdio: ['pipe','pipe','pipe'] }); targetExit = once(target, 'exit'); target.stderr.setEncoding('utf8').on('data', b => { targetErrors += b })
  const destination = await new Promise((done, reject) => { let text=''; const timer=setTimeout(()=>reject(Error('target database setup timed out')),60000); target.once('error', reject); target.once('exit',()=>reject(Error('target database setup failed'))); target.stdout.setEncoding('utf8').on('data', b => { text+=b; if(text.includes('\n')) { clearTimeout(timer); try { done(JSON.parse(text.split('\n')[0])) } catch(e) { reject(e) } } }) })
  const options = join(directory,'source.cnf'), targetOptions = join(directory,'target.cnf'); await writeFile(options,m.connection.client_options,{mode:0o600}); await writeFile(targetOptions,destination.client_options,{mode:0o600})
  const client = process.env.MASS_TEST_MARIADB_CLIENT ?? 'mariadb', dump = process.env.MASS_TEST_MARIADB_DUMP ?? 'mariadb-dump'
  const query = (db, sql) => execFileSync(client,[`--defaults-file=${targetOptions}`,'--batch','--skip-column-names',db,'-e',sql],{encoding:'utf8'}).trim()
  assert.equal(query(destination.name,'SELECT COUNT(*) FROM information_schema.tables WHERE table_schema=DATABASE()'), '0', 'restore target is not empty')
  await m.halt()
  const backup = execFileSync(dump,[`--defaults-file=${options}`,'--single-transaction','--hex-blob','--skip-add-drop-table','--skip-add-locks','--skip-comments','--skip-dump-date','--no-tablespaces',m.connection.name],{maxBuffer:64*1024*1024}); await writeFile(join(directory,'database.sql'),backup,{mode:0o600})
  execFileSync(client,[`--defaults-file=${targetOptions}`,destination.name],{input:backup})
  const sourceConfig = await readFile(m.configPath,'utf8'), restoredConfig = sourceConfig.replace('MASS_MARIADB_DSN','MASS_RESTORE_DSN').replace('prefix: "mass/"',`prefix: "restore-d10/${randomUUID()}/"`).replace(join(m.directory,'workspaces'),join(m.directory,'restored-workspaces'))
  const sourcePath = join(directory,'original.yaml'); await writeFile(sourcePath,sourceConfig.replace('bucket_env: "MASS_TOS_BUCKET"','bucket_env: "MASS_BACKUP_BUCKET"')); m.env.MASS_BACKUP_BUCKET='retained-backup';
  // Retain a separate bucket copy, then remove the original objects before
  // restore. Original logical URIs remain in the SQL backup.
  for (const [key,bytes] of [...m.tos.objects.entries()]) { assert.ok(key.startsWith('/offline-fixture/')); m.tos.objects.set(key.replace('/offline-fixture/','/retained-backup/'),Buffer.from(bytes)); m.tos.objects.delete(key) }; const restoredPath = join(directory,'restored.yaml'); await writeFile(restoredPath,restoredConfig); m.env.MASS_RESTORE_DSN=destination.dsn
  const mass = async (args, extra={}) => { const child=spawn(binary,[...args,'--config',restoredPath],{env:{...m.env,...extra},cwd:directory,stdio:['ignore','pipe','pipe']}); let stdout='',stderr=''; child.stdout.setEncoding('utf8').on('data',b=>{stdout+=b}); child.stderr.setEncoding('utf8').on('data',b=>{stderr+=b}); const timer=setTimeout(()=>child.kill('SIGKILL'),60000); try { const [status,signal]=await once(child,'exit'); return {status,signal,stdout,stderr} } finally { clearTimeout(timer) } }
  const restoreArgs = ['restore-storage','--source-config',sourcePath,'--expect-database',destination.name]
  assert.notEqual((await mass([...restoreArgs.slice(0,-1),'wrong-database'])).status,0)
  const beforeWrongKey=m.tos.objects.size; const wrong=await mass(restoreArgs,{MASS_MASTER_KEY:Buffer.alloc(32,9).toString('base64')}); assert.notEqual(wrong.status,0); assert.match(wrong.stderr,/master key/); assert.equal(m.tos.objects.size,beforeWrongKey)
  query(destination.name,`UPDATE runs SET status='queued' WHERE id='${first.id}'`); const queued=await mass(restoreArgs); assert.notEqual(queued.status,0); assert.match(queued.stderr,/quiescent backup/); query(destination.name,`UPDATE runs SET status='succeeded' WHERE id='${first.id}'`)
  assert.equal(m.tos.objects.size,beforeWrongKey,'invalid backup wrote destination objects')
  const sourceKey=[...m.tos.objects.keys()].find(k=>k.endsWith('.tar.gz')), originalBytes=m.tos.objects.get(sourceKey)
  m.tos.objects.set(sourceKey,Buffer.from('corrupt backup bytes')); assert.notEqual((await mass(restoreArgs)).status,0)
  assert.ok(![...m.tos.objects.keys()].some(k=>k.includes('/restore-d10/') && k.endsWith('/session.jsonl')))
  m.tos.objects.set(sourceKey,originalBytes)
  const copied=await mass(restoreArgs); assert.equal(copied.status,0,copied.stderr)
  const targetKeys=[...m.tos.objects.keys()].filter(k=>k.includes('/restore-d10/'))
  const objectCount=targetKeys.length
  assert.match(copied.stdout,new RegExp(`relocated ${objectCount} objects`))
  assert.equal(targetKeys.filter(k=>k.endsWith('/session.jsonl')).length, 1)
  const destinationKey=targetKeys.find(k=>k.endsWith('.tar.gz')), destinationBytes=m.tos.objects.get(destinationKey); m.tos.objects.set(destinationKey,Buffer.from('corrupt destination')); assert.notEqual((await mass(restoreArgs)).status,0,'corrupt destination accepted'); m.tos.objects.set(destinationKey,destinationBytes)
  const sourceObjects=[...m.tos.objects.keys()].filter(k=>!k.includes('/restore-d10/')); for(const key of sourceObjects) m.tos.objects.delete(key)
  assert.equal((await mass(restoreArgs)).status,0,'idempotent relocation required original bytes after commit')
  await rm(join(m.directory,'workspaces'),{recursive:true,force:true}); await writeFile(m.configPath,restoredConfig); await m.start()
  assert.equal((await fetch(server.url+'/health/ready')).status,200)
  const busy=await mass(restoreArgs); assert.notEqual(busy.status,0); assert.match(busy.stderr,/stopped/)
  assert.equal(server.modelCalls(),calls,'restore/history replay called model')
  assert.deepEqual((await (await context.request.get(`${base}/runs/${first.id}/ui-snapshot`)).json()).state.messages, history.state.messages)
  assert.equal(await (await context.request.get(server.url+artifact.download_url)).text(),'hello workspace')
  const restoredVersion=await context.request.get(`${base}/agents/${agent.definition_id}/versions/${agent.id}`); assert.equal(restoredVersion.status(),200)
  await page.reload(); await page.getByRole('heading',{name:session,exact:true}).waitFor(); await page.getByText('已完成写入、读取和发布 notes.txt',{exact:true}).waitFor(); assert.equal(server.modelCalls(),calls)
  // A fresh Run must decrypt the restored credential and read the snapshot with
  // both the original temporary workspace and original TOS objects removed.
  const second = await send('second read after backup restore'); await page.goto(`${server.url}/spaces?space=${space}&session=${session}&run=${second.id}`); await page.getByText('已读取已有文件：hello workspace',{exact:true}).waitFor()
  assert.equal(server.modelCalls(),calls+2)
  await mkdir('.cache',{recursive:true}); await page.screenshot({path:'.cache/d10-restored.png',fullPage:true}); assert.deepEqual(errors,[])
  await m.halt()
  // Maintenance member CLI uses owner authorization and writes the audit log.
  const owner=query(destination.name,"SELECT id FROM users WHERE username='alice'"), bob=query(destination.name,"SELECT id FROM users WHERE username='bob'")
  assert.equal((await mass(['member','add','--actor',owner,'--space',space,'--username','bob'])).status,0)
  assert.notEqual((await mass(['member','remove','--actor',bob,'--space',space,'--user',owner])).status,0)
  assert.equal((await mass(['member','remove','--actor',owner,'--space',space,'--user',bob])).status,0)
  const report={revision:info.revision,build:info,toolchain:{node:process.version,npm:execFileSync('npm',['--version'],{encoding:'utf8'}).trim(),mariadb:query(destination.name,'SELECT VERSION()')},archive_sha256:originalHash,objects:objectCount,database_dump_sha256:hash(backup),source:'loopback model/TOS fixtures; isolated MariaDB',real_cloud:false,reproducible:true,history_without_execution:true,continued_from_restored_snapshot:true}
  await writeFile('.cache/d10-delivery.json',JSON.stringify(report,null,2)+'\n')
  console.log('D10 delivery passed: reproducible package, empty database migrations, packaged binary with empty PATH, Skill/Agent publish, Trace/artifact, MariaDB dump/restore, verified object relocation, wrong key/corruption/active-service rejection, history rebuild without model, continued snapshot after source deletion, member CLI and clean shutdown.')
} catch(error) { await mkdir('.cache',{recursive:true}); await page?.screenshot({path:'.cache/d10-failure.png',fullPage:true}); throw error }
finally { try { await browser?.close(); await server?.stop() } finally { try { if(target) { target.stdin.end(); const [code]=await targetExit; if(code!==0) throw Error(`target cleanup failed: ${targetErrors}`) } } finally { await rm(directory,{recursive:true,force:true}) } } }
