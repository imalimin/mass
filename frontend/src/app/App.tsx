import { useEffect, useState } from 'react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { NavLink, useNavigate } from 'react-router-dom'
import { APIError, list, request, type Schema } from '../lib/api'
import { Catalog } from '../features/catalog/Catalog'
import { SpaceDetail } from '../features/spaces/SpaceDetail'

export function App() {
  const client = useQueryClient(), navigate = useNavigate()
  const [selected, setSelected] = useState<Schema['Space'] | null>(null)
  const [notice, setNotice] = useState('')
  const me = useQuery({ queryKey: ['me'], queryFn: ({ signal }) => request<Schema['User']>('/auth/me', 'GET', undefined, signal), retry: false, refetchInterval: q => q.state.error ? false : 30000 })
  useEffect(() => {
    const lost = (event: Event) => {
      const status = (event as CustomEvent<number>).detail
      void client.cancelQueries(); client.removeQueries({ queryKey: ['private'] }); setSelected(null)
      setNotice(status === 403 ? '空间访问权限已失效，已关闭当前空间。' : '登录已失效，请重新登录。')
      if (status === 401) { client.removeQueries({ queryKey: ['me'] }); void client.invalidateQueries({ queryKey: ['me'] }) }
      else void client.invalidateQueries({ queryKey: ['spaces'] })
      navigate('/spaces')
    }
    window.addEventListener('mass-access-lost', lost); return () => window.removeEventListener('mass-access-lost', lost)
  }, [client, navigate])
  useEffect(() => { if (me.error instanceof APIError && me.error.status === 401) { void client.cancelQueries({ queryKey: ['private'] }); client.removeQueries({ queryKey: ['private'] }); client.removeQueries({ queryKey: ['spaces'] }) } }, [me.error, client])
  const logout = useMutation({ mutationFn: () => request('/auth/logout', 'POST'), onSuccess: async () => { await client.cancelQueries(); client.clear(); setSelected(null); navigate('/login') } })
  if (me.isPending) return <div className="loading" role="status">正在读取登录状态…</div>
  if (me.isError) {
    if (me.error instanceof APIError && me.error.status === 401) return <Login onSuccess={() => { setSelected(null); setNotice(''); void client.invalidateQueries({ queryKey: ['me'] }); navigate('/spaces') }} />
    return <div className="login-card"><h1>暂时无法连接服务</h1><p role="alert">{me.error.message}</p><button onClick={() => { void me.refetch() }}>重试</button></div>
  }
  return <div className="layout"><aside><a className="brand" href="/">M<span className="brand-dot">A</span>SS<span className="brand-sub">AGENT WORKSPACE</span></a><div className="nav-caption">工作台</div><nav><NavLink to="/spaces" onClick={() => setSelected(null)}>◈ <span>空间</span></NavLink></nav><div className="sidebar-footer">{me.data.username}<button onClick={() => logout.mutate()} disabled={logout.isPending}>退出登录</button></div></aside>
    <main><header><div><div className="eyebrow">WORKSPACE</div><h1>{selected?.name ?? '空间'}</h1></div><span className="service-status">已连接 MASS 服务</span></header><p className="intro">管理协作成员、模型与 Agent / Skill 定义。</p>
      {notice && <div role="alert" className="error">{notice}<button onClick={() => setNotice('')}>关闭</button></div>}{logout.error && <p role="alert">{logout.error.message}</p>}
      <Spaces selected={selected} onSelect={async space => { await client.cancelQueries({ queryKey: ['private'] }); client.removeQueries({ queryKey: ['private'] }); setSelected(space); setNotice('') }} />
      {selected && <SpaceWorkspace key={selected.id} space={selected} user={me.data} onLeave={() => setSelected(null)} />}
      <footer>Agent 与 Skill 定义已开放；运行与调试将在 D04 接入。</footer>
    </main></div>
}
function Login({ onSuccess }: { onSuccess: () => void }) {
  const [username, setUsername] = useState(''), [password, setPassword] = useState('')
  const login = useMutation({ mutationFn: () => request<Schema['User']>('/auth/login', 'POST', { username, password }), onSuccess: () => { setPassword(''); onSuccess() } })
  return <main className="login-card"><div className="eyebrow">MASS WORKSPACE</div><h1>登录 MASS</h1><p className="intro">使用管理员为你创建的账号。</p><form onSubmit={e => { e.preventDefault(); login.mutate() }}><label>用户名<input autoComplete="username" value={username} onChange={e => setUsername(e.target.value)} required /></label><label>密码<input type="password" autoComplete="current-password" value={password} onChange={e => setPassword(e.target.value)} required maxLength={72} /></label>{login.error && <p role="alert" className="error">{login.error instanceof APIError && login.error.status === 401 ? '用户名或密码错误。' : login.error.message}</p>}<button disabled={login.isPending}>{login.isPending ? '正在登录…' : '登录'}</button></form></main>
}
function Spaces({ selected, onSelect }: { selected: Schema['Space'] | null; onSelect: (s: Schema['Space']) => void }) {
  const client = useQueryClient(), [name, setName] = useState('')
  const spaces = useQuery({ queryKey: ['spaces'], queryFn: ({ signal }) => list<Schema['Space']>('/spaces', signal) })
  const create = useMutation({ mutationFn: () => request<Schema['Space']>('/spaces', 'POST', { name }), onSuccess: space => { setName(''); void client.invalidateQueries({ queryKey: ['spaces'] }); onSelect(space) } })
  return <section className="panel"><h2>我的空间</h2><div className="space-list">{spaces.data?.map(s => <button key={s.id} className={s.id === selected?.id ? 'selected' : 'secondary'} onClick={() => onSelect(s)}>{s.name}<small>{s.is_owner ? '管理员' : '成员'}</small></button>)}</div>{spaces.isPending && <p>正在读取空间…</p>}{spaces.data?.length === 0 && <p className="muted">还没有空间。创建一个空间开始协作。</p>}{spaces.error && <p role="alert">{spaces.error.message}</p>}<form className="inline-form" onSubmit={e => { e.preventDefault(); create.mutate() }}><label>新空间名称<input value={name} onChange={e => setName(e.target.value)} maxLength={100} required /></label><button disabled={create.isPending}>创建空间</button></form>{create.error && <p role="alert">{create.error.message}</p>}</section>
}

function SpaceWorkspace({ space, user, onLeave }: { space: Schema['Space']; user: Schema['User']; onLeave: () => void }) {
  const [tab, setTab] = useState('manage')
  return <><div className="actions workspace-tabs"><button className={tab === 'manage' ? 'selected' : 'secondary'} onClick={() => setTab('manage')}>成员与模型</button><button className={tab === 'catalog' ? 'selected' : 'secondary'} onClick={() => setTab('catalog')}>Agent 与 Skill</button></div>{tab === 'manage' ? <SpaceDetail space={space} user={user} onLeave={onLeave} /> : <Catalog space={space} />}</>
}
