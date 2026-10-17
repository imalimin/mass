import { spawn } from 'node:child_process'
import { once } from 'node:events'
import { mkdtemp, readFile, writeFile, rm } from 'node:fs/promises'
import { tmpdir } from 'node:os'
import { join, resolve } from 'node:path'

// A foundation-only process fixture. These values are deliberately invalid
// cloud credentials; D00 does not contact storage or a real model.
export async function startFixtureServer() {
  const directory = await mkdtemp(join(tmpdir(), 'mass-d00-'))
  const config = (await readFile('configs/mass.example.yaml', 'utf8')).replace('127.0.0.1:8080', '127.0.0.1:0')
  const configPath = join(directory, 'mass.yaml')
  await writeFile(configPath, config)
  const env = {
    PATH: '', MASS_MARIADB_DSN: 'offline-fixture', MASS_TOS_ENDPOINT: 'offline-fixture',
    MASS_TOS_REGION: 'offline-fixture', MASS_TOS_BUCKET: 'offline-fixture', MASS_TOS_ACCESS_KEY: 'offline-fixture',
    MASS_TOS_SECRET_KEY: 'offline-fixture', MASS_LLM_MODEL: 'offline-fixture', MASS_LLM_API_KEY: 'offline-fixture',
    MASS_MASTER_KEY: Buffer.alloc(32).toString('base64'),
  }
  const child = spawn(resolve('bin/mass'), ['serve', '--config', configPath], { cwd: directory, env, stdio: ['ignore', 'pipe', 'pipe'] })
  let output = '', errors = ''
  child.stderr.setEncoding('utf8').on('data', chunk => { errors += chunk })
  const exited = once(child, 'exit')
  let stopped = false
  const stop = async () => {
    if (stopped) return
    stopped = true
    if (child.exitCode === null && child.signalCode === null) child.kill('SIGTERM')
    const timeout = setTimeout(() => child.kill('SIGKILL'), 12000)
    try {
      const [code, signal] = await exited
      if (code !== 0 || signal) throw new Error(`Unclean process shutdown: ${code} ${signal}; ${errors}`)
    } finally { clearTimeout(timeout); await rm(directory, { recursive: true, force: true }) }
  }
  try {
    const url = await new Promise((resolveURL, reject) => {
      const timeout = setTimeout(() => reject(new Error(`Server startup timed out: ${errors}`)), 10000)
      const fail = error => { clearTimeout(timeout); reject(error) }
      child.on('error', fail)
      child.once('exit', code => fail(new Error(`Server exited during startup: ${code}; ${errors}`)))
      child.stdout.setEncoding('utf8').on('data', chunk => {
        output += chunk
        let lineEnd
        while ((lineEnd = output.indexOf('\n')) >= 0) {
          const line = output.slice(0, lineEnd); output = output.slice(lineEnd + 1)
          try {
            const event = JSON.parse(line)
            if (event.msg === 'MASS listening') { clearTimeout(timeout); resolveURL(`http://${event.address}`) }
          } catch { /* Other log lines do not determine readiness. */ }
        }
      })
    })
    return { url, stop }
  } catch (error) { await stop().catch(() => {}); throw error }
}
