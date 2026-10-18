import { lazy, Suspense, useState } from 'react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import Markdown from 'react-markdown'
import remarkGfm from 'remark-gfm'
import { APIError, list, request, type Schema } from '../../lib/api'
const CodeEditor = lazy(() => import('./CodeEditor'))
type Kind = 'agent' | 'skill'
type Draft = Schema['DefinitionDraft']; type Doc = Schema['DefinitionDocument']; type Version = Schema['DefinitionVersion']
const tools = ['list_files', 'read_file', 'write_file', 'edit_file', 'glob_files', 'search_files', 'move_file', 'delete_file', 'list_skill_resources', 'read_skill_resource', 'read_attachment', 'run_code', 'update_plan', 'publish_artifact']
const errorText = (e: unknown) => e instanceof Error ? e.message : '操作未完成'
const displayVersion = (v: Version) => v.debug ? `调试快照 ${v.id.slice(0, 8)}` : v.label
export function Catalog({ space }: { space: Schema['Space'] }) {
  const [kind, setKind] = useState<Kind>('skill'), [selected, setSelected] = useState(''), [name, setName] = useState('')
  const client = useQueryClient(), base = `/spaces/${space.id}/${kind}s`
  const query = useQuery({ queryKey: ['private', space.id, kind, 'list'], queryFn: ({ signal }) => list<Schema['Definition']>(base, signal), refetchInterval: q => q.state.error ? false : 5000 })
  const refresh = () => { void client.invalidateQueries({ queryKey: ['private', space.id] }) }
  const create = useMutation({ mutationFn: () => request<Draft>(base, 'POST', { name }), onSuccess: d => { setSelected(d.id); setName(''); refresh() } })
  return <section className="panel catalog"><div className="actions"><button className={kind === 'skill' ? 'selected' : 'secondary'} onClick={() => { setKind('skill'); setSelected('') }}>Skills</button><button className={kind === 'agent' ? 'selected' : 'secondary'} onClick={() => { setKind('agent'); setSelected('') }}>Agents</button></div>
    <h2>{kind === 'skill' ? 'Skill 方法库' : 'Agent 定义'}</h2><p className="muted">保存草稿后发布独立版本。Agent 始终绑定指定的 Skill 版本。</p>
    <form className="inline-form" onSubmit={e => { e.preventDefault(); create.mutate() }}><label>定义名称<input value={name} onChange={e => setName(e.target.value)} pattern="[a-z][a-z0-9-]{0,63}" placeholder="例如 report-writer" required /></label><button disabled={create.isPending}>创建 {kind === 'skill' ? 'Skill' : 'Agent'}</button></form>
    {[query.error, create.error].filter(Boolean).map((e, i) => <p role="alert" key={i}>{errorText(e)}</p>)}
    <div className="definition-list">{query.data?.map(d => <button className={d.id === selected ? 'selected' : 'secondary'} key={d.id} onClick={() => setSelected(d.id)}>{d.name}{d.archived ? '（已归档）' : ''}</button>)}</div>
    {selected && <DefinitionEditor key={`${kind}-${selected}`} space={space.id} kind={kind} id={selected} onCopied={setSelected} refresh={refresh} />}
  </section>
}
function DefinitionEditor(props: { space: string; kind: Kind; id: string; onCopied: (id: string) => void; refresh: () => void }) {
  const { space, kind, id } = props
  const query = useQuery({ queryKey: ['private', space, kind, id, 'draft'], queryFn: ({ signal }) => request<Draft>(`/spaces/${space}/${kind}s/${id}/draft`, 'GET', undefined, signal) })
  if (query.error) return <p role="alert">{query.error.message}</p>
  if (!query.data) return <p role="status">正在读取定义…</p>
  return <EditorForm {...props} initial={query.data} />
}
function EditorForm({ space, kind, id, initial, refresh, onCopied }: { space: string; kind: Kind; id: string; initial: Draft; refresh: () => void; onCopied: (id: string) => void }) {
  const base = `/spaces/${space}/${kind}s/${id}`
  const [draft, setDraft] = useState(initial), [doc, setDoc] = useState<Doc>(initial.document), [label, setLabel] = useState('v1'), [notice, setNotice] = useState('')
  const [conflict, setConflict] = useState<Draft | null>(null), [copyName, setCopyName] = useState(''), [versionID, setVersionID] = useState(''), [fromID, setFromID] = useState('')
  const [file, setFile] = useState<File | null>(null), [path, setPath] = useState('assets/'), [preview, setPreview] = useState(false)
  const versions = useQuery({ queryKey: ['private', space, kind, id, 'versions'], queryFn: ({ signal }) => list<Version>(`${base}/versions`, signal) })
  const snapshots = useQuery({ queryKey: ['private', space, kind, id, 'snapshots'], queryFn: ({ signal }) => list<Version>(`${base}/debug-snapshots`, signal), enabled: kind === 'agent' })
  const all = [...(versions.data ?? []), ...(snapshots.data ?? [])]
  const selected = all.find(v => v.id === versionID)
  const detail = useQuery({ queryKey: ['private', space, kind, id, 'version', versionID], queryFn: ({ signal }) => request<Version>(`${base}/${selected?.debug ? 'debug-snapshots' : 'versions'}/${versionID}`, 'GET', undefined, signal), enabled: !!selected })
  const references = useQuery({ queryKey: ['private', space, kind, id, 'refs', versionID], queryFn: ({ signal }) => list<Schema['DefinitionReference']>(`${base}/versions/${versionID}/references`, signal), enabled: kind === 'skill' && !!versionID })
  const diff = useQuery({ queryKey: ['private', space, kind, id, 'diff', fromID, versionID], queryFn: ({ signal }) => request<Schema['DefinitionDiff']>(`${base}/diff?from=${fromID}&to=${versionID}`, 'GET', undefined, signal), enabled: !!fromID && !!versionID })
  const changed = JSON.stringify(doc) !== JSON.stringify(draft.document)
  const accept = (d: Draft) => { setDraft(d); setDoc(d.document); setConflict(null); refresh() }
  const failed = async (e: Error) => { if (e instanceof APIError && e.code === 'draft_conflict') { try { setConflict(await request<Draft>(`${base}/draft`)) } catch { /* request handles revoked access */ } } }
  const save = useMutation({ mutationFn: () => request<Draft>(`${base}/draft`, 'PUT', { expected_revision: draft.revision, document: doc }), onSuccess: d => { accept(d); setNotice('草稿已保存') }, onError: failed })
  const publish = useMutation({ mutationFn: (debug: boolean) => request<Version>(`${base}/${debug ? 'debug-snapshots' : 'versions'}`, 'POST', { expected_revision: draft.revision, label: debug ? '' : label }), onSuccess: v => { refresh(); setVersionID(v.id); setNotice(v.debug ? '调试快照已固定，运行功能将在 D04 开放。' : `已发布 ${v.label}`) }, onError: failed })
  const mutateState = useMutation({ mutationFn: ({ suffix, body }: { suffix: string; body: unknown }) => request(`${base}/${suffix}`, 'PUT', body), onSuccess: async () => { accept(await request<Draft>(`${base}/draft`)); setNotice('状态已更新') } })
  const copy = useMutation({ mutationFn: () => request<Draft>(`${base}/copy`, 'POST', { name: copyName }), onSuccess: d => { refresh(); onCopied(d.id) } })
  const upload = useMutation({ mutationFn: async () => {
    if (!file || file.size > 8 * 1024 * 1024) throw new Error('请选择不超过 8 MiB 的资源文件。')
    const data = await new Promise<string>((resolve, reject) => { const r = new FileReader(); r.onload = () => resolve(String(r.result).split(',')[1]); r.onerror = () => reject(new Error('读取文件失败')); r.readAsDataURL(file) })
    return request<Draft>(`${base}/resources`, 'POST', { expected_revision: draft.revision, path, media_type: file.type || 'application/octet-stream', data_base64: data })
  }, onSuccess: d => { accept(d); setFile(null); setNotice('资源已上传并保存到草稿') }, onError: failed })
  const pending = save.isPending || publish.isPending || upload.isPending || mutateState.isPending
  return <div className="definition-editor"><h3>{draft.name} · {kind === 'agent' ? 'Agent' : 'Skill'}</h3><p className="muted">草稿 revision {draft.revision}{changed ? ' · 有未保存修改' : ' · 已保存'}{draft.archived ? ' · 已归档' : ''}</p>
    <Suspense fallback={<p>正在载入编辑器…</p>}><CodeEditor label={kind === 'agent' ? 'AGENTS.md' : 'SKILL.md'} value={doc.markdown} onChange={v => setDoc(d => ({ ...d, markdown: v }))} />
      {kind === 'agent' && <><ConfigBuilder space={space} name={draft.name} initial={doc.config_yaml} onApply={v => setDoc(d => ({ ...d, config_yaml: v }))} /><h3>agent.yaml</h3><CodeEditor label="agent.yaml" language="yaml" value={doc.config_yaml} onChange={v => setDoc(d => ({ ...d, config_yaml: v }))} /></>}
    </Suspense>
    <div className="actions"><button disabled={pending || draft.archived} onClick={() => save.mutate()}>保存草稿</button><button className="secondary" onClick={() => setPreview(!preview)}>{preview ? '关闭预览' : '预览正文'}</button></div>
    {preview && <div className="markdown-preview"><Markdown remarkPlugins={[remarkGfm]} skipHtml components={{ img: () => null, a: ({ href, children }) => href && /^https?:\/\//i.test(href) ? <a href={href} target="_blank" rel="noreferrer">{children}</a> : <span>{children}</span> }}>{doc.markdown}</Markdown></div>}
    {conflict && <div className="error"><p role="alert">草稿已被其他编辑者修改。本地内容已保留，请查看差异后重新编辑。</p><Suspense fallback={<p>读取差异…</p>}><CodeEditor label="草稿冲突差异" language="yaml" original={JSON.stringify(conflict.document, null, 2)} value={JSON.stringify(doc, null, 2)} /></Suspense><button onClick={() => { accept(conflict); save.reset() }}>载入服务器草稿</button></div>}
    <h3>资源</h3>{doc.assets.map(a => <div className="resource-row" key={a.path}><a href={`/api/v1/spaces/${space}/payloads/${a.object_id}`} download>{a.path} · {a.size} 字节</a><button className="secondary" onClick={() => setDoc(d => ({ ...d, assets: d.assets.filter(x => x.path !== a.path) }))}>移除 {a.path}</button></div>)}
    <div className="form-grid"><label>资源路径<input value={path} onChange={e => setPath(e.target.value)} /></label><label>资源文件（最多 8 MiB）<input type="file" onChange={e => { const f = e.target.files?.[0] ?? null; setFile(f); if (f) setPath(`assets/${f.name}`) }} /></label><button disabled={pending || changed || !file || draft.archived} onClick={() => upload.mutate()}>上传资源</button><p className="muted">先保存正文修改再上传。移除草稿资源后，历史版本仍保留原文件。</p></div>
    <h3>发布</h3><div className="inline-form"><label>发布版本号<input value={label} onChange={e => setLabel(e.target.value)} maxLength={64} /></label><button disabled={pending || changed || draft.archived} onClick={() => publish.mutate(false)}>发布版本</button>{kind === 'agent' && <button className="secondary" disabled={pending || changed || draft.archived} onClick={() => publish.mutate(true)}>创建调试快照</button>}</div>
    <h3>版本与差异</h3><div className="form-grid"><label>查看版本<select value={versionID} onChange={e => { setVersionID(e.target.value); setFromID('') }}><option value="">选择历史版本</option>{all.map(v => <option key={v.id} value={v.id}>{displayVersion(v)}{v.archived ? '（已归档）' : ''}{draft.default_version_id === v.id ? '（默认）' : ''}</option>)}</select></label><label>对比基准<select value={fromID} onChange={e => setFromID(e.target.value)}><option value="">不比较</option>{all.filter(v => v.id !== versionID).map(v => <option key={v.id} value={v.id}>{displayVersion(v)}</option>)}</select></label></div>
    {detail.data && <div className="version-detail"><h3>不可变版本：{displayVersion(detail.data)}</h3><p className="muted">SHA-256：{detail.data.sha256}</p><pre data-testid="version-markdown">{detail.data.bundle.document.markdown}</pre><pre data-testid="version-config">{detail.data.bundle.document.config_yaml}</pre>{detail.data.bundle.bindings.map(b => <p key={b.skill_id}>Skill {b.skill_id} → {b.version} · {b.version_id}</p>)}{detail.data.bundle.document.assets.map(a => <p key={a.path}><a download href={`/api/v1/spaces/${space}/payloads/${a.object_id}`}>{a.path}</a></p>)}
      <div className="actions">{kind === 'agent' && !detail.data.debug && !detail.data.archived && <button disabled={pending || draft.archived} onClick={() => mutateState.mutate({ suffix: 'default-version', body: { version_id: versionID } })}>设为默认版本</button>}<button className="secondary" disabled={pending} onClick={() => mutateState.mutate({ suffix: `versions/${versionID}/archive`, body: { archived: !detail.data!.archived } })}>{detail.data.archived ? '恢复版本选择' : '归档此版本'}</button></div>
      {kind === 'skill' && <div><h4>引用此版本的 Agent</h4>{references.data?.map(r => <p key={r.id}>{r.name} · {r.debug ? '调试快照' : r.label}</p>)}{references.data?.length === 0 && <p className="muted">暂无引用</p>}</div>}
    </div>}
    {diff.data && <Suspense fallback={<p>载入版本差异…</p>}><CodeEditor key={`${fromID}-${versionID}`} label="版本差异" language="yaml" original={JSON.stringify(diff.data.from.bundle, null, 2)} value={JSON.stringify(diff.data.to.bundle, null, 2)} /></Suspense>}
    <div className="inline-form"><label>复制为新名称<input value={copyName} onChange={e => setCopyName(e.target.value)} /></label><button className="secondary" disabled={copy.isPending || changed || !copyName} onClick={() => copy.mutate()}>复制定义</button><button className="secondary" disabled={pending || changed} onClick={() => mutateState.mutate({ suffix: 'archive', body: { archived: !draft.archived } })}>{draft.archived ? '恢复定义' : '归档定义'}</button></div>
    {notice && <p role="status">{notice}</p>}{[save.error, publish.error, upload.error, mutateState.error, copy.error, versions.error, snapshots.error, detail.error, references.error, diff.error].filter(Boolean).map((e, i) => <p role="alert" key={i}>{errorText(e)}</p>)}
  </div>
}
function ConfigBuilder({ space, name, initial, onApply }: { space: string; name: string; initial: string; onApply: (value: string) => void }) {
  const models = useQuery({ queryKey: ['private', space, 'catalog-models'], queryFn: ({ signal }) => list<Schema['Model']>(`/spaces/${space}/models?available=true`, signal) })
  const skills = useQuery({ queryKey: ['private', space, 'catalog-skills'], queryFn: async ({ signal }) => {
    const definitions = await list<Schema['Definition']>(`/spaces/${space}/skills`, signal)
    return Promise.all(definitions.filter(d => !d.archived).map(async d => ({ ...d, versions: (await list<Version>(`/spaces/${space}/skills/${d.id}/versions`, signal)).filter(v => !v.archived) })))
  } })
  const [state, setState] = useState<{ model: string; skills: Record<string, string>; tools: string[] }>(() => {
    try { const c = JSON.parse(initial) as Schema['DefinitionConfig']; return { model: c.model, skills: Object.fromEntries((c.skills ?? []).map(s => [s.id, s.version])), tools: c.tools ?? [] } } catch { return { model: '', skills: {}, tools: ['read_file', 'write_file', 'read_skill_resource'] } }
  })
  const apply = () => {
    const m = models.data?.find(m => m.id === state.model); if (!m) return
    const output = Math.min(4000, m.max_output_tokens, Math.floor(m.context_window / 2))
    onApply(JSON.stringify({ schema_version: 1, name, model: state.model, skills: Object.entries(state.skills).filter(([, v]) => v).map(([id, version]) => ({ id, version })), tools: state.tools, context: { max_input_tokens: Math.min(24000, m.context_window - output), reserved_output_tokens: output, compression: 'summary' }, limits: { max_steps: 30, timeout_seconds: 300 } }, null, 2))
  }
  return <details className="config-builder" open><summary>选择模型、Skill 版本与工具</summary><div className="form-grid"><label>绑定模型<select value={state.model} onChange={e => setState(s => ({ ...s, model: e.target.value }))}><option value="">选择可用模型</option>{models.data?.map(m => <option key={m.id} value={m.id}>{m.name}</option>)}</select></label>{skills.data?.map(s => <label key={s.id}>Skill 版本：{s.name}<select aria-label={`Skill 版本：${s.name}`} value={state.skills[s.id] ?? ''} onChange={e => setState(v => ({ ...v, skills: { ...v.skills, [s.id]: e.target.value } }))}><option value="">不绑定</option>{s.versions.map(v => <option value={v.label} key={v.id}>{v.label}</option>)}</select></label>)}</div>
    <div className="tool-options">{tools.map(t => <label className="checkbox" key={t}><input type="checkbox" checked={state.tools.includes(t)} onChange={e => setState(s => ({ ...s, tools: e.target.checked ? [...s.tools, t] : s.tools.filter(x => x !== t) }))} />{t}</label>)}</div>
    {state.tools.includes('run_code') && <p className="muted">run_code 允许 JavaScript 读写当前会话文件及发送 HTTP 请求；平台不提供 OS 沙箱，也不会自动执行 Skill 脚本。</p>}
    <button className="secondary" disabled={!state.model} onClick={apply}>应用配置选择</button><p className="muted">应用后可在 agent.yaml 中继续编辑。发布时再次检查模型能力、限额与准确版本。</p>{[models.error, skills.error].filter(Boolean).map((e, i) => <p role="alert" key={i}>{errorText(e)}</p>)}
  </details>
}
