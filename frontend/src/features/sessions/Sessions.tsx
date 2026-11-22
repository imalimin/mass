import { Link, Navigate, NavLink, useLocation, useParams } from 'react-router-dom'
import { useState } from 'react'
import { useQuery } from '@tanstack/react-query'
import { agentSessionsPath, creativitySessionPath, spacePath } from '../../app/routes'
import { Icon } from '../../components/Icon'
import { list, request, type Schema } from '../../lib/api'
import { Conversation } from './Conversation'
import { WorkspaceFiles } from './WorkspaceFiles'
import './sessions.css'

export function SessionWorkspace({ space }: { space: string }) {
  const { agentId = '', sessionId = '' } = useParams()
  return <AgentConversations key={agentId} space={space} agentID={agentId} sessionID={sessionId} />
}

function AgentConversations({ space, agentID, sessionID }: { space: string; agentID: string; sessionID: string }) {
  const { state } = useLocation() as { state?: { creativitySessionID?: string } }
  const creativitySessionID = sessionID ? state?.creativitySessionID : undefined
  const [startedSession, setStartedSession] = useState('')
  const [draftNumber, setDraftNumber] = useState(0)
  const startNew = () => {
    setStartedSession('')
    setDraftNumber(n => n + 1)
  }
  const current = useQuery({
    queryKey: ['private', space, sessionID, 'session'],
    queryFn: ({ signal }) => request<Schema['RuntimeSession']>(`/spaces/${space}/sessions/${sessionID}`, 'GET', undefined, signal),
    enabled: !!sessionID,
  })
  const sharedAgent = current.data?.agent_source_space_id
    ? { id: agentID, name: current.data.agent_name ?? agentID, archived: false }
    : undefined
  const agent = useQuery({
    queryKey: ['private', space, 'agent', agentID, 'draft'],
    queryFn: ({ signal }) => request<Schema['DefinitionDraft']>(`/spaces/${space}/agents/${agentID}/draft`, 'GET', undefined, signal),
    enabled: !sessionID || !!current.data && !sharedAgent,
  })
  const fixedDraftAgent = current.data?.draft_revision
    ? { id: agentID, name: current.data.agent_name ?? agentID, archived: false }
    : undefined
  const conversationAgent = sharedAgent ?? fixedDraftAgent ?? agent.data
  const sessions = useQuery({
    queryKey: ['private', space, 'sessions', agentID],
    queryFn: ({ signal }) => list<Schema['RuntimeSession']>(`/spaces/${space}/agents/${agentID}/sessions`, signal),
    enabled: !creativitySessionID,
    refetchInterval: q => q.state.error ? false : 5000,
  })
  const recent = [...(sessions.data ?? [])].sort((a, b) => b.created_at.localeCompare(a.created_at) || b.id.localeCompare(a.id))
  return <div className={creativitySessionID ? 'session-workspace' : 'session-workspace session-workspace--history'}>
    {!creativitySessionID && <div className="session-back"><Link className="text-link icon-link" to={spacePath(space, 'agents')}
      aria-label="← Agents"><Icon name="arrowLeft" />Agents</Link></div>}
    {!creativitySessionID && <nav aria-label="会话列表" className="session-rail">
      <div className="session-rail-top">
        {!sharedAgent && agent.data && !agent.data.archived && <NavLink end to={agentSessionsPath(space, agentID)} aria-label="新建会话"
          onClick={startNew}><Icon name="plus" />新建会话</NavLink>}
      </div>
      <div className="session-links">
        <h2>历史会话</h2>
        {recent.map(s => <NavLink key={s.id} to={s.creativity ? creativitySessionPath(space, s.id) : agentSessionsPath(space, agentID, s.id)} end
          onClick={() => { if (s.id !== sessionID) setStartedSession('') }} title={s.title ? `${s.title} (${s.id})` : s.id}>
          <Icon name="chat" size={16} />
          <span>{s.title || s.id}{s.draft_revision && <small>草稿调试</small>}{s.workspace_error && <small>文件待恢复</small>}</span>
        </NavLink>)}
        {sessions.isPending && <p role="status">正在读取会话…</p>}
        {sessions.data?.length === 0 && <p>还没有会话</p>}
        {sessions.error && <p role="alert">{sessions.error.message}</p>}
      </div>
    </nav>}
    <WorkspaceFiles space={space} sessionID={sessionID}>
      {workspaceToggle => conversationAgent ? <Conversation key={!sessionID || sessionID === startedSession ? `draft-${draftNumber}` : sessionID} space={space}
        agent={conversationAgent} sessionID={sessionID} onCreated={setStartedSession} workspaceToggle={workspaceToggle}
        back={creativitySessionID && <Link className="text-link icon-link" to={creativitySessionPath(space, creativitySessionID)}
          aria-label="← 返回创意会话"><Icon name="arrowLeft" />返回创意会话</Link>} />
        : <p className="session-loading" role={current.error || agent.error ? 'alert' : 'status'}>{(current.error || agent.error)?.message ?? '正在读取会话…'}</p>}
    </WorkspaceFiles>
  </div>
}

export function LegacySession({ space }: { space: string }) {
  const { sessionId = '' } = useParams(), { search } = useLocation()
  const session = useQuery({
    queryKey: ['private', space, sessionId, 'session'],
    queryFn: ({ signal }) => request<Schema['RuntimeSession']>(`/spaces/${space}/sessions/${sessionId}`, 'GET', undefined, signal),
  })
  if (session.data) return <Navigate replace to={(session.data.creativity ? creativitySessionPath(space, sessionId)
    : agentSessionsPath(space, session.data.definition_id, sessionId)) + search} />
  return <p className="session-loading" role={session.error ? 'alert' : 'status'}>{session.error?.message ?? '正在读取会话…'}</p>
}
