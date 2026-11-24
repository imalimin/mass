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
    const cachedTokens = task === 'second read' && count > 0 ? 40 : 0
    const usage = { prompt_tokens: 100, completion_tokens: 10, total_tokens: 110, prompt_tokens_details: { cached_tokens: cachedTokens } }
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
      const systemMsg = messages.find(m => m.role === 'system')?.content ?? ''
      if (!systemMsg.includes('<skill_resources>\n- rules.txt')) throw new Error('missing <skill_resources> in system prompt')
      const binding = systemMsg.match(/<skill id="([^"]+)"/)?.[1]
      const attachments = JSON.parse(task.split('[MASS accepted attachments; use read_attachment by ID: ')[1].slice(0, -1))
      const steps = status => [{ id: 'report', title: 'Create summary', status }]
      const actions = [
        () => ['read_skill_resource', { skill_binding_id: binding, path: 'rules.txt' }],
        () => ['read_attachment', { attachment_id: attachments[0].id, format: 'json' }],
        () => ['write_file', { path: 'records.json', content: JSON.stringify(data(1).value) }],
        () => ['read_file', { path: 'records.json' }],
        () => ['update_plan', { steps: steps('in_progress') }],
        () => {
          const rows = JSON.parse(data(3).content)
          const total = rows.reduce((sum, r) => sum + r.amount, 0) * Number(data(0).content)
          return ['write_file', { path: 'summary.json', content: JSON.stringify({ total, label: 'draft' }) }]
        },
        () => ['edit_file', { path: 'summary.json', edits: [{ old_text: 'draft', new_text: 'final' }] }],
        () => ['run_command', { command: `mkdir -p reports && mv summary.json reports/summary.json && curl -fsS -X POST -d 10 http://127.0.0.1:${server.address().port}/tools-input` }],
        () => ['publish_artifact', { path: 'reports/summary.json' }],
        () => ['run_command', { command: 'rm -f reports/summary.json' }],
        () => ['update_plan', { steps: steps('completed') }],
      ]
      if (actions[count]) [name, args] = actions[count]()
    }
    else if (task === 'run a host command') {
      const results = messages.slice(index + 1).filter(m => m.role === 'tool').map(m => JSON.parse(m.content))
      const actions = [
        ['run_command', { command: 'printf problem >&2; exit 7' }],
        ['run_command', { command: 'sh', args: ['-c', 'printf "%s" "$1" > command.txt', 'mass', 'hello workspace'] }],
        ['publish_artifact', { path: 'command.txt', expected_sha256: sha, display_name: 'command.txt', media_type: 'text/plain' }],
      ]
      if (count === 1 && (results[0].data.exit_code !== 7 || results[0].data.stderr !== 'problem')) throw new Error('missing command failure details')
      if (actions[count]) [name, args] = actions[count]
    }
    else if (task === '帮我清理当前工作区的非产物中间文件') {
      const results = messages.slice(index + 1).filter(m => m.role === 'tool').map(m => JSON.parse(m.content))
      const retried = messages.slice(0, index).some(message => message.role === 'user' && message.content === task)
      const scratch = retried ? 'scratch-2.txt' : 'scratch.txt'
      const actions = [
        ['read_file', { path: `configs/${scratch}` }],
        ['run_command', { command: `rm -f ${scratch}` }],
      ]
      if (count === 1 && (results[0].error !== 'workspace_symlink' || results[0].side_effect !== 'none')) throw new Error('expected recoverable symlink access error')
      if (actions[count]) [name, args] = actions[count]
    }
    else if (/second|读取已有/.test(task)) { if (!count) { name = 'read_file'; args = { path: 'notes.txt' } } }
    else if (count === 0) { name = 'write_file'; args = { path: 'notes.txt', content: 'hello workspace' } }
    else if (count === 1) { name = 'read_file'; args = { path: 'notes.txt' } }
    else if (count === 2) { name = 'publish_artifact'; args = { path: 'notes.txt', expected_sha256: sha, display_name: 'notes.txt', media_type: 'text/plain' } }
    if (name) {
      const progress = task === 'write, read and publish' ? ['准备工作区。\n先写入文件。', '检查刚写入的文件。'][count] : undefined
      if (progress) frame({ role: 'assistant', content: progress })
      frame({ role: 'assistant', tool_calls: [{ index: 0, id: task.startsWith('D05') ? 'reused-native-id' : randomUUID(), type: 'function', function: { name, arguments: JSON.stringify(args) } }] }, 'tool_calls', usage)
    }
    else { frame({ role: 'assistant', content: task.startsWith('D05') ? '核心工具处理完成，汇总结果为 10。' : task === 'run a host command' ? '已运行宿主命令并发布 command.txt' : task === '帮我清理当前工作区的非产物中间文件' ? '已处理清理请求。' : /second|读取已有/.test(task) ? '已读取已有文件：hello workspace' : '已完成写入、读取和发布 notes.txt' }); frame({}, 'stop', usage) }
    res.end('data: [DONE]\n\n')
  })
  server.listen(0, '127.0.0.1'); await once(server, 'listening')
  return { endpoint: `http://127.0.0.1:${server.address().port}/v1`, get calls() { return calls }, get requests() { return requests }, stop: () => new Promise((resolve, reject) => server.close(e => e ? reject(e) : resolve())) }
}
