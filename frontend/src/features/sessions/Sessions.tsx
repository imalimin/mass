import { Link, NavLink, Outlet, useNavigate, useParams, useSearchParams } from 'react-router-dom'
import { useEffect, useRef, useState } from 'react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { sessionPath, spacePath } from '../../app/routes'
import { list, request, type Schema } from '../../lib/api'
import { Conversation } from './Conversation'
import './sessions.css'

type Session = Schema['RuntimeSession']
const sessionsKey = (space: string) => ['private', space, 'sessions']

export function SessionWorkspace({ space }: { space: string }) {
  const [showSessions, setShowSessions] = useState(false)
  const sessions = useQuery({
    queryKey: sessionsKey(space),
    queryFn: ({ signal }) => list<Session>(`/spaces/${space}/sessions`, signal),
    refetchInterval: q => q.state.error ? false : 5000,
  })
  const recent = [...(sessions.data ?? [])].sort((a, b) => b.created_at?.localeCompare(a.created_at) || b.id.localeCompare(a.id))
  return <div className="session-workspace">
    <button className="session-switch secondary" type="button" aria-expanded={showSessions} aria-controls="session-list"
      onClick={() => setShowSessions(open => !open)}>会话列表</button>
    <nav id="session-list" aria-label="会话列表" className={`session-rail${showSessions ? ' is-open' : ''}`}>
      <div className="session-rail-top">
        <NavLink end to={spacePath(space, 'sessions')} aria-label="新建会话" onClick={() => setShowSessions(false)}><span aria-hidden="true">+</span> 新建会话</NavLink>
      </div>
      <div className="session-links">
        <h2>会话</h2>
        {recent.map(s => <NavLink key={s.id} to={sessionPath(space, s.id)} end onClick={() => setShowSessions(false)} title={s.title || s.id}>
          {s.title || s.id.slice(0, 8)}{s.workspace_error && <small>文件待恢复</small>}
        </NavLink>)}
        {sessions.isPending && <p role="status">正在读取会话…</p>}
        {sessions.data?.length === 0 && <p>还没有会话</p>}
        {sessions.error && <p role="alert">{sessions.error.message}</p>}
      </div>
    </nav>
    <Outlet />
  </div>
}

export function NewSession({ space }: { space: string }) {
  const [params] = useSearchParams()
  const source = params.get('from') ?? ''
  return <SessionForm key={source} space={space} source={source} />
}

function SessionForm({ space, source }: { space: string; source: string }) {
  const [agent, setAgent] = useState(''), [version, setVersion] = useState('')
  const [title, setTitle] = useState(''), [snapshot, setSnapshot] = useState('')
  const client = useQueryClient(), navigate = useNavigate(), base = `/spaces/${space}`
  const key = useRef<{ signature: string; key: string } | null>(null)
  const abort = useRef<AbortController | null>(null)
  useEffect(() => {
    const controller = new AbortController()
    abort.current = controller
    return () => controller.abort()
  }, [])
  const agents = useQuery({
    queryKey: ['private', space, 'run-agents'],
    queryFn: ({ signal }) => list<Schema['Definition']>(`${base}/agents`, signal),
  })
  const versions = useQuery({
    queryKey: ['private', space, 'run-agent-versions', agent],
    queryFn: async ({ signal }) => {
      const [published, debug] = await Promise.all([
        list<Schema['DefinitionVersion']>(`${base}/agents/${agent}/versions`, signal),
        list<Schema['DefinitionVersion']>(`${base}/agents/${agent}/debug-snapshots`, signal),
      ])
      return [...published, ...debug]
    },
    enabled: !!agent,
  })
  const snapshots = useQuery({
    queryKey: ['private', space, source, 'workspace-snapshots'],
    queryFn: ({ signal }) => request<Schema['RuntimeSnapshots']>(`${base}/sessions/${source}/workspace-snapshots`, 'GET', undefined, signal),
    enabled: !!source,
  })
  const create = useMutation({
    mutationFn: () => {
      const body = { agent_id: agent, version_id: version, source_workspace_snapshot_id: snapshot, title }
      const signature = JSON.stringify(body)
      if (key.current?.signature !== signature) key.current = { signature, key: crypto.randomUUID() }
      return request<Session>(`${base}/sessions`, 'POST', { ...body, idempotency_key: key.current.key }, abort.current?.signal)
    },
    onSuccess: session => {
      if (abort.current?.signal.aborted) return
      client.setQueryData<Session[]>(sessionsKey(space), previous => [
        session, ...(previous ?? []).filter(s => s.id !== session.id),
      ])
      void client.invalidateQueries({ queryKey: sessionsKey(space) })
      navigate(sessionPath(space, session.id), { replace: true })
    },
  })
  const available = agents.data?.filter(a => !a.archived) ?? []
  return <section className="session-start">
    <div className="session-start-content">
      {source && <Link className="text-link" to={sessionPath(space, source)}>返回来源会话</Link>}
      <h1>{source ? '从文件快照新建会话' : '开始新对话'}</h1>
      <form className="session-start-form" onSubmit={event => {
        event.preventDefault()
        if (!create.isPending && agent && (!source || snapshot)) create.mutate()
      }}>
        <label>使用 Agent<select required value={agent} disabled={create.isPending} onChange={event => { setAgent(event.target.value); setVersion('') }}>
          <option value="">选择 Agent</option>
          {available.map(a => <option key={a.id} value={a.id}>{a.name}</option>)}
        </select></label>
        <label>定义版本<select value={version} disabled={!agent || create.isPending} onChange={event => setVersion(event.target.value)}>
          <option value="">默认发布版本</option>
          {versions.data?.filter(v => !v.archived).map(v => <option key={v.id} value={v.id}>{v.debug ? `调试快照 ${v.id.slice(0, 8)}` : v.label}</option>)}
        </select></label>
        {source && <label className="full-width">文件起始快照<select required value={snapshot} disabled={create.isPending} onChange={event => setSnapshot(event.target.value)}>
          <option value="">选择文件快照</option>
          {snapshots.data?.items.map(s => <option key={s.id} value={s.id}>{s.id.slice(0, 8)}</option>)}
        </select></label>}
        <label className="full-width">会话名称<input value={title} maxLength={200} placeholder="可选" disabled={create.isPending} onChange={event => setTitle(event.target.value)} /></label>
        {agents.isSuccess && available.length === 0 && <Link className="text-link full-width" to={spacePath(space, 'agents')}>先创建并发布一个 Agent →</Link>}
        {[agents.error, versions.error, snapshots.error, create.error].filter(Boolean).map((error, i) => <p className="full-width" role="alert" key={i}>{error?.message}</p>)}
        <button disabled={create.isPending || !agent || (!!source && !snapshot)}>{create.isPending ? '正在创建…' : '新建会话'}</button>
      </form>
    </div>
  </section>
}

export function SessionConversation({ space }: { space: string }) {
  const { sessionId = '' } = useParams()
  return <Conversation key={sessionId} space={space} sessionID={sessionId} />
}
