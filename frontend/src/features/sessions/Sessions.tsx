import { Link, Navigate, NavLink, useLocation, useParams } from 'react-router-dom'
import { useState } from 'react'
import { useQuery } from '@tanstack/react-query'
import { agentSessionsPath, spacePath } from '../../app/routes'
import { list, request, type Schema } from '../../lib/api'
import { Conversation } from './Conversation'
import './sessions.css'

export function SessionWorkspace({ space }: { space: string }) {
  const { agentId = '', sessionId = '' } = useParams()
  return <AgentConversations key={agentId} space={space} agentID={agentId} sessionID={sessionId} />
}

function AgentConversations({ space, agentID, sessionID }: { space: string; agentID: string; sessionID: string }) {
  const [showSessions, setShowSessions] = useState(false)
  const [startedSession, setStartedSession] = useState('')
  const [draftNumber, setDraftNumber] = useState(0)
  const startNew = () => {
    setStartedSession('')
    setDraftNumber(n => n + 1)
    setShowSessions(false)
  }
  const agent = useQuery({
    queryKey: ['private', space, 'agent', agentID, 'draft'],
    queryFn: ({ signal }) => request<Schema['DefinitionDraft']>(`/spaces/${space}/agents/${agentID}/draft`, 'GET', undefined, signal),
  })
  const sessions = useQuery({
    queryKey: ['private', space, 'sessions', agentID],
    queryFn: ({ signal }) => list<Schema['RuntimeSession']>(`/spaces/${space}/agents/${agentID}/sessions`, signal),
    refetchInterval: q => q.state.error ? false : 5000,
  })
  const recent = [...(sessions.data ?? [])].sort((a, b) => b.created_at.localeCompare(a.created_at) || b.id.localeCompare(a.id))
  return <div className="session-workspace">
    <div className="session-mobile-nav">
      <Link className="text-link" to={spacePath(space, 'agents')}>← Agents</Link>
      <button className="secondary" type="button" aria-expanded={showSessions} aria-controls="session-list"
        onClick={() => setShowSessions(open => !open)}>会话列表</button>
    </div>
    <nav id="session-list" aria-label="会话列表" className={`session-rail${showSessions ? ' is-open' : ''}`}>
      <div className="session-rail-top">
        <Link className="agent-back" to={spacePath(space, 'agents')}>← Agents</Link>
        {agent.data && !agent.data.archived && <NavLink end to={agentSessionsPath(space, agentID)} aria-label="新建会话"
          onClick={startNew}><span aria-hidden="true">+</span> 新建会话</NavLink>}
      </div>
      <div className="session-links">
        <h2>会话</h2>
        {recent.map(s => <NavLink key={s.id} to={agentSessionsPath(space, agentID, s.id)} end
          onClick={() => { if (s.id !== sessionID) setStartedSession(''); setShowSessions(false) }} title={s.id}>
          {s.id}{s.workspace_error && <small>文件待恢复</small>}
        </NavLink>)}
        {sessions.isPending && <p role="status">正在读取会话…</p>}
        {sessions.data?.length === 0 && <p>还没有会话</p>}
        {sessions.error && <p role="alert">{sessions.error.message}</p>}
      </div>
    </nav>
    {agent.data ? <Conversation key={!sessionID || sessionID === startedSession ? `draft-${draftNumber}` : sessionID} space={space}
      agent={agent.data} sessionID={sessionID} onCreated={setStartedSession} />
      : <p className="session-loading" role={agent.error ? 'alert' : 'status'}>{agent.error?.message ?? '正在读取 Agent…'}</p>}
  </div>
}

export function LegacySession({ space }: { space: string }) {
  const { sessionId = '' } = useParams(), { search } = useLocation()
  const session = useQuery({
    queryKey: ['private', space, sessionId, 'session'],
    queryFn: ({ signal }) => request<Schema['RuntimeSession']>(`/spaces/${space}/sessions/${sessionId}`, 'GET', undefined, signal),
  })
  if (session.data) return <Navigate replace to={agentSessionsPath(space, session.data.definition_id, sessionId) + search} />
  return <p className="session-loading" role={session.error ? 'alert' : 'status'}>{session.error?.message ?? '正在读取会话…'}</p>
}
