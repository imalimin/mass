import { spawn, spawnSync } from 'node:child_process'
import { once } from 'node:events'
import { readdir, stat } from 'node:fs/promises'
import { resolve } from 'node:path'
import { setTimeout as sleep } from 'node:timers/promises'

// Polling works on macOS/Linux and mounted volumes. It watches source files,
// builds frontend assets and restarts the sole HTTP server at the Go address.
const config = resolve(process.argv[2] ?? 'mass.yaml')
let child, exiting = false
const stop = async () => {
  if (!child || child.exitCode !== null || child.signalCode !== null) return
  const exited = once(child, 'exit')
  child.kill('SIGTERM')
  const timer = setTimeout(() => child?.kill('SIGKILL'), 12000)
  await exited
  clearTimeout(timer)
}
for (const signal of ['SIGINT', 'SIGTERM']) process.on(signal, () => { exiting = true; void stop() })

async function fingerprint(file) {
  if (file === 'internal/transport/http/ui/dist') return ''
  try {
    const info = await stat(file)
    if (info.isDirectory()) return (await Promise.all((await readdir(file)).sort().map(name => fingerprint(`${file}/${name}`)))).join('|')
    return `${file}:${info.mtimeMs}:${info.size}`
  } catch { return `${file}:missing` }
}
let previous = ''
while (!exiting) {
  const current = (await Promise.all(['cmd', 'internal', 'api', 'db', 'frontend/src', 'frontend/index.html', 'frontend/vite.config.ts', 'frontend/package-lock.json', 'go.mod', 'go.sum', config].map(fingerprint))).join('|')
  if (current !== previous) {
    previous = current
    await stop()
    const build = spawnSync('make', ['build'], { stdio: 'inherit' })
    if (build.status === 0 && !exiting) {
      child = spawn(resolve('bin/mass'), ['serve', '--config', config], { stdio: 'inherit' })
      child.on('error', error => console.error(error.message))
    } else console.error('Build failed; waiting for a source change.')
  }
  await sleep(800)
}
await stop()
