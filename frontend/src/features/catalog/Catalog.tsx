import { Link, useNavigate, useParams } from 'react-router-dom'
import { sessionPath } from '../../app/routes'
import { lazy, Suspense, useEffect, useRef, useState } from 'react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import Markdown from 'react-markdown'
import remarkGfm from 'remark-gfm'
import { APIError, list, request, type Schema } from '../../lib/api'
import { SkillWorkspace, assetPayload, maxResourceBytes, resourceParent, skillDocument } from './SkillWorkspace'
const CodeEditor = lazy(() => import('./CodeEditor'))
type Kind = 'agent' | 'skill'
type Asset = Schema['DefinitionAsset']
type Draft = Schema['DefinitionDraft']; type Doc = Schema['DefinitionDocument']; type Version = Schema['DefinitionVersion']
type ConfigAction = 'draft' | 'publish' | 'debug'
const tools = ['list_files', 'read_file', 'write_file', 'edit_file', 'glob_files', 'search_files', 'move_file', 'delete_file', 'list_skill_resources', 'read_skill_resource', 'read_attachment', 'run_code', 'update_plan', 'publish_artifact']
const errorText = (e: unknown) => e instanceof Error ? e.message : '操作未完成'
const displayVersion = (v: Version) => v.debug ? `调试快照 ${v.id.slice(0, 8)}` : v.label
export function Catalog({ space, kind }: { space: Schema['Space']; kind: Kind }) {
  const { definitionId: selected = '' } = useParams(), navigate = useNavigate()
  const [name, setName] = useState('')
  const client = useQueryClient(), base = `/spaces/${space.id}/${kind}s`
  const query = useQuery({ queryKey: ['private', space.id, kind, 'list'], queryFn: ({ signal }) => list<Schema['Definition']>(base, signal), refetchInterval: q => q.state.error ? false : 5000 })
  const refresh = () => { void client.invalidateQueries({ queryKey: ['private', space.id] }) }
  const select = (id: string) => navigate(`${base}/${id}`)
  const create = useMutation({ mutationFn: () => request<Draft>(base, 'POST', { name }), onSuccess: d => { select(d.id); setName(''); refresh() } })
  return <section className="panel catalog">{selected ? <><Link className="text-link back-link" to={base}>← 返回 {kind === 'skill' ? 'Skills' : 'Agents'} 列表</Link><DefinitionEditor key={`${kind}-${selected}`} space={space.id} kind={kind} id={selected} onCopied={select} refresh={refresh} /></> : <>
    <h2>{kind === 'skill' ? 'Skill 方法库' : 'Agent 定义'}</h2><p className="muted">保存草稿后发布独立版本。Agent 始终绑定指定的 Skill 版本。</p>
    <form className="inline-form" onSubmit={e => { e.preventDefault(); create.mutate() }}><label>定义名称<input value={name} onChange={e => setName(e.target.value)} pattern="[a-z][a-z0-9-]{0,63}" placeholder="例如 report-writer" required /></label><button disabled={create.isPending}>创建 {kind === 'skill' ? 'Skill' : 'Agent'}</button></form>
    {[query.error, create.error].filter(Boolean).map((e, i) => <p role="alert" key={i}>{errorText(e)}</p>)}
    <div className="definition-list">{query.data?.map(d => <Link className="resource-link" key={d.id} to={`${base}/${d.id}`}>{d.name}{d.archived ? '（已归档）' : ''}<span aria-hidden="true"> →</span></Link>)}</div>
    {query.isPending && <p role="status">正在读取定义…</p>}{query.data?.length === 0 && <p className="muted">还没有 {kind === 'skill' ? 'Skill' : 'Agent'}，创建第一个定义开始使用。</p>}
  </>}</section>
}
function DefinitionEditor(props: { space: string; kind: Kind; id: string; onCopied: (id: string) => void; refresh: () => void }) {
  const { space, kind, id } = props
  const query = useQuery({ queryKey: ['private', space, kind, id, 'draft'], queryFn: ({ signal }) => request<Draft>(`/spaces/${space}/${kind}s/${id}/draft`, 'GET', undefined, signal) })
  if (query.error && !query.data) return <p role="alert">{query.error.message}</p>
  if (!query.data) return <p role="status">正在读取定义…</p>
  return <EditorForm {...props} initial={query.data} />
}
function EditorForm({ space, kind, id, initial, refresh, onCopied }: { space: string; kind: Kind; id: string; initial: Draft; refresh: () => void; onCopied: (id: string) => void }) {
  const debugAbort = useRef(new AbortController())
  useEffect(() => { const controller = new AbortController(); debugAbort.current = controller; return () => controller.abort() }, [])
  const navigate = useNavigate(), debugRequest = useRef<{ version: string; key: string } | null>(null)
  const base = `/spaces/${space}/${kind}s/${id}`
  const [draft, setDraft] = useState(initial), [doc, setDoc] = useState<Doc>(initial.document), [label, setLabel] = useState('v1'), [notice, setNotice] = useState('')
  const [conflict, setConflict] = useState<Draft | null>(null), [copyName, setCopyName] = useState(''), [versionID, setVersionID] = useState(''), [fromID, setFromID] = useState('')
  const uploadInput = useRef<HTMLInputElement>(null)
  const [file, setFile] = useState<File | null>(null), [path, setPath] = useState<string | null>(null), [preview, setPreview] = useState(false)
  const [activePath, setActivePath] = useState(skillDocument)
  const [resourceEdits, setResourceEdits] = useState(() => new Map<string, string>())
  const changeResource = (id: string, text: string | undefined) => setResourceEdits(edits => {
    if (edits.get(id) === text) return edits
    const next = new Map(edits)
    if (text === undefined) next.delete(id)
    else next.set(id, text)
    return next
  })
  const versions = useQuery({ queryKey: ['private', space, kind, id, 'versions'], queryFn: ({ signal }) => list<Version>(`${base}/versions`, signal) })
  const snapshots = useQuery({ queryKey: ['private', space, kind, id, 'snapshots'], queryFn: ({ signal }) => list<Version>(`${base}/debug-snapshots`, signal), enabled: kind === 'agent' })
  const all = [...(versions.data ?? []), ...(snapshots.data ?? [])]
  const selected = all.find(v => v.id === versionID)
  const detail = useQuery({ queryKey: ['private', space, kind, id, 'version', versionID], queryFn: ({ signal }) => request<Version>(`${base}/${selected?.debug ? 'debug-snapshots' : 'versions'}/${versionID}`, 'GET', undefined, signal), enabled: !!selected })
  const references = useQuery({ queryKey: ['private', space, kind, id, 'refs', versionID], queryFn: ({ signal }) => list<Schema['DefinitionReference']>(`${base}/versions/${versionID}/references`, signal), enabled: kind === 'skill' && !!versionID })
  const diff = useQuery({ queryKey: ['private', space, kind, id, 'diff', fromID, versionID], queryFn: ({ signal }) => request<Schema['DefinitionDiff']>(`${base}/diff?from=${fromID}&to=${versionID}`, 'GET', undefined, signal), enabled: !!fromID && !!versionID })
  const documentChanged = JSON.stringify(doc) !== JSON.stringify(draft.document)
  const changed = documentChanged || resourceEdits.size > 0
  useEffect(() => {
    if (kind !== 'skill' || !changed) return
    const confirmExit = (event: BeforeUnloadEvent) => { event.preventDefault(); event.returnValue = '' }
    window.addEventListener('beforeunload', confirmExit)
    return () => window.removeEventListener('beforeunload', confirmExit)
  }, [changed, kind])
  const accept = (d: Draft) => { setDraft(d); setDoc(d.document); setConflict(null); refresh() }
  const acceptResourceChange = (d: Draft, target?: string) => {
    setDraft(d)
    setDoc(current => ({
      ...current,
      directories: d.document.directories,
      assets: d.document.assets?.filter(asset => asset.path === target || current.assets?.some(local => local.path === asset.path)),
    }))
    setConflict(null)
    refresh()
  }
  const failed = async (e: Error) => { if (e instanceof APIError && e.code === 'draft_conflict') { try { setConflict(await request<Draft>(`${base}/draft`)) } catch { /* request handles revoked access */ } } }
  const saveDocument = (document: Doc) => request<Draft>(`${base}/draft`, 'PUT', { expected_revision: draft.revision, document })
  const save = useMutation({
    mutationFn: ({ directory, configYaml }: { directory?: string; configYaml?: string } = {}) => saveDocument(directory
      ? { ...draft.document, directories: [...(draft.document.directories ?? []), directory] }
      : { ...doc, config_yaml: configYaml ?? doc.config_yaml }),
    onSuccess: (d, options) => {
      const directory = options?.directory
      if (directory) {
        acceptResourceChange(d)
        setActivePath(directory)
        setPath(null)
      } else {
        accept(d)
        setNotice('草稿已保存')
      }
    },
    onError: failed,
  })
  const publish = useMutation({
    mutationFn: async ({ debug = false, configYaml }: { debug?: boolean; configYaml?: string } = {}) => {
      let revision = draft.revision
      if (kind === 'agent' && (documentChanged || (configYaml ?? doc.config_yaml) !== doc.config_yaml)) {
        const saved = await saveDocument({ ...doc, config_yaml: configYaml ?? doc.config_yaml })
        accept(saved)
        setNotice('草稿已保存')
        revision = saved.revision
      }
      return request<Version>(`${base}/${debug ? 'debug-snapshots' : 'versions'}`, 'POST', { expected_revision: revision, label: debug ? '' : label })
    },
    onSuccess: v => { refresh(); setVersionID(v.id); setNotice(v.debug ? '调试快照已固定，可在会话与运行中选择它。' : `已发布 ${v.label}`) },
    onError: failed,
  })
  const debug = useMutation({ mutationFn: async () => {
    if (!detail.data || detail.data.archived) throw new Error('请选择可用的固定版本')
    if (debugRequest.current?.version !== detail.data.id) debugRequest.current = { version: detail.data.id, key: crypto.randomUUID() }
    return request<Schema['RuntimeSession']>(`/spaces/${space}/sessions`, 'POST', { agent_id: id, version_id: detail.data.id, title: `调试 · ${draft.name}`, idempotency_key: debugRequest.current.key }, debugAbort.current.signal)
  }, onSuccess: session => { if (debugAbort.current.signal.aborted) return; navigate(sessionPath(space, session.id)) } })
  const mutateState = useMutation({ mutationFn: ({ suffix, body }: { suffix: string; body: unknown }) => request(`${base}/${suffix}`, 'PUT', body), onSuccess: async () => { accept(await request<Draft>(`${base}/draft`)); setNotice('状态已更新') } })
  const copy = useMutation({ mutationFn: () => request<Draft>(`${base}/copy`, 'POST', { name: copyName }), onSuccess: d => { refresh(); onCopied(d.id) } })
  const upload = useMutation({
    mutationFn: async ({ target, bytes, edited, creating }: { target: string; bytes: Blob; edited?: Asset; creating?: boolean }) => {
      if (creating && draft.document.assets?.some(asset => asset.path.toLowerCase() === target.toLowerCase())) {
        throw new Error('此位置已有同名文件。')
      }
      if (bytes.size > maxResourceBytes) throw new Error('资源文件不能超过 8 MiB。')
      const data = await new Promise<string>((resolve, reject) => {
        const reader = new FileReader()
        reader.onload = () => resolve(String(reader.result).split(',')[1])
        reader.onerror = () => reject(new Error('读取文件失败'))
        reader.readAsDataURL(bytes)
      })
      return request<Draft>(`${base}/resources`, 'POST', { expected_revision: draft.revision, path: target, media_type: edited?.media_type || bytes.type || 'application/octet-stream', data_base64: data })
    },
    onSuccess: (d, { target, edited, creating }) => {
      if (edited) {
        acceptResourceChange(d, target)
        changeResource(edited.path, undefined)
        setNotice('文件已保存')
      } else if (creating) {
        acceptResourceChange(d, target)
        setActivePath(target)
        setPath(null)
        setNotice('已新建文件')
      } else {
        accept(d)
        setFile(null)
        if (uploadInput.current) uploadInput.current.value = ''
        setActivePath(target)
        setPath(null)
        setNotice('资源已上传并保存到草稿')
      }
    },
    onError: failed,
  })
  const pending = save.isPending || publish.isPending || upload.isPending || mutateState.isPending
  const changeMarkdown = (markdown: string) => setDoc(d => ({ ...d, markdown }))
  const removeAsset = (asset: Asset) => {
    setDoc(d => ({ ...d, assets: d.assets?.filter(a => a.path !== asset.path) }))
    setActivePath(skillDocument)
  }
  const previewContent = preview && <div className="markdown-preview"><Markdown remarkPlugins={[remarkGfm]} skipHtml components={{ img: () => null, a: ({ href, children }) => href && /^https?:\/\//i.test(href) ? <a href={href} target="_blank" rel="noreferrer">{children}</a> : <span>{children}</span> }}>{doc.markdown}</Markdown></div>
  const previewToggle = <button type="button" className="secondary" aria-expanded={preview} onClick={() => setPreview(!preview)}>{preview ? '关闭预览' : '预览正文'}</button>
  const assetSelected = doc.assets?.some(asset => asset.path === activePath)
  const folderSelected = doc.directories?.some(path => path === activePath || path.startsWith(`${activePath}/`)) || doc.assets?.some(asset => asset.path.startsWith(`${activePath}/`))
  const uploadFolder = folderSelected ? activePath : assetSelected ? resourceParent(activePath) : ''
  const uploadPrefix = uploadFolder ? `${uploadFolder}/` : ''
  const uploadPath = path ?? (uploadPrefix + (file?.name ?? ''))
  const selectSkillPath = (next: string) => {
    setActivePath(next)
    setPath(null)
  }
  const uploadForm = <div className="form-grid">
    <label>资源路径<input value={uploadPath} placeholder="例如 scripts/python/example.py" onChange={e => setPath(e.target.value)} /></label>
    <label>资源文件（最多 8 MiB）<input ref={uploadInput} type="file" onChange={e => setFile(e.target.files?.[0] ?? null)} /></label>
    <button type="button" disabled={pending || changed || !file || !uploadPath || draft.archived} onClick={() => { if (file) upload.mutate({ target: uploadPath, bytes: file }) }}>上传资源</button>
    {changed && <p className="muted">请先保存当前修改。</p>}
  </div>
  return <div className="definition-editor"><h3>{draft.name} · {kind === 'agent' ? 'Agent' : 'Skill'}</h3><p className="muted">草稿{kind === 'agent' && ` revision ${draft.revision}`}{changed ? ' · 有未保存修改' : ' · 已保存'}{draft.archived ? ' · 已归档' : ''}</p>
    {kind === 'skill' ? <SkillWorkspace space={space} markdown={doc.markdown} assets={doc.assets ?? []} directories={doc.directories} activePath={activePath} edits={resourceEdits}
      documentChanged={documentChanged} pending={pending} archived={draft.archived} previewControl={previewToggle} preview={previewContent}
      uploadForm={uploadForm} onSelect={selectSkillPath} onCreateDirectory={directory => save.mutateAsync({ directory })} onMarkdownChange={changeMarkdown}
      onCreateFile={target => upload.mutateAsync({ target, bytes: new Blob([], { type: 'text/plain' }), creating: true })}
      onResourceChange={changeResource} onSaveDocument={() => save.mutate({})}
      onSaveResource={(asset, text) => upload.mutate({ target: asset.path, bytes: new Blob([text]), edited: asset })}
      onRemove={removeAsset}
    /> : <>
      <Suspense fallback={<p>正在载入编辑器…</p>}>
        <CodeEditor label="AGENTS.md" value={doc.markdown} onChange={changeMarkdown} readOnly={pending || draft.archived} />
      </Suspense>
      <div className="actions">{previewToggle}</div>
      {previewContent}
      <ConfigBuilder key={draft.revision} space={space} name={draft.name} config={doc.config_yaml} disabled={pending || draft.archived}
        label={label} onLabelChange={setLabel}
        onAction={(action, configYaml) => action === 'draft' ? save.mutate({ configYaml }) : publish.mutate({ debug: action === 'debug', configYaml })} />
      <details className="config-builder"><summary>高级配置</summary>
        <Suspense fallback={<p>正在载入编辑器…</p>}><CodeEditor label="agent.yaml" language="yaml" value={doc.config_yaml}
          onChange={config_yaml => setDoc(d => ({ ...d, config_yaml }))} readOnly={pending || draft.archived} /></Suspense>
      </details>
    </>}
    {kind === 'skill' && [!save.variables && save.error, !upload.variables?.creating && upload.error].filter(Boolean).map((e, i) => <p role="alert" key={i}>{errorText(e)}</p>)}
    {conflict && <div className="error"><p role="alert">草稿已被其他编辑者修改。本地内容已保留，请查看差异后重新编辑。</p><Suspense fallback={<p>读取差异…</p>}><CodeEditor label="草稿冲突差异" language="yaml" original={JSON.stringify(conflict.document, null, 2)} value={JSON.stringify(doc, null, 2)} /></Suspense><button disabled={pending} onClick={() => { accept(conflict); setResourceEdits(new Map()); save.reset(); upload.reset() }}>载入服务器草稿</button></div>}
    {kind === 'skill' && <><h3>发布</h3><div className="inline-form"><label>发布版本号<input value={label} onChange={e => setLabel(e.target.value)} maxLength={64} /></label><button disabled={pending || changed || draft.archived} onClick={() => publish.mutate({})}>发布版本</button></div></>}
    <h3>版本与差异</h3><div className="form-grid"><label>查看版本<select value={versionID} onChange={e => { setVersionID(e.target.value); setFromID('') }}><option value="">选择历史版本</option>{all.map(v => <option key={v.id} value={v.id}>{displayVersion(v)}{v.archived ? '（已归档）' : ''}{draft.default_version_id === v.id ? '（默认）' : ''}</option>)}</select></label><label>对比基准<select value={fromID} onChange={e => setFromID(e.target.value)}><option value="">不比较</option>{all.filter(v => v.id !== versionID).map(v => <option key={v.id} value={v.id}>{displayVersion(v)}</option>)}</select></label></div>
    {debug.error && <p role="alert">{debug.error.message}</p>}{detail.data && <div className="version-detail"><h3>不可变版本：{displayVersion(detail.data)}</h3><p className="muted">SHA-256：{detail.data.sha256}</p><pre data-testid="version-markdown">{detail.data.bundle.document.markdown}</pre><pre data-testid="version-config">{detail.data.bundle.document.config_yaml}</pre>{detail.data.bundle.bindings.map(b => <p key={b.skill_id}>Skill {b.skill_id} → {b.version} · {b.version_id}</p>)}{detail.data.bundle.document.assets?.map(a => <p key={a.path}><a download href={`/api/v1${assetPayload(space, a)}`}>{a.path}</a></p>)}
      <div className="actions">{kind === 'agent' && !detail.data.archived && <button disabled={debug.isPending} onClick={() => debug.mutate()}>使用此版本开始调试</button>}{kind === 'agent' && !detail.data.debug && !detail.data.archived && <button disabled={pending || draft.archived} onClick={() => mutateState.mutate({ suffix: 'default-version', body: { version_id: versionID } })}>设为默认版本</button>}<button className="secondary" disabled={pending || kind === 'skill' && changed} onClick={() => mutateState.mutate({ suffix: `versions/${versionID}/archive`, body: { archived: !detail.data!.archived } })}>{detail.data.archived ? '恢复版本选择' : '归档此版本'}</button></div>
      {kind === 'skill' && <div><h4>引用此版本的 Agent</h4>{references.data?.map(r => <p key={r.id}>{r.name} · {r.debug ? '调试快照' : r.label}</p>)}{references.data?.length === 0 && <p className="muted">暂无引用</p>}</div>}
    </div>}
    {diff.data && <Suspense fallback={<p>载入版本差异…</p>}><CodeEditor key={`${fromID}-${versionID}`} label="版本差异" language="yaml" original={JSON.stringify(diff.data.from.bundle, null, 2)} value={JSON.stringify(diff.data.to.bundle, null, 2)} /></Suspense>}
    <div className="inline-form"><label>复制为新名称<input value={copyName} onChange={e => setCopyName(e.target.value)} /></label><button className="secondary" disabled={copy.isPending || changed || !copyName} onClick={() => copy.mutate()}>复制定义</button><button className="secondary" disabled={pending || changed} onClick={() => mutateState.mutate({ suffix: 'archive', body: { archived: !draft.archived } })}>{draft.archived ? '恢复定义' : '归档定义'}</button></div>
    {notice && <p role="status">{notice}</p>}{[kind === 'agent' && save.error, publish.error, mutateState.error, copy.error, versions.error, snapshots.error, detail.error, references.error, diff.error].filter(Boolean).map((e, i) => <p role="alert" key={i}>{errorText(e)}</p>)}
  </div>
}
function ConfigBuilder({ space, name, config, disabled, label, onLabelChange, onAction }: {
  space: string; name: string; config: string; disabled: boolean; label: string
  onLabelChange: (label: string) => void
  onAction: (action: ConfigAction, config: string) => void
}) {
  const models = useQuery({ queryKey: ['private', space, 'catalog-models'], queryFn: ({ signal }) => list<Schema['Model']>(`/spaces/${space}/models?available=true`, signal) })
  const skills = useQuery({ queryKey: ['private', space, 'catalog-skills'], queryFn: async ({ signal }) => {
    const definitions = await list<Schema['Definition']>(`/spaces/${space}/skills`, signal)
    return Promise.all(definitions.filter(d => !d.archived).map(async d => ({ ...d, versions: (await list<Version>(`/spaces/${space}/skills/${d.id}/versions`, signal)).filter(v => !v.archived) })))
  } })
  type Selection = { model: string; skills: Record<string, string>; tools: string[]; input: number }
  const [edits, setEdits] = useState<{ source: string; selection: Selection }>()
  let existing: Schema['DefinitionConfig'] | undefined
  try { existing = JSON.parse(config) as Schema['DefinitionConfig'] } catch { /* Raw YAML is kept for advanced editing. */ }
  const selection = edits?.source === config ? edits.selection : undefined
  const state: Selection = selection ?? {
    model: existing?.model ?? '',
    skills: Object.fromEntries((existing?.skills ?? []).map(skill => [skill.id, skill.version])),
    tools: existing ? existing.tools ?? [] : ['read_file', 'write_file', 'publish_artifact'],
    input: existing?.context?.max_input_tokens ?? 24000,
  }
  const select = (change: Partial<Selection>) => setEdits({ source: config, selection: { ...state, ...change } })
  const model = models.data?.find(m => m.id === state.model)
  const submit = (action: ConfigAction) => {
    if (!selection) {
      onAction(action, config)
      return
    }
    if (!model) return
    const output = Math.min(existing?.context?.reserved_output_tokens ?? 4000, model.max_output_tokens, Math.floor(model.context_window / 2))
    onAction(action, JSON.stringify({
      schema_version: 1,
      name,
      model: state.model,
      skills: Object.entries(state.skills).filter(([, version]) => version).map(([id, version]) => ({ id, version })),
      tools: state.tools,
      context: {
        max_input_tokens: Math.min(state.input, model.context_window - output),
        reserved_output_tokens: output,
        compression: existing?.context?.compression ?? 'summary',
      },
      limits: existing?.limits ?? { max_steps: 30, timeout_seconds: 300 },
    }, null, 2))
  }
  const canPublish = selection ? !!model : !!config.trim()
  return <details className="config-builder" open>
    <summary>选择模型、Skill 版本与工具</summary>
    <div className="form-grid">
      <label>绑定模型<select disabled={disabled} value={state.model} onChange={e => select({ model: e.target.value })}>
        <option value="">选择可用模型</option>{models.data?.map(m => <option value={m.id} key={m.id}>{m.name}</option>)}
      </select></label>
      <label>输入 Token 上限<input disabled={disabled} type="number" min={1} max={2000000} value={state.input} onChange={e => select({ input: Number(e.target.value) })} /></label>
      {skills.data?.map(skill => <label key={skill.id}>Skill 版本：{skill.name}
        <select disabled={disabled} aria-label={`Skill 版本：${skill.name}`} value={state.skills[skill.id] ?? ''} onChange={e => select({ skills: { ...state.skills, [skill.id]: e.target.value } })}>
          <option value="">不绑定</option>{skill.versions.map(v => <option value={v.label} key={v.id}>{v.label}</option>)}
        </select>
      </label>)}
    </div>
    <div className="tool-options">{tools.map(tool => <label className="checkbox" key={tool}>
      <input type="checkbox" disabled={disabled} checked={state.tools.includes(tool)}
        onChange={e => select({ tools: e.target.checked ? [...state.tools, tool] : state.tools.filter(t => t !== tool) })} />{tool}
    </label>)}</div>
    {state.tools.includes('run_code') && <p className="muted">run_code 允许 JavaScript 读写当前会话文件及发送 HTTP 请求；平台不提供 OS 沙箱，也不会自动执行 Skill 脚本。</p>}
    <div className="inline-form">
      <label>发布版本号<input disabled={disabled} value={label} onChange={e => onLabelChange(e.target.value)} maxLength={64} /></label>
      <button type="button" disabled={disabled || !canPublish || !label.trim()} onClick={() => submit('publish')}>发布版本</button>
      <button type="button" className="secondary" disabled={disabled || !!selection && !model} onClick={() => submit('draft')}>保存草稿</button>
      <button type="button" className="secondary" disabled={disabled || !canPublish} onClick={() => submit('debug')}>创建调试快照</button>
    </div>
    {[models.error, skills.error].filter(Boolean).map((e, i) => <p role="alert" key={i}>{errorText(e)}</p>)}
  </details>
}
