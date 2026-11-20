import { useEffect, useRef, useState } from 'react'
import { useMutation, useQueries, useQuery, useQueryClient } from '@tanstack/react-query'
import { Link, NavLink, Navigate, Route, Routes, useLocation, useNavigate, useParams, useSearchParams } from 'react-router-dom'
import { agentSessionsPath, spacePath } from '../../app/routes'
import { list, request, type Schema } from '../../lib/api'
import './evaluation.css'

type Dataset = Schema['EvaluationDataset']
type DatasetInput = Schema['EvaluationDatasetInput']
type Case = Schema['EvaluationCase']
type Experiment = Schema['EvaluationExperiment']
type Progress = Schema['EvaluationProgress']
const labels: Record<string, string> = { queued: '排队中', running: '执行中', completed: '已结束', interrupted: '已中断', pending: '待执行', active: '执行中', succeeded: '成功', failed: '失败', unavailable: '进度已丢失' }
const newTurn = () => ({ content: [{ type: 'text' as const, text: '' }] })
const newCase = (): Case => ({ turns: [newTurn()] })
const newRequestKey = () => Array.from(crypto.getRandomValues(new Uint8Array(16)), byte => byte.toString(16).padStart(2, '0')).join('')
const maxPersistedConcurrency = 65_535
const countTurns = (cases?: Case[]) => (cases ?? []).reduce((n, c) => n + c.turns.length, 0)
const errorText = (error: unknown) => error instanceof Error ? error.message : ''
const caseText = (c: Case, turn: number) => c.turns[turn].content.map(part => part.type === 'text' ? part.text : '').join('')
function move<T>(items: T[], index: number, delta: number): T[] {
  const next = [...items]; [next[index], next[index + delta]] = [next[index + delta], next[index]]; return next
}
export function Evaluation({ space }: { space: string }) {
  const location = useLocation(), base = spacePath(space, 'evaluation')
  return <div className="evaluation-workspace"><div className="evaluation-heading"><div><h2>评测与实验</h2><p className="muted">用固定的输入和 Agent 版本，重复验证单轮与多轮对话。</p></div>
    <nav aria-label="测评导航"><NavLink to={`${base}/datasets`}>评测集</NavLink><NavLink to={`${base}/experiments`}>实验</NavLink></nav></div>
    <Routes><Route index element={<Navigate to="datasets" replace />} /><Route path="datasets" element={<Datasets space={space} />} />
      <Route path="datasets/:datasetId" element={<DatasetEditor key={space + location.pathname} space={space} />} />
      <Route path="experiments" element={<Experiments space={space} />} />
      <Route path="experiments/:experimentId" element={<ExperimentDetail space={space} />} />
      <Route path="*" element={<p>测评页面不存在。</p>} /></Routes>
  </div>
}
function Datasets({ space }: { space: string }) {
  const base = `/spaces/${space}/evaluation`, path = spacePath(space, 'evaluation/datasets')
  const datasets = useQuery({ queryKey: ['private', space, 'evaluation', 'datasets'], queryFn: ({ signal }) => list<Dataset>(`${base}/datasets`, signal) })
  return <section className="panel"><div className="evaluation-toolbar"><h2>评测集</h2><Link className="primary-link" to={`${path}/new`}>新建评测集</Link></div>
    {datasets.isPending && <p role="status">正在读取评测集…</p>}{datasets.error && <p role="alert">{datasets.error.message}</p>}
    {datasets.data?.length === 0 && <p className="muted">创建第一份评测集，添加用例及每轮用户输入。</p>}
    <div className="evaluation-table"><table><thead><tr><th>名称</th><th>当前版本</th><th>用例数</th><th>保存时间</th></tr></thead><tbody>{datasets.data?.map(d => <tr key={d.id}><td><Link to={`${path}/${d.id}`}>{d.name}</Link></td><td>v{d.version}</td><td>{d.case_count}</td><td>{new Date(d.saved_at).toLocaleString()}</td></tr>)}</tbody></table></div>
  </section>
}
function DatasetEditor({ space }: { space: string }) {
  const { datasetId = '' } = useParams(), [params, setParams] = useSearchParams(), navigate = useNavigate(), client = useQueryClient()
  const fresh = datasetId === 'new', version = params.get('version'), base = `/spaces/${space}/evaluation/datasets`
  const [draft, setDraft] = useState<DatasetInput>({ name: '', cases: [newCase()] }), [history, setHistory] = useState('')
  const dataset = useQuery({ queryKey: ['private', space, 'evaluation', 'dataset', datasetId, version], enabled: !fresh,
    queryFn: ({ signal }) => request<Dataset>(`${base}/${datasetId}${version ? `/versions/${encodeURIComponent(version)}` : ''}`, 'GET', undefined, signal) })
  useEffect(() => { if (dataset.data) { setDraft({ name: dataset.data.name, cases: dataset.data.cases ?? [] }); setHistory(String(dataset.data.version)) } }, [dataset.data])
  const save = useMutation({ mutationFn: async () => {
    if (new TextEncoder().encode(JSON.stringify(draft)).length > 1024 * 1024) throw new Error('整份评测集不能超过 1 MiB，请减少输入内容。')
    return request<Dataset>(fresh ? base : `${base}/${datasetId}/versions`, 'POST', draft)
  }, onSuccess: d => { client.setQueryData(['private', space, 'evaluation', 'dataset', d.id, null], d); void client.invalidateQueries({ queryKey: ['private', space, 'evaluation', 'datasets'] }); navigate(spacePath(space, `evaluation/datasets/${d.id}`)); } })
  const readOnly = !!version, disabled = readOnly || save.isPending
  const editCase = (index: number, c: Case) => setDraft(previous => ({ ...previous, cases: previous.cases.map((v, i) => i === index ? c : v) }))
  if (!fresh && dataset.isPending) return <p role="status">正在读取评测集…</p>
  if (dataset.error) return <p role="alert">{dataset.error.message}</p>
  return <section className="panel"><div className="evaluation-toolbar"><h2>{fresh ? '新建评测集' : `${readOnly ? '查看' : '编辑'}评测集 · v${dataset.data?.version}`}</h2><Link to={spacePath(space, 'evaluation/datasets')}>返回列表</Link></div>
    <p className="muted">每条用例单独开始会话，同一用例的多轮输入依次发送。保存会生成新版本，已有版本保持不变。</p>
    {!fresh && <div className="evaluation-toolbar"><label>历史版本<input aria-label="历史版本号" type="number" min="1" value={history} onChange={e => setHistory(e.target.value)} /></label>
      <button className="secondary" disabled={!/^[1-9][0-9]*$/.test(history)} onClick={() => setParams({ version: history })}>查看版本</button>{readOnly && <button className="secondary" onClick={() => setParams({})}>编辑当前版本</button>}</div>}
    <form onSubmit={e => { e.preventDefault(); if (!disabled) save.mutate() }}><fieldset disabled={disabled}>
      <label>评测集名称<input required maxLength={100} value={draft.name} onChange={e => setDraft(previous => ({ ...previous, name: e.target.value }))} /></label>
      <p>{draft.cases.length} 条用例 · {countTurns(draft.cases)} 轮输入</p>
      {draft.cases.map((c, index) => <section className="evaluation-case" key={index}><div className="evaluation-toolbar"><h3>用例 {index + 1}</h3><div className="actions">
        <button type="button" className="secondary" aria-label={`上移用例 ${index + 1}`} disabled={index === 0} onClick={() => setDraft(p => ({ ...p, cases: move(p.cases, index, -1) }))}>↑</button>
        <button type="button" className="secondary" aria-label={`下移用例 ${index + 1}`} disabled={index === draft.cases.length - 1} onClick={() => setDraft(p => ({ ...p, cases: move(p.cases, index, 1) }))}>↓</button>
        <button type="button" className="secondary" onClick={() => setDraft(p => ({ ...p, cases: p.cases.filter((_, i) => i !== index) }))}>移除用例</button></div></div>
        {c.turns.map((_, turn) => <div className="evaluation-turn" key={turn}><label>第 {turn + 1} 轮用户输入<textarea aria-label={`第 ${turn + 1} 轮用户输入`} required rows={3} maxLength={262144} value={caseText(c, turn)} onChange={e => editCase(index, { turns: c.turns.map((v, i) => i === turn ? { content: [{ type: 'text', text: e.target.value }] } : v) })} /></label>
          <div className="actions"><button type="button" className="secondary" disabled title="暂不支持图片输入">图片（暂不支持）</button><button type="button" className="secondary" disabled title="暂不支持视频输入">视频（暂不支持）</button>
            <button type="button" className="secondary" disabled={turn === 0} aria-label={`上移用例 ${index + 1} 第 ${turn + 1} 轮`} onClick={() => editCase(index, { turns: move(c.turns, turn, -1) })}>↑</button>
            <button type="button" className="secondary" disabled={turn === c.turns.length - 1} aria-label={`下移用例 ${index + 1} 第 ${turn + 1} 轮`} onClick={() => editCase(index, { turns: move(c.turns, turn, 1) })}>↓</button>
            <button type="button" className="secondary" disabled={c.turns.length === 1} onClick={() => editCase(index, { turns: c.turns.filter((_, i) => i !== turn) })}>移除本轮</button></div>
        </div>)}
        <button type="button" className="secondary" disabled={c.turns.length >= 10} onClick={() => editCase(index, { turns: [...c.turns, newTurn()] })}>添加一轮</button>
      </section>)}
      <div className="actions"><button type="button" className="secondary" disabled={draft.cases.length >= 100} onClick={() => setDraft(p => ({ ...p, cases: [...p.cases, newCase()] }))}>添加用例</button><button disabled={!draft.name.trim()}>{save.isPending ? '正在保存…' : fresh ? '创建 v1' : '保存新版本'}</button></div>
    </fieldset></form>{save.error && <p role="alert">{save.error.message}</p>}
  </section>
}
function Experiments({ space }: { space: string }) {
  const base = `/spaces/${space}/evaluation`, path = spacePath(space, 'evaluation/experiments'), client = useQueryClient(), navigate = useNavigate()
  const datasets = useQuery({ queryKey: ['private', space, 'evaluation', 'datasets'], queryFn: ({ signal }) => list<Dataset>(`${base}/datasets`, signal) })
  const agents = useQuery({ queryKey: ['private', space, 'evaluation', 'agents'], queryFn: ({ signal }) => list<Schema['Definition']>(`/spaces/${space}/agents`, signal) })
  const experiments = useQuery({ queryKey: ['private', space, 'evaluation', 'experiments'], queryFn: ({ signal }) => list<Experiment>(`${base}/experiments`, signal), refetchInterval: q => q.state.error ? false : 3000 })
  const [datasetID, setDatasetID] = useState(''), [agentID, setAgentID] = useState(''), [versionID, setVersionID] = useState(''), [concurrency, setConcurrency] = useState('1')
  const versions = useQuery({ queryKey: ['private', space, 'evaluation', 'versions', agentID], queryFn: ({ signal }) => list<Schema['DefinitionVersion']>(`/spaces/${space}/agents/${agentID}/versions`, signal), enabled: !!agentID })
  const selected = datasets.data?.find(d => d.id === datasetID)
  const preview = useQuery({ queryKey: ['private', space, 'evaluation', 'dataset', datasetID, null], queryFn: ({ signal }) => request<Dataset>(`${base}/datasets/${datasetID}`, 'GET', undefined, signal), enabled: !!datasetID })
  const requestKey = useRef<string | null>(null)
  const launch = useMutation({
    mutationFn: () => {
      requestKey.current ??= newRequestKey()
      // The API value fits the existing storage type. This ceiling exceeds the
      // maximum dataset size, so larger inputs still run every case in parallel.
      return request<Experiment>(`${base}/experiments`, 'POST', {
        dataset_id: datasetID,
        agent_id: agentID,
        agent_version_id: versionID,
        concurrency: Math.min(Number(concurrency), maxPersistedConcurrency),
        idempotency_key: requestKey.current,
      })
    },
    onSuccess: experiment => {
      requestKey.current = null
      void client.invalidateQueries({ queryKey: ['private', space, 'evaluation', 'experiments'] })
      navigate(`${path}/${experiment.id}`)
    },
  })
  const change = () => { requestKey.current = null; launch.reset() }
  return <><section className="panel"><h2>发起实验</h2><form onSubmit={e => { e.preventDefault(); launch.mutate() }}><fieldset disabled={launch.isPending}><div className="evaluation-form-grid">
    <label>评测集<select aria-label="评测集" required value={datasetID} onChange={e => { change(); setDatasetID(e.target.value) }}><option value="">选择评测集</option>{datasets.data?.map(d => <option key={d.id} value={d.id} disabled={d.case_count === 0}>{d.name} · v{d.version} · {d.case_count} 条用例</option>)}</select></label>
    <label>Agent<select aria-label="Agent" required value={agentID} onChange={e => { change(); setAgentID(e.target.value); setVersionID('') }}><option value="">选择 Agent</option>{agents.data?.filter(a => !a.archived).map(a => <option key={a.id} value={a.id}>{a.name}</option>)}</select></label>
    <label>发布版本<select aria-label="发布版本" required value={versionID} onChange={e => { change(); setVersionID(e.target.value) }} disabled={!agentID || versions.isPending}><option value="">选择发布版本</option>{versions.data?.filter(v => !v.archived && !v.debug).map(v => <option key={v.id} value={v.id}>{v.label}</option>)}</select></label>
    <label>并发用例数<input inputMode="numeric" pattern="0*[1-9][0-9]*" title="请输入大于 0 的整数" required value={concurrency} onChange={e => { change(); setConcurrency(e.target.value) }} /></label></div>
    {selected && <p className="muted">使用发起时的最新保存版本 · {selected.case_count} 条用例 · {countTurns(preview.data?.cases)} 轮输入。</p>}
    <button disabled={!datasetID || !agentID || !versionID || launch.isPending}>{launch.isPending ? '正在发起…' : '发起实验'}</button>
  </fieldset></form>{[datasets.error, agents.error, versions.error, preview.error, launch.error].filter(Boolean).map((e, i) => <p role="alert" key={i}>{errorText(e)}</p>)}</section>
    <section className="panel"><h2>实验记录</h2>{experiments.error && <p role="alert">{experiments.error.message}</p>}{experiments.isPending && <p role="status">正在读取实验…</p>}
      {experiments.data?.length === 0 && <p className="muted">还没有实验。</p>}<div className="evaluation-table"><table><thead><tr><th>实验</th><th>评测集版本</th><th>状态</th><th>已结束 / 总用例</th><th>成功 / 失败</th></tr></thead><tbody>{[...(experiments.data ?? [])].sort((a, b) => b.created_at.localeCompare(a.created_at)).map(v => <tr key={v.id}><td><Link to={`${path}/${v.id}`}>{new Date(v.created_at).toLocaleString()}</Link><small>{v.id}</small></td><td>v{v.dataset_version}</td><td>{labels[v.status]}</td><td>{v.succeeded + v.failed} / {v.case_count}</td><td>{v.succeeded} / {v.failed}</td></tr>)}</tbody></table></div>
    </section></>
}
const metricNames = ['input_tokens', 'input_cached_tokens', 'output_tokens', 'reasoning_tokens', 'duration_ms', 'step_count'] as const
const metricLabels = ['输入 Token', '缓存输入 Token', '输出 Token', '推理 Token', '耗时 ms', '模型步骤']
function ExperimentDetail({ space }: { space: string }) {
  const { experimentId = '' } = useParams(), base = `/spaces/${space}/evaluation/experiments/${experimentId}`
  const experiment = useQuery({ queryKey: ['private', space, 'evaluation', 'experiment', experimentId], queryFn: ({ signal }) => request<Experiment>(base, 'GET', undefined, signal), refetchInterval: q => q.state.error || ['completed', 'interrupted'].includes(q.state.data?.status ?? '') ? false : 1500 })
  const v = experiment.data, progress = v?.result ?? v?.case_progress
  const activeRuns = Object.values(progress ?? {}).filter(p => p.status === 'active' && p.run_ids.length > 0).map(p => p.run_ids[p.run_ids.length - 1])
  const runs = useQueries({ queries: activeRuns.map(id => ({ queryKey: ['private', space, 'evaluation', 'run', id],
    queryFn: ({ signal }: { signal: AbortSignal }) => request<Schema['RuntimeRun']>(`/spaces/${space}/runs/${id}`, 'GET', undefined, signal),
    refetchInterval: 1500 })) })
  const succeeded = new Set(runs.flatMap(q => q.data?.status === 'succeeded' ? [q.data.id] : []))
  const dataset = useQuery({ queryKey: ['private', space, 'evaluation', 'frozen', v?.dataset_id, v?.dataset_version], queryFn: ({ signal }) => request<Dataset>(`/spaces/${space}/evaluation/datasets/${v!.dataset_id}/versions/${v!.dataset_version}`, 'GET', undefined, signal), enabled: !!v })
  const [expanded, setExpanded] = useState<number | null>(null)
  if (experiment.isPending) return <p role="status">正在读取实验…</p>
  if (experiment.error) return <p role="alert">{experiment.error.message}</p>
  if (!v) return null
  return <section className="panel"><div className="evaluation-toolbar"><h2>实验 · {labels[v.status]}</h2><Link to={spacePath(space, 'evaluation/experiments')}>返回实验列表</Link></div>
    <p><code>{v.id}</code></p><p>评测集：<Link to={`${spacePath(space, `evaluation/datasets/${v.dataset_id}`)}?version=${v.dataset_version}`}>{dataset.data?.name ?? v.dataset_id} · v{v.dataset_version}</Link> · 并发 {v.concurrency} · Agent <code>{v.agent_id}</code> · 版本 <code>{v.agent_version_id}</code></p>
    <p role="status">成功 {v.succeeded} · 失败 {v.failed} · 未完成 {v.case_count - v.succeeded - v.failed}</p>
    {v.status === 'interrupted' && <p className="error">实验已中断，未保存的过程数据不可用。请从实验列表发起新的实验。</p>}{dataset.error && <p role="alert">{dataset.error.message}</p>}
    <div className="evaluation-table"><table><thead><tr><th>用例</th><th>状态</th><th>已受理 / 成功 / 总轮次</th>{metricLabels.map(label => <th key={label}>{label}</th>)}<th>回放</th></tr></thead><tbody>{dataset.data?.cases?.map((c, i) => {
      const p = progress?.[String(i + 1)], turns = p?.run_ids.length ?? 0
      const complete = completedTurns(p, succeeded.has(p?.run_ids[p.run_ids.length - 1] ?? ''))
      return <tr key={i}><td><button className="text-link" onClick={() => setExpanded(expanded === i ? null : i)}>用例 {i + 1}</button></td><td>{labels[p?.status ?? (v.status === 'interrupted' ? 'unavailable' : 'pending')]}{p?.failure_code && <small>{p.failure_stage !== 'create_session' && <>第 {p.failure_stage === 'submit_run' ? turns + 1 : turns} 轮 · </>}{p.failure_stage} · {p.failure_code}</small>}</td><td>{turns} / {complete} / {c.turns.length}</td>{metricNames.map(name => <td key={name}>{p?.[name] ?? '—'}</td>)}<td>{p?.session_id && <Link to={agentSessionsPath(space, v.agent_id, p.session_id)}>查看会话</Link>}</td></tr>
    })}</tbody></table></div>
    {expanded !== null && dataset.data?.cases?.[expanded] && <div className="evaluation-case"><h3>用例 {expanded + 1} · 输入与运行</h3>{dataset.data.cases[expanded].turns.map((t, i) => {
      const p = progress?.[String(expanded + 1)], run = p?.run_ids[i]
      return <div className="evaluation-turn" key={i}><strong>第 {i + 1} 轮</strong><pre>{t.content.map(part => part.type === 'text' ? part.text : JSON.stringify(part)).join('\n')}</pre>{run && p?.session_id ? <Link to={`${agentSessionsPath(space, v.agent_id, p.session_id)}?run=${run}`}>查看本轮 Run · {run}</Link> : <span className="muted">未受理</span>}</div>
    })}</div>}
    <p className="muted">耗时为首轮受理到最后一轮进入收尾的时间跨度；— 表示指标暂不可用。</p>
  </section>
}
function completedTurns(p: Progress | undefined, latestSucceeded: boolean) {
  if (!p) return 0
  const n = p.run_ids.length
  if (p.status === 'succeeded' || p.failure_stage === 'submit_run' || latestSucceeded) return n
  return Math.max(0, n - 1)
}
