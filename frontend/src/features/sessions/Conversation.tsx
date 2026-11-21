import { lazy, Suspense, useEffect, useLayoutEffect, useRef, useState } from 'react'
import { useNavigate, useSearchParams } from 'react-router-dom'
import { useInfiniteQuery, useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { agentSessionsPath } from '../../app/routes'
import { list, newRequestKey, request, type Schema } from '../../lib/api'
import { useRunStream } from './useRunStream'
import { CreativityPublish } from './CreativityPublish'

const Trace = lazy(() => import('../debug/Trace'))
const SafeMarkdown = lazy(() => import('../debug/SafeMarkdown'))
type Run = Schema['RuntimeRun']
type Attachment = Schema['RuntimeAttachment']
type Message = Schema['RuntimeMessage']
type Tool = Schema['UITool']
type Turn = { key: string; runID?: string; user?: Message; assistant: Message[] }
type DraftMessage = { text: string; files: File[]; key: string; workspaceCleanup?: boolean }
type SubmittedMessage = { run: Run; text: string; attachments: Attachment[] }
const cleanupText = '帮我清理当前工作区的非产物中间文件'
const statuses: Record<string, string> = { queued: '排队中', running: '运行中', succeeded: '已完成', failed: '失败', cancelled: '已取消' }
const toolStatuses: Record<string, string> = {
  parameters: '准备中', parameters_complete: '准备中', not_started: '待执行', running: '运行中',
  completed: '已完成', failed: '失败', cancelled: '已取消',
}
const isActive = (status?: string) => status === 'queued' || status === 'running'
const atBottom = (viewport: HTMLElement) => viewport.scrollHeight - viewport.scrollTop - viewport.clientHeight < 56

export function Conversation({ space, agent, sessionID, onCreated, initialPrompt, onInitialAccepted, enablePublish }: {
  space: string; agent: Pick<Schema['DefinitionDraft'], 'id' | 'name' | 'archived'>; sessionID: string
  onCreated?: (sessionID: string) => void
  initialPrompt?: { text: string; key: string }
  onInitialAccepted?: () => void
  enablePublish?: boolean
}) {
  const [params, setParams] = useSearchParams(), chosenRun = params.get('run') ?? ''
  const [text, setText] = useState(initialPrompt?.text ?? ''), [files, setFiles] = useState<File[]>([])
  const [accepted, setAccepted] = useState<SubmittedMessage>()
  const [selectedVersion, setSelectedVersion] = useState<string>()
  const [createdSession, setCreatedSession] = useState<Schema['RuntimeSession']>()
  const navigate = useNavigate()
  const [showDetails, setShowDetails] = useState(!!chosenRun)
  const [generation, setGeneration] = useState(0)
  const [publishing, setPublishing] = useState(false)
  const client = useQueryClient(), base = `/spaces/${space}`, sessionURL = `${base}/sessions/${sessionID}`
  const sessionKey = ['private', space, sessionID], runsKey = [...sessionKey, 'runs'], messagesKey = [...sessionKey, 'messages']
  const viewport = useRef<HTMLDivElement>(null), content = useRef<HTMLDivElement>(null), follow = useRef(true)
  const [showJump, setShowJump] = useState(false)
  const olderScroll = useRef<{ top: number; height: number; pageCount: number } | null>(null)
  const composer = useRef<HTMLTextAreaElement>(null), fileInput = useRef<HTMLInputElement>(null)
  const requestKey = useRef<{ text: string; files: File[]; key: string } | null>(null)
  const initialAttempted = useRef(false)
  const cleanupKey = useRef<string | null>(null)
  const createKey = useRef<{ version: string; key: string } | null>(null)
  const uploaded = useRef(new WeakMap<File, Attachment>())

  const session = useQuery({
    queryKey: [...sessionKey, 'session'],
    queryFn: ({ signal }) => request<Schema['RuntimeSession']>(sessionURL, 'GET', undefined, signal),
    enabled: !!sessionID,
  })
  const readOnly = !!session.data?.hide
  const ownsSession = session.data?.definition_id === agent.id
  const versions = useQuery({
    queryKey: ['private', space, 'run-agent-versions', agent.id],
    queryFn: async ({ signal }) => {
      const path = `${base}/agents/${agent.id}`
      const [published, debug] = await Promise.all([
        list<Schema['DefinitionVersion']>(`${path}/versions`, signal),
        list<Schema['DefinitionVersion']>(`${path}/debug-snapshots`, signal),
      ])
      // The API serializes UTC timestamps. Compare without Z to keep fractional seconds in order.
      published.sort((a, b) => b.created_at.slice(0, -1).localeCompare(a.created_at.slice(0, -1)) || b.id.localeCompare(a.id))
      return [...published, ...debug].filter(version => !version.archived)
    },
    enabled: !sessionID && !agent.archived,
  })
  const latestPublished = versions.data?.find(version => !version.debug)
  const versionID = createdSession?.version_id ?? selectedVersion ?? params.get('version') ?? latestPublished?.id ?? ''
  const runs = useQuery({
    queryKey: runsKey,
    queryFn: ({ signal }) => list<Run>(`${sessionURL}/runs`, signal),
    enabled: ownsSession,
    refetchInterval: q => q.state.error ? false : 5000,
  })
  const sorted = [...(runs.data ?? [])].sort((a, b) => b.created_at.localeCompare(a.created_at) || b.id.localeCompare(a.id))
  const latest = sorted[0]
  const current = useQuery({
    queryKey: [...sessionKey, 'run', latest?.id],
    queryFn: ({ signal }) => request<Run>(`${base}/runs/${latest.id}`, 'GET', undefined, signal),
    enabled: ownsSession && !!latest,
    refetchInterval: q => q.state.error || q.state.data && !isActive(q.state.data.status) ? false : 1500,
  })
  const stream = useRunStream(space, sessionID, ownsSession ? latest?.id ?? '' : '', generation)
  const currentRun = current.data ?? latest
  const streamStatus = stream.state?.status
  const status = (currentRun && !isActive(currentRun.status)) || (streamStatus === 'queued' && currentRun?.status === 'running')
    ? currentRun?.status : streamStatus || currentRun?.status
  const stopRequested = !!(stream.state?.cancel_requested || currentRun?.cancel_requested)
  const busy = isActive(status)
  const messages = useInfiniteQuery({
    queryKey: messagesKey,
    initialPageParam: '0',
    queryFn: ({ signal, pageParam }) => request<Schema['RuntimeMessages']>(`${sessionURL}/messages?before_sequence=${pageParam}&limit=20`, 'GET', undefined, signal),
    getNextPageParam: page => page.has_more ? page.items[0]?.sequence : undefined,
    enabled: ownsSession,
  })

  // Persisted history catches up when a turn starts and when its stream closes.
  const runID = latest?.id
  const ended = !!status && !busy
  const settledRun = current.data && !isActive(current.data.status) ? current.data : undefined
  useEffect(() => {
    if (!settledRun) return
    client.setQueryData<Run[]>(['private', space, sessionID, 'runs'], previous =>
      previous?.map(run => run.id === settledRun.id ? settledRun : run))
  }, [client, space, sessionID, settledRun])
  useEffect(() => {
    if (!runID) return
    void client.invalidateQueries({ queryKey: ['private', space, sessionID, 'messages'] })
    if (ended) {
      void client.invalidateQueries({ queryKey: ['private', space, sessionID, 'session'] })
      void client.invalidateQueries({ queryKey: ['private', space, sessionID, 'workspace-files'] })
    }
  }, [client, space, sessionID, runID, ended])

  const [fileError, setFileError] = useState('')
  const submit = useMutation({
    mutationFn: async (draft: DraftMessage) => {
      if (draft.workspaceCleanup) {
        const run = await request<Run>(`${sessionURL}/workspace/cleanup`, 'POST', { idempotency_key: draft.key })
        return { run, attachments: [] }
      }
      let target = session.data ?? createdSession
      if (!target) {
        if (createKey.current?.version !== versionID) createKey.current = { version: versionID, key: newRequestKey() }
        target = await request<Schema['RuntimeSession']>(`${base}/sessions`, 'POST', {
          agent_id: agent.id, version_id: versionID, idempotency_key: createKey.current.key,
        })
        setCreatedSession(target)
        void client.invalidateQueries({ queryKey: ['private', space, 'sessions', agent.id] })
      }
      const attachments: Attachment[] = []
      const url = `${base}/sessions/${target.id}`
      for (const file of draft.files) {
        let attachment = uploaded.current.get(file)
        if (!attachment) {
          attachment = await uploadAttachment(url, file)
          uploaded.current.set(file, attachment)
        }
        attachments.push(attachment)
      }
      const run = await request<Run>(`${url}/runs`, 'POST', {
        text: draft.text, attachment_ids: attachments.map(a => a.id), idempotency_key: draft.key,
      })
      return { target, run, attachments }
    },
    onSuccess: ({ target, run, attachments }, draft) => {
      if (initialPrompt && !draft.workspaceCleanup) onInitialAccepted?.()
      setAccepted({ run, text: draft.text, attachments })
      if (draft.workspaceCleanup) {
        cleanupKey.current = null
      } else {
        setText('')
        setFiles([])
        requestKey.current = null
      }
      const key = ['private', space, run.session_id]
      if (target) client.setQueryData([...key, 'session'], target)
      client.setQueryData<Run[]>([...key, 'runs'], previous => [run, ...(previous ?? []).filter(r => r.id !== run.id)])
      client.setQueryData([...key, 'run', run.id], run)
      composer.current?.focus()
    },
  })
  const cancel = useMutation({
    mutationFn: (id: string) => request<Run>(`${base}/runs/${id}/cancel`, 'POST'),
    onSuccess: run => {
      client.setQueryData([...sessionKey, 'run', run.id], run)
      void client.invalidateQueries({ queryKey: runsKey })
    },
  })
  const workspaceAction = useMutation({
    mutationFn: (action: string) => request(`${sessionURL}/${action}`, 'POST'),
    onSuccess: () => { void client.invalidateQueries({ queryKey: sessionKey }); void client.invalidateQueries({ queryKey: ['private', space, 'sessions'] }) },
  })
  const workspaceActionPending = workspaceAction.isPending || submit.isPending

  const validVersion = !!createdSession || versions.data?.some(version => version.id === versionID)
  const canSend = (sessionID ? ownsSession && runs.isSuccess && !session.data?.workspace_error : !agent.archived && validVersion)
    && !busy && !publishing && !submit.isPending && !!text.trim()
  const submitMessage = submit.mutate
  useEffect(() => {
    if (!initialPrompt || initialAttempted.current || !canSend || text !== initialPrompt.text || files.length) return
    initialAttempted.current = true
    const draft = { ...initialPrompt, files }
    requestKey.current = draft
    submitMessage(draft)
  }, [canSend, files, initialPrompt, submitMessage, text])
  const send = () => {
    if (!canSend) return
    if (requestKey.current?.text !== text || requestKey.current.files !== files) requestKey.current = { text, files, key: newRequestKey() }
    jumpToLatest()
    submit.mutate(requestKey.current, {
      onSuccess: ({ target }) => {
        if (!sessionID && target) {
          onCreated?.(target.id)
          navigate(agentSessionsPath(space, agent.id, target.id), { replace: true })
        }
      },
    })
  }
  const jumpToLatest = () => {
    follow.current = true
    if (viewport.current) viewport.current.scrollTop = viewport.current.scrollHeight
    setShowJump(false)
  }
  const pageCount = messages.data?.pages.length ?? 0
  const loadOlder = async () => {
    const node = viewport.current
    if (!node || messages.isFetching) return
    olderScroll.current = { top: node.scrollTop, height: node.scrollHeight, pageCount }
    follow.current = false
    const result = await messages.fetchNextPage({ cancelRefetch: false })
    if (result.isError || result.data?.pages.length === pageCount) olderScroll.current = null
  }
  useLayoutEffect(() => {
    const node = viewport.current, older = olderScroll.current
    if (node && older && older.pageCount !== pageCount) {
      node.scrollTop = older.top + node.scrollHeight - older.height
      olderScroll.current = null
    }
  }, [pageCount])
  useLayoutEffect(() => {
    const node = viewport.current, body = content.current
    if (!node || !body) return
    const observer = new ResizeObserver(() => { if (follow.current) node.scrollTop = node.scrollHeight })
    observer.observe(body)
    observer.observe(node)
    return () => observer.disconnect()
  }, [sessionID, ownsSession])
  useLayoutEffect(() => {
    const node = composer.current
    if (!node) return
    node.style.height = 'auto'
    node.style.height = `${node.scrollHeight}px`
  }, [text, sessionID, ownsSession])

  const history = messages.data?.pages.slice().reverse().flatMap(page => page.items) ?? []
  const runsByOrigin = new Map(sorted.map(run => [run.user_event_id, run.id]))
  const runsByID = new Map(sorted.map(run => [run.id, run]))
  const turns: Turn[] = []
  for (const message of history) {
    const id = message.run_id || runsByOrigin.get(message.source_event_id)
    let turn = turns.at(-1)
    if (!turn || !id || turn.runID !== id || message.role === 'user') {
      turn = { key: id ?? message.id, runID: id, assistant: [] }
      turns.push(turn)
    }
    if (message.role === 'user') turn.user = message
    else turn.assistant.push(message)
  }
  const showAccepted = accepted && !history.some(m => m.source_event_id === accepted.run.user_event_id)
  if (showAccepted && !turns.some(turn => turn.runID === accepted.run.id)) {
    turns.push({ key: accepted.run.id, runID: accepted.run.id, assistant: [] })
  }
  if (latest && !turns.some(turn => turn.runID === latest.id)) {
    turns.push({ key: latest.id, runID: latest.id, assistant: [] })
  }
  const pending = submit.isPending ? submit.variables : undefined
  const liveMessage = stream.state?.messages.some(m => m.text && !m.closed)
  const errors = [runs.error, current.error, messages.error, versions.error, submit.error, cancel.error, workspaceAction.error].filter(Boolean)
  const openRun = (id: string) => { setParams({ run: id }); setShowDetails(true) }
  const closeDetails = () => {
    setShowDetails(false)
    if (chosenRun) setParams({}, { replace: true })
  }
  if (sessionID && session.isPending) return <p className="session-loading" role="status">正在读取会话…</p>
  if (sessionID && session.isError) return <p className="session-loading" role="alert">{session.error.message}</p>
  if (sessionID && !ownsSession) return <p className="session-loading" role="alert">此会话不属于当前 Agent。</p>
  const fixedVersion = session.data?.version_id ?? createdSession?.version_id
  const draftRevision = session.data?.draft_revision ?? createdSession?.draft_revision
  return <section className="conversation-page">
    <header className="conversation-header">
      <h1>{sessionID || agent.name}</h1>
      <div className="conversation-actions">
        {sessionID && <button type="button" className="secondary" disabled={!latest} onClick={() => setShowDetails(true)}>运行详情</button>}
        {enablePublish && <CreativityPublish space={space} sessionID={sessionID} busy={busy || workspaceActionPending}
          snapshot={currentRun?.workspace_snapshot_committed ? currentRun.workspace_snapshot_id ?? '' : ''}
          onPublishing={setPublishing} />}
      </div>
    </header>
    <div className="conversation-body" ref={viewport} tabIndex={0} aria-label="对话记录" onScroll={event => {
      const bottom = atBottom(event.currentTarget)
      follow.current = bottom
      setShowJump(!bottom)
    }}>
      <div className="conversation-content chat-history" ref={content}>
        {messages.hasNextPage && <div className="older-messages">
          <button type="button" className="secondary" disabled={messages.isFetching} onClick={() => { void loadOlder() }}>
            {messages.isFetchingNextPage ? '正在读取…' : '加载更早的消息'}
          </button>
        </div>}
        {sessionID && messages.isPending && <p className="muted" role="status">正在读取消息…</p>}
        {(!sessionID || messages.isSuccess) && !history.length && !pending && !showAccepted && !busy && !stream.state?.messages.length &&
          <div className="empty-conversation"><h2>开始对话</h2>{!sessionID && agent.archived ? <p>此 Agent 已归档。</p>
            : !sessionID && versions.data?.length === 0 ? <p>请先发布 Agent 版本。</p> : null}</div>}
        {turns.map(({ key, runID, user, assistant }) => {
          const previous = runID && runID !== latest?.id ? runsByID.get(runID) : undefined
          const draft = showAccepted && accepted.run.id === runID ? accepted : undefined
          return <div key={key}>
            {user && (user.content || !!user.attachments?.length) && <ChatMessage role="user" text={user.content} attachments={user.attachments} space={space}
              contentID={user.truncated ? user.content_id : undefined} />}
            {draft && <ChatMessage role="user" text={draft.text} attachments={draft.attachments} space={space} />}
            <AssistantTurn space={space} messages={assistant} state={runID === latest?.id ? stream.state : null} />
            {previous && <div className="past-run">
              {previous.status !== 'succeeded' && <span>{statuses[previous.status]}{previous.error && ` · ${previous.error}`}</span>}
              <button type="button" className="text-link" onClick={() => openRun(previous.id)}>查看本轮运行</button>
            </div>}
          </div>
        })}
        {latest && <>
          <div className="run-status" data-testid="run-status" role="status">
            {busy && <span className="working-dot" aria-hidden="true" />}
            <strong>{statuses[status ?? ''] ?? status}</strong>
            <span>{busy && stopRequested ? '正在停止，等待文件收尾' : currentRun?.error || stream.state?.error}</span>
            {busy && !stopRequested && !liveMessage && <span>等待回复…</span>}
          </div>
          <RunActivity key={latest.id} state={stream.state} />
          {stream.error && <p role="alert" className="stream-error">{stream.error} <button type="button" className="secondary" onClick={() => setGeneration(x => x + 1)}>重新同步事件</button></p>}
        </>}
        {pending && <ChatMessage role="user" text={pending.text} space={space} pending />}
      </div>
    </div>
    {!readOnly && <div className="composer-dock">
      {showJump && <button type="button" className="secondary jump-to-latest" onClick={jumpToLatest}>↓ 回到最新消息</button>}
      {session.data?.workspace_error && !busy && <div className="error workspace-notice">
        <p role="alert">工作目录尚未确认持久化，已阻止下一轮运行。</p>
        <div className="actions">
          <button disabled={workspaceActionPending} onClick={() => workspaceAction.mutate('workspace-snapshots/retry')}>仅重试保存文件</button>
          <button className="secondary" disabled={workspaceActionPending || !runs.isSuccess} onClick={() => {
            cleanupKey.current ??= newRequestKey()
            workspaceAction.reset()
            jumpToLatest()
            submit.mutate({ text: cleanupText, files: [], key: cleanupKey.current, workspaceCleanup: true })
          }}>尝试Agent清理文件</button>
          <button className="secondary" disabled={workspaceActionPending} onClick={() => workspaceAction.mutate('workspace/restore')}>恢复最近已提交快照</button>
        </div>
        <p>恢复会丢弃当前未保存文件。</p>
      </div>}
      {fileError && <p role="alert">{fileError}</p>}
      {errors.map((error, i) => <p role="alert" key={i}>{error?.message}</p>)}
      <form className="chat-composer" onSubmit={event => { event.preventDefault(); send() }}>
        {files.length > 0 && <ul className="composer-files">{files.map((file, index) => <li key={index}>
          <span title={file.name}>{file.name}</span>
          <button className="secondary" type="button" disabled={submit.isPending} aria-label={`移除附件 ${file.name}`}
            onClick={() => setFiles(items => items.filter(item => item !== file))}>×</button>
        </li>)}</ul>}
        <textarea ref={composer} aria-label="输入任务" value={text} onChange={event => setText(event.target.value)} readOnly={submit.isPending}
          maxLength={262144} rows={2} placeholder="输入任务，继续对话…"
          onKeyDown={event => {
            if (event.key === 'Enter' && !event.shiftKey && !event.nativeEvent.isComposing && event.keyCode !== 229) {
              event.preventDefault()
              if (!event.repeat) send()
            }
          }} />
        <div className="composer-tools">
          <div className="composer-options">
            <input ref={fileInput} type="file" hidden aria-label="添加任务附件" disabled={submit.isPending || files.length >= 16}
              onChange={event => {
                const file = event.currentTarget.files?.[0]
                event.currentTarget.value = ''
                if (!file) return
                setFileError(file.size > 8 * 1024 * 1024 ? '附件不能超过 8 MiB' : '')
                if (file.size <= 8 * 1024 * 1024) setFiles(items => [...items, file])
              }} />
            <button type="button" className="secondary attach-button" disabled={submit.isPending || files.length >= 16}
              onClick={() => fileInput.current?.click()} title="添加附件（每个最多 8 MiB）">+ 附件</button>
            {draftRevision ? <span className="composer-version">草稿调试 · r{draftRevision}</span>
              : fixedVersion ? <span className="composer-version" title={fixedVersion}>固定版本 · {fixedVersion.slice(0, 8)}</span>
              : <select className="composer-version" aria-label="Agent 版本" disabled={submit.isPending || !versions.data?.length}
                value={validVersion ? versionID : ''} onChange={event => setSelectedVersion(event.target.value)}>
                <option value="" disabled>选择 Agent 版本</option>
                {versions.data?.map(version => <option key={version.id} value={version.id}>
                  {version.debug ? `调试快照 ${version.id.slice(0, 8)}` : version.label}
                </option>)}
              </select>}
          </div>
          {busy && latest
            ? <button type="button" className="stop-button secondary" aria-label="取消运行" disabled={cancel.isPending || stopRequested} onClick={() => cancel.mutate(latest.id)}>
              <span className="stop-icon" aria-hidden="true" />{stopRequested || cancel.isPending ? '正在停止' : '停止'}
            </button>
            : <button type="submit" className="send-button" aria-label="发送任务" disabled={!canSend}>{submit.isPending ? '正在发送…' : '发送 ↑'}</button>}
        </div>
      </form>
      <p className="composer-hint">Enter 发送 · Shift + Enter 换行</p>
    </div>
    }
    {readOnly && <p className="readonly-replay">测评会话 · 只读回放</p>}
    {showDetails && session.data && <RunDetails space={space} session={session.data} runs={sorted} current={currentRun}
      chosenRun={chosenRun} onSelect={id => setParams({ run: id })} onClose={closeDetails} />}
  </section>
}

async function uploadAttachment(sessionURL: string, file: File) {
  const bytes = new Uint8Array(await file.arrayBuffer())
  let binary = ''
  for (let i = 0; i < bytes.length; i += 32768) binary += String.fromCharCode(...bytes.subarray(i, i + 32768))
  const types: Record<string, string> = { json: 'application/json', csv: 'text/csv', txt: 'text/plain' }
  const mediaType = file.type || types[file.name.split('.').at(-1)?.toLowerCase() ?? ''] || 'application/octet-stream'
  return request<Attachment>(`${sessionURL}/attachments`, 'POST', { name: file.name, media_type: mediaType, content_base64: btoa(binary) })
}

function ChatMessage({ role, text, live, attachments, space, contentID, pending }: {
  role: 'user' | 'assistant'; text: string; live?: Schema['UIMessage']; attachments?: Attachment[]
  space: string; contentID?: string; pending?: boolean
}) {
  return <article className={`chat-turn ${role}`}>
    {role === 'assistant' && <div className="assistant-label"><span className="assistant-icon" aria-hidden="true">✦</span><strong>助手</strong>{live?.incomplete && <small>输出未完成</small>}</div>}
    <div className="chat-turn-content">
      <MessageText text={text} plain={role === 'user'} live={live} />
      {attachments?.map(a => <a className="attached-file" key={a.id} href={`/api/v1/spaces/${space}/payloads/${a.ref.object_id}`} download>{a.name}</a>)}
      {contentID && <a className="attached-file" href={`/api/v1/spaces/${space}/payloads/${contentID}`} download>下载完整内容</a>}
      {pending && <small>正在发送…</small>}
    </div>
  </article>
}

function MessageText({ text, plain, live }: { text: string; plain?: boolean; live?: Schema['UIMessage'] }) {
  if (!text) return null
  if (plain || live && (!live.closed || live.incomplete)) {
    return <div className="chat-plain">{text}{live && !live.closed && !live.incomplete && <span className="typing-cursor" aria-hidden="true" />}</div>
  }
  return <Suspense fallback={<div className="chat-plain">{text}</div>}><SafeMarkdown text={text} showSource={false} /></Suspense>
}

function AssistantTurn({ space, messages, state }: { space: string; messages: Message[]; state: Schema['UIState'] | null }) {
  const positions = new Map(messages.map((message, index) => [message.id, index]))
  const liveTools = new Map(state?.tools.map(tool => [tool.id, tool]))
  const toolsByMessage = new Map<string, Tool[]>()
  for (const tool of liveTools.values()) {
    if (!tool.parent_message_id) continue
    const tools = toolsByMessage.get(tool.parent_message_id) ?? []
    tools.push(tool)
    toolsByMessage.set(tool.parent_message_id, tools)
  }
  const attached = new Set<string>()
  const steps: { id: string; text: string; live?: Schema['UIMessage']; contentID?: string; tools: Tool[] }[] = []
  const addStep = (message?: Message, live?: Schema['UIMessage']) => {
    const id = live?.id ?? message?.id
    if (!id) return
    const saved = message?.content ?? ''
    const text = live && live.text.length >= saved.length ? live.text : saved
    const contentID = message?.truncated && text.length <= saved.length ? message.content_id : undefined
    const tools = new Map(message?.tools?.map(tool => [tool.id, tool]))
    for (const tool of toolsByMessage.get(id) ?? []) {
      if (!tools.has(tool.id)) tools.set(tool.id, tool)
    }
    const calls = [...tools.values()].map(tool => {
      attached.add(tool.id)
      const active = liveTools.get(tool.id)
      return active ? { ...active, arguments: active.arguments || tool.arguments, result: active.result || tool.result } : tool
    })
    if (!text && !contentID) {
      if (!calls.length) return
      const previous = steps.at(-1)
      if (previous) {
        previous.tools.push(...calls)
        return
      }
    }
    steps.push({ id, text, live, contentID, tools: calls })
  }

  // Merge by message ID before grouping textless tool steps. History may start mid-run.
  let next = 0
  for (const live of state?.messages ?? []) {
    const position = positions.get(live.id)
    if (position === undefined) addStep(undefined, live)
    else {
      while (next < position) addStep(messages[next++])
      addStep(messages[next++], live)
    }
  }
  while (next < messages.length) addStep(messages[next++])
  const unattached = [...liveTools.values()].filter(tool => !attached.has(tool.id))
  if (!steps.length && !unattached.length) return null

  return <article className="chat-turn assistant">
    <div className="assistant-label"><span className="assistant-icon" aria-hidden="true">✦</span><strong>助手</strong></div>
    <div className="chat-turn-content assistant-steps">
      {steps.map(step => <div key={step.id} className="assistant-step">
        <MessageText text={step.text} live={step.live} />
        {step.live?.incomplete && <small className="assistant-incomplete">输出未完成</small>}
        {step.contentID && <a className="attached-file" href={`/api/v1/spaces/${space}/payloads/${step.contentID}`} download>下载完整内容</a>}
        <ToolActivity tools={step.tools} />
      </div>)}
      <ToolActivity tools={unattached} />
    </div>
  </article>
}

function ToolActivity({ tools }: { tools: Tool[] }) {
  const [limit, setLimit] = useState(50)
  if (!tools.length) return null
  return <details className="tool-activity">
    <summary>工具调用 · {tools.length}</summary>
    {tools.length > limit && <button className="secondary" onClick={() => setLimit(n => n + 50)}>显示更早的工具调用</button>}
    {tools.slice(-limit).map(t => <details key={t.id}>
      <summary>{t.name} · {toolStatuses[t.status] ?? t.status}</summary>
      {t.standard ? <pre>{t.arguments}</pre> : <p className="muted">完整参数可在运行详情的 Trace 中查看。</p>}
      {t.result && <pre>{t.result}</pre>}
    </details>)}
  </details>
}

function RunActivity({ state, showTools }: { state: Schema['UIState'] | null; showTools?: boolean }) {
  if (!state || !state.plan && !state.artifacts.length && !(showTools && state.tools.length)) return null
  return <div className="run-output">
    {state.plan && <aside aria-label="当前运行计划" className="run-plan"><strong>任务计划 · 修订 {state.plan.revision}</strong>
      <ol>{state.plan.steps.map(step => <li key={step.id}>
        <span>{({ pending: '待处理', in_progress: '进行中', completed: '已完成', skipped: '已跳过' })[step.status]}</span> · {step.title}
      </li>)}</ol>
    </aside>}
    {showTools && <ToolActivity tools={state.tools} />}
    {state.artifacts.length > 0 && <div className="run-artifacts">{state.artifacts.map(a => <a key={a.id} href={a.download_url} download>{a.name} ↓</a>)}</div>}
  </div>
}

function RunDetails({ space, session, chosenRun, runs, current, onSelect, onClose }: {
  space: string; session: Schema['RuntimeSession']; chosenRun: string; runs: Run[]; current?: Run
  onSelect: (run: string) => void; onClose: () => void
}) {
  const dialog = useRef<HTMLDialogElement>(null)
  const selected = !chosenRun || chosenRun === current?.id ? current : runs.find(run => run.id === chosenRun)
  const { input_tokens: input, output_tokens: output, cache_read_tokens: cached } = selected?.usage ?? {}
  const cacheHitRate = input != null && input > 0 && cached != null && cached <= input
    ? `${(100 * cached / input).toFixed(1)}%` : '未知'
  const historical = useRunStream(space, session.id, selected && selected.id !== current?.id ? selected.id : '', 0)
  const state = historical.state
  useEffect(() => {
    const node = dialog.current
    node?.showModal()
    return () => node?.close()
  }, [])
  return <dialog className="run-details-dialog" ref={dialog} aria-labelledby="run-details-heading" onCancel={event => { event.preventDefault(); onClose() }}>
    <header><h2 id="run-details-heading">运行详情</h2><button type="button" className="secondary" onClick={onClose}>关闭</button></header>
    <label>查看运行<select value={selected?.id ?? ''} onChange={event => onSelect(event.target.value)}>
      {!selected && <option value="" disabled>选择运行</option>}
      {runs.map(run => <option key={run.id} value={run.id}>{new Date(run.created_at).toLocaleString()} · {statuses[run.status]} · {run.id.slice(0, 8)}</option>)}
    </select></label>
    {chosenRun && runs.length > 0 && !selected && <p role="alert">此运行不属于当前会话。</p>}
    {selected && <>
      <dl className="run-detail-info">
        <div><dt>状态</dt><dd>{statuses[selected.status]}{selected.error && ` · ${selected.error}`}</dd></div>
        <div><dt>版本</dt><CopyableRunID label="版本" value={session.version_id} /></div>
        <div><dt>文件快照</dt><dd>{selected.workspace_snapshot_committed ? '已提交' : '尚未提交'}</dd></div>
        <div><dt>Token 用量</dt><dd>输入 {input ?? '未知'} / 输出 {output ?? '未知'} / 缓存读取 {cached ?? '未知'} / 输入缓存命中率 {cacheHitRate}</dd></div>
      </dl>
      {historical.error && <p role="alert">{historical.error}</p>}
      {selected.id !== current?.id && state?.messages.filter(m => m.incomplete && m.text).map(m =>
        <ChatMessage key={m.id} role="assistant" text={m.text} live={m} space={space} />)}
      {selected.id !== current?.id && <RunActivity key={selected.id} state={state} showTools />}
      <Suspense fallback={<p role="status">正在载入 Trace…</p>}><Trace key={selected.id} space={space} runID={selected.id} /></Suspense>
    </>}
  </dialog>
}

function CopyableRunID({ label, value }: { label: string; value: string }) {
  const [feedback, setFeedback] = useState('')
  const copy = async () => {
    try {
      await navigator.clipboard.writeText(value)
      setFeedback('已复制')
    } catch {
      setFeedback('复制失败')
    }
  }
  return <dd className="run-copy-id">
    <code>{value}</code>
    <button type="button" className="secondary" aria-label={`复制${label} ID`} onClick={copy}>复制</button>
    <span className="muted" role="status">{feedback}</span>
  </dd>
}
