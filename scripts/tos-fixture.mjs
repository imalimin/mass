// Loopback protocol fixture only: exercises the official SDK used by bin/mass.
// Real cloud behavior is separately required by make smoke-tos.
import { createServer } from 'node:http'
import { once } from 'node:events'
import { createHash } from 'node:crypto'
export async function startTOSFixture() {
  const objects = new Map()
  let blockSessionFinish = false, blockedSessionFinishes = 0
  const server = createServer(async (req, res) => {
    res.setHeader('x-tos-request-id', 'local-test')
    if (req.method === 'HEAD') { res.writeHead(200); res.end(); return }
    const key = req.url.split('?')[0]
    if (req.method === 'PUT') {
      const mutable = ['/trace.jsonl', '/session.jsonl', '/catalog.json'].some(suffix => key.endsWith(suffix))
      if (!mutable && req.headers['x-tos-forbid-overwrite'] !== 'true') { res.writeHead(400); res.end('{}'); return }
      if (req.headers['x-tos-forbid-overwrite'] === 'true' && objects.has(key)) { res.writeHead(409); res.end('{"Code":"FileAlreadyExists"}'); return }
      const chunks = []; let size = 0
      for await (const chunk of req) { size += chunk.length; if (size > 32 * 1024 * 1024) { res.writeHead(413); res.end('{}'); return }; chunks.push(chunk) }
      const data = Buffer.concat(chunks), etag = createHash('md5').update(data).digest('hex')
      if (blockSessionFinish && key.endsWith('/session.jsonl') && JSON.parse(data.toString().trimEnd().split('\n').at(-1)).type === 'run_finished') {
        blockedSessionFinishes++
        res.writeHead(503); res.end('{"Code":"ServiceUnavailable"}'); return
      }
      objects.set(key, data); res.setHeader('ETag', `"${etag}"`); res.writeHead(200); res.end('{}'); return
    }
    if (req.method === 'GET' && objects.has(key)) { const data = objects.get(key); res.setHeader('Content-Length', data.length); res.writeHead(200); res.end(data); return }
    res.writeHead(404); res.end('{"Code":"NoSuchKey"}')
  })
  server.listen(0, '127.0.0.1'); await once(server, 'listening')
  return {
    endpoint: `http://127.0.0.1:${server.address().port}`, objects,
    blockSessionFinish: blocked => { blockSessionFinish = blocked },
    blockedSessionFinishes: () => blockedSessionFinishes,
    stop: () => new Promise((resolve, reject) => server.close(e => { objects.clear(); e ? reject(e) : resolve() })),
  }
}
