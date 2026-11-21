import { useRef, useState } from 'react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { Link, useLocation, useNavigate, useParams } from 'react-router-dom'
import { creativitySessionPath, spacePath } from '../../app/routes'
import { newRequestKey, request, type Schema } from '../../lib/api'
import { Conversation } from './Conversation'
import { WorkspaceFiles } from './WorkspaceFiles'

export function CreativityHome({ space }: { space: string }) {
  const [text, setText] = useState('')
  const draft = useRef<{ text: string; key: string } | null>(null)
  const client = useQueryClient(), navigate = useNavigate()
  const base = `/spaces/${space}/creativity`
  const availability = useQuery({
    queryKey: ['private', space, 'creativity'],
    queryFn: ({ signal }) => request<Schema['CreativityAvailability']>(base, 'GET', undefined, signal),
  })
  const create = useMutation({
    mutationFn: (prompt: { text: string; key: string }) => request<Schema['RuntimeSession']>(`${base}/sessions`, 'POST', { idempotency_key: prompt.key }),
    onSuccess: (session, prompt) => {
      client.setQueryData(['private', space, session.id, 'session'], session)
      navigate(creativitySessionPath(space, session.id), { state: { prompt } })
    },
  })
  return <section className="creativity-home" aria-label="创意">
    <form onSubmit={event => {
      event.preventDefault()
      if (!availability.data?.available || create.isPending || !text.trim()) return
      if (draft.current?.text !== text) draft.current = { text, key: newRequestKey() }
      create.mutate(draft.current)
    }}>
      <textarea aria-label="新建 Agent 的要求" placeholder="你想创建怎样的 Agent？" maxLength={65536}
        value={text} disabled={!availability.data?.available || create.isPending} onChange={event => setText(event.target.value)} />
      <button disabled={!availability.data?.available || create.isPending || !text.trim()}>{create.isPending ? '正在进入…' : '发送'}</button>
    </form>
    {availability.isPending && <p role="status">正在检查创作 Agent…</p>}
    {availability.data && !availability.data.available && <p role="status">创作 Agent 暂不可用，请联系空间管理员。</p>}
    {(create.error || availability.error) && <p role="alert">{(create.error || availability.error)?.message}</p>}
  </section>
}

export function CreativityConversation({ space }: { space: string }) {
  const { sessionId = '' } = useParams(), location = useLocation(), navigate = useNavigate()
  const session = useQuery({
    queryKey: ['private', space, sessionId, 'session'],
    queryFn: ({ signal }) => request<Schema['RuntimeSession']>(`/spaces/${space}/sessions/${sessionId}`, 'GET', undefined, signal),
  })
  const prompt = (location.state as { prompt?: { text: string; key: string } } | null)?.prompt
  const target = session.data?.creativity?.target_agent_id
  const definition = useQuery({
    queryKey: ['private', space, 'agent', target, 'draft'], enabled: !!target,
    queryFn: ({ signal }) => request<Schema['DefinitionDraft']>(`/spaces/${space}/agents/${target}/draft`, 'GET', undefined, signal),
  })
  return <div className="session-workspace">
    <div className="session-toolbar">
      <Link className="text-link" to={spacePath(space)}>← 创意</Link>
      {target && <span>正在迭代：{definition.data?.name ?? target}</span>}
    </div>
    <WorkspaceFiles space={space} sessionID={session.data?.creativity ? sessionId : ''}>
      {session.data?.creativity
        ? <Conversation key={sessionId} space={space} sessionID={sessionId} initialPrompt={prompt} enablePublish
          agent={{ id: session.data.definition_id, name: session.data.agent_name ?? '创作 Agent', archived: false }}
          onInitialAccepted={() => navigate(location.pathname + location.search, { replace: true, state: null })} />
        : <p className="session-loading" role={session.isPending ? 'status' : 'alert'}>
          {session.error?.message ?? (session.isPending ? '正在读取会话…' : '此会话不属于创意。')}
        </p>}
    </WorkspaceFiles>
  </div>
}
