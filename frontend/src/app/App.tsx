import { useEffect, useRef, useState } from 'react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { Link, NavLink, Navigate, Route, Routes, useLocation, useMatch, useNavigate, useParams } from 'react-router-dom'
import { APIError, list, request, type Schema } from '../lib/api'
import { Sessions } from '../features/sessions/Sessions'
import { Catalog } from '../features/catalog/Catalog'
import { Members, Models, Credentials } from '../features/spaces/SpaceDetail'
import { legacySpacePath, spacePath, workspaceSections } from './routes'

export function App() {
  const client = useQueryClient(), navigate = useNavigate(), location = useLocation()
  const match = useMatch('/spaces/:spaceId/*'), spaceID = match?.params.spaceId
  const [notice, setNotice] = useState(''), lastSpace = useRef<string | null>(null)
  const me = useQuery({ queryKey: ['me'], queryFn: ({ signal }) => request<Schema['User']>('/auth/me', 'GET', undefined, signal), retry: false, refetchInterval: q => q.state.error ? false : 30000 })
  const spaces = useQuery({ queryKey: ['spaces'], queryFn: ({ signal }) => list<Schema['Space']>('/spaces', signal), enabled: me.isSuccess, refetchInterval: q => q.state.error ? false : 5000 })
  const space = spaces.data?.find(s => s.id === spaceID)
  const section = match?.params['*']?.split('/')[0] ?? ''
  const page = workspaceSections.find(s => s.path === section)
  useEffect(() => {
    document.title = `${space ? `${page?.label ?? '页面未找到'} · ${space.name}` : '我的空间'} · MASS`
    window.scrollTo(0, 0)
  }, [location.pathname, space, page])
  useEffect(() => {
    const lost = (event: Event) => {
      const status = (event as CustomEvent<number>).detail
      void client.cancelQueries(); client.removeQueries({ queryKey: ['private'] })
      setNotice(status === 403 ? '空间访问权限已失效，已关闭当前空间。' : '登录已失效，请重新登录。')
      if (status === 401) { client.removeQueries({ queryKey: ['me'] }); void client.invalidateQueries({ queryKey: ['me'] }) }
      else void client.invalidateQueries({ queryKey: ['spaces'] })
      navigate('/spaces')
    }
    window.addEventListener('mass-access-lost', lost); return () => window.removeEventListener('mass-access-lost', lost)
  }, [client, navigate])
  useEffect(() => { if (me.error instanceof APIError && me.error.status === 401) { void client.cancelQueries({ queryKey: ['private'] }); client.removeQueries({ queryKey: ['private'] }); client.removeQueries({ queryKey: ['spaces'] }) } }, [me.error, client])
  useEffect(() => {
    if (space) { lastSpace.current = space.id; return }
    if (spaceID && spaces.isSuccess && lastSpace.current === spaceID) {
      lastSpace.current = null
      window.dispatchEvent(new CustomEvent('mass-access-lost', { detail: 403 }))
    } else if (!spaceID || lastSpace.current !== spaceID) lastSpace.current = null
  }, [space, spaceID, spaces.isSuccess])
  const logout = useMutation({ mutationFn: () => request('/auth/logout', 'POST'), onSuccess: async () => { await client.cancelQueries(); client.clear(); navigate('/login') } })
  if (me.isPending) return <div className="loading" role="status">正在读取登录状态…</div>
  if (me.isError) {
    if (me.error instanceof APIError && me.error.status === 401) return <Login onSuccess={() => { setNotice(''); void client.invalidateQueries({ queryKey: ['me'] }) }} />
    return <div className="login-card"><h1>暂时无法连接服务</h1><p role="alert">{me.error.message}</p><button onClick={() => { void me.refetch() }}>重试</button></div>
  }
  return <div className="layout"><aside className="app-sidebar"><Link className="brand" to="/spaces">M<span className="brand-dot">A</span>SS<span className="brand-sub">AGENT WORKSPACE</span></Link>
    <nav aria-label="主导航"><NavLink className="all-spaces" to="/spaces" end>我的空间</NavLink></nav>
    {space && <><div className="sidebar-space-name">{space.name}</div>
      <nav aria-label="空间导航">{['工作空间', '定义与资源', '空间管理'].map(group => <div key={group}><div className="nav-caption">{group}</div>{workspaceSections.filter(s => s.group === group && (!('ownerOnly' in s) || space.is_owner)).map(s => <NavLink key={s.path} to={spacePath(space.id, s.path)} end={!s.path}>{s.label}</NavLink>)}</div>)}</nav></>}
    <AccountSettings username={me.data.username} spaces={spaces.data ?? []} spaceID={space?.id} pending={logout.isPending} onLogout={() => logout.mutate()} /></aside>
    <main id="main-content"><nav className="breadcrumbs" aria-label="面包屑"><Link to="/spaces">我的空间</Link>{space && <><span aria-hidden="true">/</span><Link to={spacePath(space.id)}>{space.name}</Link>{section && <><span aria-hidden="true">/</span><span aria-current="page">{page?.label ?? '页面未找到'}</span></>}</>}</nav>
      <header><div><div className="eyebrow">{space ? space.name : 'WORKSPACE'}</div><h1>{space ? page?.label ?? '页面未找到' : '我的空间'}</h1></div><span className="service-status">已连接 MASS 服务</span></header>
      <p className="intro">{space ? page?.description : '选择一个空间，开始协作与任务。'}</p>
      {notice && <div role="alert" className="error">{notice}<button onClick={() => setNotice('')}>关闭</button></div>}{logout.error && <p role="alert">{logout.error.message}</p>}
      <Routes><Route path="/" element={<Navigate to="/spaces" replace />} /><Route path="/login" element={<Navigate to="/spaces" replace />} />
        <Route path="/spaces" element={legacySpacePath(location.search) ? <Navigate to={legacySpacePath(location.search)!} replace /> : <Spaces spaces={spaces.data} pending={spaces.isPending} error={spaces.error} />} />
        <Route path="/spaces/:spaceId/*" element={<SpaceWorkspace user={me.data} space={space} pending={spaces.isPending} error={spaces.error} />} />
        <Route path="*" element={<NotFound />} /></Routes>
      <footer>会话固定定义版本，运行记录与文件快照持续保存。</footer>
    </main></div>
}
function AccountSettings({ username, spaces, spaceID, pending, onLogout }: { username: string; spaces: Schema['Space'][]; spaceID?: string; pending: boolean; onLogout: () => void }) {
  const [open, setOpen] = useState(false), root = useRef<HTMLDivElement>(null), trigger = useRef<HTMLButtonElement>(null), panel = useRef<HTMLDivElement>(null)
  const navigate = useNavigate(), location = useLocation()
  useEffect(() => { setOpen(false) }, [location.pathname])
  useEffect(() => {
    if (!open) return
    panel.current?.focus()
    const outside = (event: PointerEvent) => { if (!root.current?.contains(event.target as Node)) setOpen(false) }
    const escape = (event: KeyboardEvent) => { if (event.key === 'Escape') { setOpen(false); trigger.current?.focus() } }
    document.addEventListener('pointerdown', outside)
    document.addEventListener('keydown', escape)
    return () => { document.removeEventListener('pointerdown', outside); document.removeEventListener('keydown', escape) }
  }, [open])
  return <div className="sidebar-footer account-settings" ref={root} onBlur={event => { if (!event.currentTarget.contains(event.relatedTarget)) setOpen(false) }}>
    <span className="account-name" title={username}>{username}</span>
    <button className="settings-toggle" type="button" ref={trigger} aria-label="账号设置" title="账号设置" aria-expanded={open} aria-controls={open ? 'account-settings-panel' : undefined} aria-haspopup="dialog" onClick={() => setOpen(value => !value)}>
      <svg width="18" height="18" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.6" strokeLinecap="round" strokeLinejoin="round" aria-hidden="true"><path d="m9.2 3-.6 2.2-1.4.8L5 5.5 2.3 10l1.6 1.7v1.6L2.3 15 5 19.5l2.2-.5 1.4.8.6 2.2h5.2l.6-2.2 1.4-.8 2.2.5 2.7-4.5-1.6-1.7v-1.6l1.6-1.7-2.7-4.5-2.2.5-1.4-.8-.6-2.2Z" /><circle cx="11.8" cy="12.5" r="3.2" /></svg>
    </button>
    {open && <div className="account-popover" id="account-settings-panel" role="dialog" aria-label="账号设置" ref={panel} tabIndex={-1}>
      <div className="account-popover-title">账号设置</div>
      <label className="space-switcher">切换空间<select value={spaceID ?? ''} onChange={event => { navigate(spacePath(event.target.value)); setOpen(false); trigger.current?.focus() }} disabled={spaces.length === 0}><option value="" disabled>选择空间</option>{spaces.map(s => <option key={s.id} value={s.id}>{s.name}</option>)}</select></label>
      {spaces.length === 0 && <p className="muted">暂无可切换的空间。</p>}
      <div className="account-popover-divider" />
      <button className="logout-action" type="button" disabled={pending} onClick={onLogout}>{pending ? '正在退出…' : '退出登录'}</button>
    </div>}
  </div>
}
function Login({ onSuccess }: { onSuccess: () => void }) {
  const [username, setUsername] = useState(''), [password, setPassword] = useState('')
  const login = useMutation({ mutationFn: () => request<Schema['User']>('/auth/login', 'POST', { username, password }), onSuccess: () => { setPassword(''); onSuccess() } })
  return <main className="login-card"><div className="eyebrow">MASS WORKSPACE</div><h1>登录 MASS</h1><p className="intro">使用管理员为你创建的账号。</p><form onSubmit={e => { e.preventDefault(); login.mutate() }}><label>用户名<input autoComplete="username" value={username} onChange={e => setUsername(e.target.value)} required /></label><label>密码<input type="password" autoComplete="current-password" value={password} onChange={e => setPassword(e.target.value)} required maxLength={72} /></label>{login.error && <p role="alert" className="error">{login.error instanceof APIError && login.error.status === 401 ? '用户名或密码错误。' : login.error.message}</p>}<button disabled={login.isPending}>{login.isPending ? '正在登录…' : '登录'}</button></form></main>
}
function Spaces({ spaces, pending, error }: { spaces?: Schema['Space'][]; pending: boolean; error: Error | null }) {
  const client = useQueryClient(), navigate = useNavigate(), [name, setName] = useState('')
  const create = useMutation({ mutationFn: () => request<Schema['Space']>('/spaces', 'POST', { name }), onSuccess: space => { setName(''); void client.invalidateQueries({ queryKey: ['spaces'] }); navigate(spacePath(space.id)) } })
  return <><section className="space-grid" aria-label="空间列表">{spaces?.map(s => <Link className="space-card" key={s.id} to={spacePath(s.id)}><span className="space-card-icon" aria-hidden="true">◈</span><h2>{s.name}</h2><small>{s.is_owner ? '管理员' : '成员'}</small><span className="card-link">进入空间 →</span></Link>)}</section>
    {pending && <p role="status">正在读取空间…</p>}{spaces?.length === 0 && <div className="empty-state"><h2>还没有空间</h2><p>创建一个空间，邀请成员并配置你的第一个 Agent。</p></div>}{error && <p role="alert">{error.message}</p>}
    <section className="panel"><h2>创建空间</h2><p className="muted">每个空间独立管理成员、模型、定义和会话。</p><form className="inline-form" onSubmit={e => { e.preventDefault(); create.mutate() }}><label>新空间名称<input value={name} onChange={e => setName(e.target.value)} maxLength={100} required /></label><button disabled={create.isPending}>创建空间</button></form>{create.error && <p role="alert">{create.error.message}</p>}</section></>
}
function SpaceWorkspace({ space, user, pending, error }: { space?: Schema['Space']; user: Schema['User']; pending: boolean; error: Error | null }) {
  const client = useQueryClient(), navigate = useNavigate(), { spaceId } = useParams()
  useEffect(() => () => { void client.cancelQueries({ queryKey: ['private', spaceId] }); client.removeQueries({ queryKey: ['private', spaceId] }) }, [client, spaceId])
  if (pending) return <p role="status">正在读取空间…</p>
  if (error) return <p role="alert">{error.message}</p>
  if (!space) return <section className="panel"><h2>空间不存在或无权访问</h2><Link className="text-link" to="/spaces">返回我的空间</Link></section>
  return <Routes><Route index element={<SpaceOverview space={space} />} />
    <Route path="members" element={<Members key={space.id} space={space} user={user} onLeave={() => navigate('/spaces')} />} />
    <Route path="models" element={<Models key={space.id} space={space} />} />
    <Route path="credentials" element={space.is_owner ? <Credentials key={space.id} space={space} /> : <section className="panel"><h2>需要管理员权限</h2><p>请联系空间管理员管理供应商凭证。</p><Link className="text-link" to={spacePath(space.id)}>返回空间概览</Link></section>} />
    <Route path="agents" element={<Catalog key={`${space.id}-agents`} space={space} kind="agent" />} />
    <Route path="agents/:definitionId" element={<Catalog key={`${space.id}-agents`} space={space} kind="agent" />} />
    <Route path="skills" element={<Catalog key={`${space.id}-skills`} space={space} kind="skill" />} />
    <Route path="skills/:definitionId" element={<Catalog key={`${space.id}-skills`} space={space} kind="skill" />} />
    <Route path="sessions" element={<Sessions key={space.id} space={space.id} />} />
    <Route path="sessions/:sessionId" element={<Sessions key={space.id} space={space.id} />} />
    <Route path="*" element={<NotFound />} /></Routes>
}
function SpaceOverview({ space }: { space: Schema['Space'] }) {
  return <><section className="panel overview-welcome"><div className="eyebrow">{space.is_owner ? '管理员' : '空间成员'}</div><h2>{space.name}</h2><p className="muted">从会话开始工作，在定义与资源中组织能力，在空间管理中配置协作环境。</p><Link className="primary-link" to={spacePath(space.id, 'sessions')}>进入会话与运行 →</Link></section>
    {['定义与资源', '空间管理'].map(group => <section className="overview-group" key={group}><h2>{group}</h2><div className="overview-grid">{workspaceSections.filter(s => s.group === group && (!('ownerOnly' in s) || space.is_owner)).map(s => <Link className="overview-card" key={s.path} to={spacePath(space.id, s.path)}><h3>{s.label}</h3><p>{s.description}</p><span className="card-link">进入 →</span></Link>)}</div></section>)}</>
}
function NotFound() { return <section className="panel"><h2>页面未找到</h2><p className="muted">请从导航选择功能，或返回空间列表。</p><Link className="text-link" to="/spaces">返回我的空间</Link></section> }
