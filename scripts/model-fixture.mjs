// Test-only OpenAI-compatible SSE provider; production uses the configured endpoint.
import { createServer } from 'node:http'
import { once } from 'node:events'
import { createHash, randomUUID } from 'node:crypto'
export async function startModelFixture() {
  let calls = 0
  const requests = []
  const server = createServer(async (req, res) => {
    if (req.method === 'POST' && req.url === '/tools-input') {
      let value = ''; for await (const b of req) value += b
      res.writeHead(value === '10' ? 201 : 400, { 'Content-Type': 'application/json' }); res.end('{"accepted":true}'); return
    }
    if (req.method !== 'POST' || !req.url.endsWith('/chat/completions') || !req.headers.authorization) { res.writeHead(400); res.end(); return }
    let body
    try { const chunks = []; for await (const b of req) chunks.push(b); body = JSON.parse(Buffer.concat(chunks)) } catch { res.writeHead(400); res.end(); return }
    calls++
    requests.push(body)
    const messages = body.messages ?? []; let task = '', index = 0
    for (const [i, message] of messages.entries()) if (message.role === 'user' && !message.content.startsWith('[MASS')) { task = message.content; index = i }
    const count = messages.slice(index + 1).filter(m => m.role === 'tool').length
    res.setHeader('Content-Type', 'text/event-stream'); res.flushHeaders()
    const frame = (delta, reason = null, usage = undefined) => res.write(`data: ${JSON.stringify({ id: 'fixture-completion', choices: [{ index: 0, delta, finish_reason: reason }], ...(usage ? { usage } : {}) })}\n\n`)
    const usage = { prompt_tokens: 100, completion_tokens: 10, total_tokens: 110, prompt_tokens_details: { cached_tokens: 0 } }
    if (messages[0]?.content?.startsWith('Summarize this historical transcript') || task.startsWith('D06')) {
      const summary = messages[0]?.content?.startsWith('Summarize this historical transcript')
      if (summary && body.tools?.length) throw new Error('summary bound tools')
      frame({ role: 'assistant', content: summary ? 'D06 saved history: earlier reports completed; no tool effects.' : task.includes('long') ? 'report fact. '.repeat(60) : 'D06 continued.' }, 'stop', usage)
      res.end('data: [DONE]\n\n'); return
    }
    if (/wait|等待/.test(task) || task.startsWith('D09 crash') && count > 0) {
      frame({ role: 'assistant', content: '正在等待取消或重启。' })
      const timer = setTimeout(() => { if (!res.destroyed) { frame({}, 'stop', usage); res.end('data: [DONE]\n\n') } }, 30000)
      res.on('close', () => clearTimeout(timer)); return
    }
    const sha = createHash('sha256').update('hello workspace').digest('hex')
    let name, args
    if (task.startsWith('D05')) {
      const results = messages.slice(index + 1).filter(m => m.role === 'tool').map(m => JSON.parse(m.content))
      const failed = results.find(r => r.status !== 'completed'); if (failed) throw new Error(`D05 tool failed: ${JSON.stringify(failed)}`)
      const data = i => results[i]?.data ?? {}
      const binding = messages.find(m => m.role === 'system').content.match(/<skill id="([^"]+)"/)?.[1]
      const attachments = JSON.parse(task.split('[MASS accepted attachments; use read_attachment by ID: ')[1].slice(0, -1))
      const hash = i => data(i).sha256
      const steps = status => [{ id: 'report', title: 'Create summary', status }]
      const actions = [
        () => ['list_skill_resources', { skill_binding_id: binding, limit: 1 }],
        () => ['read_skill_resource', { skill_binding_id: binding, path: 'assets/rules.txt' }],
        () => ['read_attachment', { attachment_id: attachments[0].id, format: 'json' }],
        () => ['write_file', { path: 'records.json', content: JSON.stringify(data(2).value), media_type: 'application/json', expected_sha256: 'missing' }],
        () => ['read_file', { path: 'records.json', expected_sha256: hash(3) }],
        () => ['list_files', { directory: '.', limit: 1 }],
        () => ['glob_files', { pattern: '**/*.json' }],
        () => ['search_files', { pattern: 'amount', mode: 'literal', path_glob: '*.json' }],
        () => ['update_plan', { expected_revision: 0, steps: steps('in_progress') }],
        () => ['run_code', { language: 'javascript', input: { multiplier: data(1).content }, code: `let rows;for(let i=0;i<2;i++)rows=JSON.parse(fs.readFile('records.json').content);const total=rows.reduce((sum,r)=>sum+r.amount,0)*Number(input.multiplier);const csv=CSV.stringify([['total'],[String(total)]]);if(CSV.parse(csv)[1][0]!==String(total))throw Error('csv');fs.mkdir('reports');const written=fs.writeFile('summary.json',JSON.stringify({total,label:'draft'}),{expected_sha256:'missing',media_type:'application/json'});if(net.request({method:'POST',url:'http://127.0.0.1:${server.address().port}/tools-input',body:String(total)}).status!==201)throw Error('network');log({total});return {total,sha256:written.sha256};` }],
        () => ['edit_file', { path: 'summary.json', expected_sha256: data(9).value.sha256, edits: [{ old_text: 'draft', new_text: 'final' }] }],
        () => ['move_file', { source: 'summary.json', target: 'reports/summary.json', expected_sha256: hash(10) }],
        () => ['publish_artifact', { path: 'reports/summary.json', expected_sha256: hash(11), display_name: 'summary.json', media_type: 'application/json' }],
        () => ['delete_file', { path: 'reports/summary.json', expected_sha256: hash(11) }],
        () => ['update_plan', { expected_revision: 1, steps: steps('completed') }],
      ]
      if (actions[count]) [name, args] = actions[count]()
    }
    else if (/second|读取已有/.test(task)) { if (!count) { name = 'read_file'; args = { path: 'notes.txt' } } }
    else if (count === 0) { name = 'write_file'; args = { path: 'notes.txt', content: 'hello workspace', media_type: 'text/plain', expected_sha256: 'missing' } }
    else if (count === 1) { name = 'read_file'; args = { path: 'notes.txt' } }
    else if (count === 2) { name = 'publish_artifact'; args = { path: 'notes.txt', expected_sha256: sha, display_name: 'notes.txt', media_type: 'text/plain' } }
    if (name) frame({ role: 'assistant', tool_calls: [{ index: 0, id: task.startsWith('D05') ? 'reused-native-id' : randomUUID(), type: 'function', function: { name, arguments: JSON.stringify(args) } }] }, 'tool_calls', usage)
    else { frame({ role: 'assistant', content: task.startsWith('D05') ? '十四工具处理完成，汇总结果为 10。' : /second|读取已有/.test(task) ? '已读取已有文件：hello workspace' : '已完成写入、读取和发布 notes.txt' }); frame({}, 'stop', usage) }
    res.end('data: [DONE]\n\n')
  })
  server.listen(0, '127.0.0.1'); await once(server, 'listening')
  return { endpoint: `http://127.0.0.1:${server.address().port}/v1`, get calls() { return calls }, get requests() { return requests }, stop: () => new Promise((resolve, reject) => server.close(e => e ? reject(e) : resolve())) }
}
