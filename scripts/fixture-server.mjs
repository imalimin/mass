import { spawn, spawnSync } from 'node:child_process'
import { once } from 'node:events'
import { mkdtemp, readFile, writeFile, rm } from 'node:fs/promises'
import { tmpdir } from 'node:os'
import { join, resolve } from 'node:path'
import { createServer } from 'node:net'
import { startTOSFixture } from './tos-fixture.mjs'
import { startModelFixture } from './model-fixture.mjs'

export async function startFixtureServer({ storage = false, model = false, binary = resolve('bin/mass'), template = 'configs/mass.example.yaml', emptyDatabase = false, commandPath = '' } = {}) {
  if (!process.env.MASS_TEST_ADMIN_DSN) throw new Error('MASS_TEST_ADMIN_DSN must identify a disposable MariaDB test server')
  const directory = await mkdtemp(join(tmpdir(), 'mass-d02-'))
  const database = spawn('sh', ['scripts/go.sh', 'run', './scripts/test-database'], { env: { ...process.env, MASS_TEST_EMPTY_DATABASE: emptyDatabase ? '1' : '0' }, stdio: ['pipe', 'pipe', 'pipe'] })
  const databaseExited = once(database, 'exit')
  let tos, provider, child, exited, stopped = false, errors = '', fixtureError = ''
  database.stderr.setEncoding('utf8').on('data', chunk => { fixtureError += chunk })
  const stop = async () => {
    if (stopped) return
    stopped = true
    try {
      if (child) {
        if (child.exitCode === null && child.signalCode === null) child.kill('SIGTERM')
        const timeout = setTimeout(() => child.kill('SIGKILL'), 12000)
        try { const [code, signal] = await exited; if (code !== 0 || signal) throw new Error(`Unclean process shutdown: ${code} ${signal}; ${errors}`) } finally { clearTimeout(timeout) }
      }
    } finally {
      await tos?.stop()
      await provider?.stop()
      database.stdin.end()
      const [code] = await databaseExited
      await rm(directory, { recursive: true, force: true })
      if (code !== 0) throw new Error(`Test database cleanup failed: ${fixtureError}`)
    }
  }
  try {
    const connection = await new Promise((done, reject) => {
      let output = ''
      const timeout = setTimeout(() => reject(new Error('Database setup timed out')), 60000)
      database.on('error', error => { clearTimeout(timeout); reject(error) })
      database.once('exit', () => { clearTimeout(timeout); reject(new Error(`Database setup failed: ${fixtureError}`)) })
      database.stdout.setEncoding('utf8').on('data', chunk => { output += chunk; if (output.includes('\n')) { clearTimeout(timeout); try { done(JSON.parse(output.split('\n')[0])) } catch { reject(new Error('Invalid database fixture response')) } } })
    })
    const probe = createServer(); probe.listen(0, '127.0.0.1'); await once(probe, 'listening')
    const port = probe.address().port; await new Promise(resolveClose => probe.close(resolveClose))
    const url = `http://127.0.0.1:${port}`
    const config = (await readFile(template, 'utf8')).replaceAll('127.0.0.1:8080', `127.0.0.1:${port}`).replace('/tmp/mass-workspaces', join(directory, 'workspaces'))
    const configPath = join(directory, 'mass.yaml'); await writeFile(configPath, config)
    if (storage) tos = await startTOSFixture()
    if (model) provider = await startModelFixture()
    const env = {
      PATH: commandPath, MASS_MARIADB_DSN: connection.dsn, MASS_MARIADB_MIGRATION_DSN: connection.migration_dsn,
      MASS_TOS_ENDPOINT: tos?.endpoint ?? 'offline-fixture', MASS_TOS_REGION: 'offline-fixture', MASS_TOS_BUCKET: 'offline-fixture',
      MASS_TOS_ACCESS_KEY: 'offline-fixture', MASS_TOS_SECRET_KEY: 'offline-fixture', MASS_MASTER_KEY: Buffer.alloc(32, 5).toString('base64'),
    }
    const cli = args => { const result = spawnSync(binary, [...args, '--config', configPath], { cwd: directory, env, encoding: 'utf8' }); if (result.status !== 0) throw new Error(`Fixture CLI failed: ${result.stderr}`); return result.stdout }
    cli(['migrate'])
    for (const username of ['alice', 'bob']) {
      env.MASS_TEST_PASSWORD = `correct-password-${username}`
      cli(['user', 'create', '--username', username, '--password-env', 'MASS_TEST_PASSWORD'])
    }
    delete env.MASS_TEST_PASSWORD; delete env.MASS_MARIADB_MIGRATION_DSN
    const startApplication = async () => {
      child = spawn(binary, ['serve', '--config', configPath], { cwd: directory, env, stdio: ['ignore', 'pipe', 'pipe'] })
    exited = once(child, 'exit'); child.stderr.setEncoding('utf8').on('data', chunk => { errors += chunk })
    await new Promise((done, reject) => {
      const timeout = setTimeout(() => reject(new Error(`Server startup timed out: ${errors}`)), 10000)
      const fail = error => { clearTimeout(timeout); reject(error) }
      child.on('error', fail); child.once('exit', code => fail(new Error(`Server exited: ${code}; ${errors}`)))
      let output = ''; child.stdout.setEncoding('utf8').on('data', chunk => {
        output += chunk
        while (output.includes('\n')) { const end = output.indexOf('\n'), line = output.slice(0, end); output = output.slice(end + 1); try { if (JSON.parse(line).msg === 'MASS listening') { clearTimeout(timeout); done() } } catch { /* Ignore non-JSON logging. */ } }
      })
    })
    }
    await startApplication()
    const halt = async () => { if (!child) return; child.kill('SIGTERM'); const [code, signal] = await exited; if (code !== 0 || signal) throw Error(`Unclean maintenance shutdown: ${code} ${signal}`); child = undefined }
    return { url, stop, modelBaseURL: provider?.endpoint ?? 'https://ark.cn-beijing.volces.com/api/v3', modelCalls: () => provider?.calls ?? 0, modelRequests: () => provider?.requests ?? [], restart: async afterStop => { child.kill('SIGKILL'); await exited; await afterStop?.(); errors = ''; await startApplication() }, maintenance: { directory, connection, configPath, env, cli, halt, start: startApplication, tos } }
  } catch (error) { await stop().catch(() => {}); throw error }
}
