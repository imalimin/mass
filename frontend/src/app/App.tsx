import { NavLink, Route, Routes } from 'react-router-dom'
import { useQuery } from '@tanstack/react-query'
import { getSystem } from '../lib/api'

function Spaces() {
  return <section className="empty-state" aria-labelledby="spaces-title">
    <div className="space-icon" aria-hidden="true">◈</div>
    <h2 id="spaces-title">从一个协作空间开始</h2>
    <p>空间将集中管理你的 Agent、Skills 与会话。<br />空间创建和成员管理将在后续阶段开放。</p>
    <span className="tag">空间功能 · 建设中</span>
  </section>
}

export function App() {
  const system = useQuery({ queryKey: ['system'], queryFn: getSystem })
  return <div className="layout">
    <aside><a className="brand" href="/">M<span className="brand-dot">A</span>SS<span className="brand-sub">AGENT WORKSPACE</span></a>
      <div className="nav-caption">工作台</div><nav><NavLink to="/spaces">◈ <span>空间</span></NavLink></nav>
      <div className="sidebar-footer"><span className="status-dot" />工程基础 · D00</div>
    </aside>
    <main><header><div><div className="eyebrow">WORKSPACE</div><h1>空间</h1></div><span className="service-status">{system.isError ? '服务信息读取失败' : system.isPending ? '正在连接服务…' : '已连接 MASS 服务'}</span></header>
      <p className="intro">组织任务方法，构建可复用的 Agent。</p>
      {system.isError && <div role="alert" className="error">暂时无法读取服务信息。<button onClick={() => { void system.refetch() }}>重试</button></div>}
      <Routes><Route path="/" element={<Spaces />} /><Route path="/spaces" element={<Spaces />} /><Route path="*" element={<p>页面不存在。</p>} /></Routes>
      <footer>当前为工程基础预览，尚未开放登录、Agent 编辑与运行。</footer>
    </main>
  </div>
}
