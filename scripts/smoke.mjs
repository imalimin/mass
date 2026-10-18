import assert from 'node:assert/strict'
import { spawnSync } from 'node:child_process'
import { resolve } from 'node:path'
import { startFixtureServer } from './fixture-server.mjs'

const server = await startFixtureServer()
try {
  for (const [path, status, contentType] of [['/', 200, 'text/html'], ['/spaces', 200, 'text/html'], ['/api/v1/system', 200, 'application/json'], ['/health/live', 200, 'application/json'], ['/health/ready', 503, 'application/json'], ['/api/v1/missing', 404, 'application/json'], ['/assets/missing.js', 404, 'application/json']]) {
    const response = await fetch(server.url + path)
    assert.equal(response.status, status, path)
    assert.ok(response.headers.get('content-type').includes(contentType), path)
    if (path === '/') {
      const html = await response.text()
      const asset = html.match(/src="([^"]+\.js)"/)?.[1]
      assert.ok(asset, 'embedded script is present')
      assert.equal((await fetch(server.url + asset)).status, 200)
    }
    if (path === '/health/ready') assert.equal((await response.json()).code, 'storage_unavailable')
  }
} finally { await server.stop() }
const missing = spawnSync(resolve('bin/mass'), ['check-config', '--config', 'configs/mass.example.yaml'], { env: { PATH: '' }, encoding: 'utf8' })
assert.notEqual(missing.status, 0, 'missing required environment must fail')
assert.match(missing.stderr, /requires a nonempty environment value/)
console.log('D02 process smoke passed: embedded UI/API, honest readiness, no runtime Node/PATH dependency, clean SIGTERM, missing-config failure.')
