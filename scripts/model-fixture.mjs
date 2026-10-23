// Test-only OpenAI-compatible SSE provider; production uses the configured endpoint.
import { createServer } from 'node:http'
import { once } from 'node:events'
import { createHash, randomUUID } from 'node:crypto'
export async function startModelFixture() {
  let calls = 0
  const server = createServer(async (req, res) => {
    if (req.method !== 'POST' || !req.url.endsWith('/chat/completions') || !req.headers.authorization) { res.writeHead(400); res.end(); return }
    let body
    try { const chunks = []; for await (const b of req) chunks.push(b); body = JSON.parse(Buffer.concat(chunks)) } catch { res.writeHead(400); res.end(); return }
    calls++
    const messages = body.messages ?? []; let task = '', index = 0
    for (const [i, message] of messages.entries()) if (message.role === 'user' && !message.content.startsWith('[MASS')) { task = message.content; index = i }
    const count = messages.slice(index + 1).filter(m => m.role === 'tool').length
    res.setHeader('Content-Type', 'text/event-stream'); res.flushHeaders()
    const frame = (delta, reason = null, usage = undefined) => res.write(`data: ${JSON.stringify({ id: 'fixture-completion', choices: [{ index: 0, delta, finish_reason: reason }], ...(usage ? { usage } : {}) })}\n\n`)
    const usage = { prompt_tokens: 100, completion_tokens: 10, total_tokens: 110, prompt_tokens_details: { cached_tokens: 0 } }
    if (/wait|等待/.test(task)) {
      frame({ role: 'assistant', content: '正在等待取消或重启。' })
      const timer = setTimeout(() => { if (!res.destroyed) { frame({}, 'stop', usage); res.end('data: [DONE]\n\n') } }, 30000)
      res.on('close', () => clearTimeout(timer)); return
    }
    const sha = createHash('sha256').update('hello workspace').digest('hex')
    let name, args
    if (/second|读取已有/.test(task)) { if (!count) { name = 'read_file'; args = { path: 'notes.txt' } } }
    else if (count === 0) { name = 'write_file'; args = { path: 'notes.txt', content: 'hello workspace', media_type: 'text/plain', expected_sha256: 'missing' } }
    else if (count === 1) { name = 'read_file'; args = { path: 'notes.txt' } }
    else if (count === 2) { name = 'publish_artifact'; args = { path: 'notes.txt', expected_sha256: sha, display_name: 'notes.txt', media_type: 'text/plain' } }
    if (name) frame({ role: 'assistant', tool_calls: [{ index: 0, id: randomUUID(), type: 'function', function: { name, arguments: JSON.stringify(args) } }] }, 'tool_calls', usage)
    else { frame({ role: 'assistant', content: /second|读取已有/.test(task) ? '已读取已有文件：hello workspace' : '已完成写入、读取和发布 notes.txt' }); frame({}, 'stop', usage) }
    res.end('data: [DONE]\n\n')
  })
  server.listen(0, '127.0.0.1'); await once(server, 'listening')
  return { endpoint: `http://127.0.0.1:${server.address().port}/v1`, get calls() { return calls }, stop: () => new Promise((resolve, reject) => server.close(e => e ? reject(e) : resolve())) }
}
