import { Link, useNavigate, useParams } from 'react-router-dom'
import { agentSessionsPath, creativitySessionPath } from '../../app/routes'
import { FormDialog } from '../../components/FormDialog'
import { Icon } from '../../components/Icon'
import { WorkspaceListTable, WorkspaceListToolbar } from '../../components/WorkspaceList'
import { lazy, Suspense, useEffect, useMemo, useRef, useState } from 'react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import Markdown from 'react-markdown'
import remarkGfm from 'remark-gfm'
import { APIError, list, newRequestKey, request, type Schema } from '../../lib/api'
import { SkillWorkspace, assetPayload, maxResourceBytes, resourceParent, skillDocument } from './SkillWorkspace'
const CodeEditor = lazy(() => import('./CodeEditor'))
type Kind = 'agent' | 'skill'
type Asset = Schema['DefinitionAsset']
type Draft = Schema['DefinitionDraft']; type Doc = Schema['DefinitionDocument']; type Version = Schema['DefinitionVersion']
type ConfigAction = 'draft' | 'publish' | 'debug'
const agentLimits = {
  max_steps: { label: '最大步数', max: 1000, initial: 30 },
  timeout_seconds: { label: '运行超时（秒）', max: 86400, initial: 300 },
} as const
type Limit = keyof typeof agentLimits
const limitFields = Object.keys(agentLimits) as Limit[]
const validLimit = (key: Limit, value: string) => Number.isInteger(Number(value)) && Number(value) >= 1 && Number(value) <= agentLimits[key].max
const tools = ['run_command', 'read_file', 'write_file', 'edit_file', 'read_attachment', 'update_plan']
const errorText = (e: unknown) => e instanceof Error ? e.message : '操作未完成'
const displayVersion = (v: Version) => v.debug ? `调试快照 ${v.id.slice(0, 8)}` : v.label
const definitionNamePattern = String.raw`[a-z][a-z0-9_\-]{0,63}`
const definitionNameHint = '以小写英文字母开头，最多 64 个字符，可使用小写英文字母、数字、连字符（-）和下划线（_）。'
const definitionDate = new Intl.DateTimeFormat('sv-SE')
const parseConfig = (value: string) => {
  if (!value.trim()) return undefined
  try {
    const parsed: unknown = JSON.parse(value)
    return parsed && typeof parsed === 'object' && !Array.isArray(parsed) ? parsed as Schema['DefinitionConfig'] : undefined
  } catch { return undefined }
}
export function Catalog({ space, kind }: { space: Schema['Space']; kind: Kind }) {
  const { definitionId: selected = '' } = useParams(), navigate = useNavigate()
  const [name, setName] = useState('')
  const [creating, setCreating] = useState(false)
  const [copiedId, setCopiedId] = useState<string | null>(null)
  const copyTimer = useRef<number | null>(null)
  const importInput = useRef<HTMLInputElement>(null)
  useEffect(() => () => {
    if (copyTimer.current !== null) window.clearTimeout(copyTimer.current)
  }, [])
  const copyAgentId = async (id: string) => {
    let copied = false
    if (navigator.clipboard?.writeText) {
      try {
        await navigator.clipboard.writeText(id)
        copied = true
      } catch {
        copied = false
      }
    }
    if (!copied) {
      const textarea = document.createElement('textarea')
      textarea.value = id
      textarea.style.position = 'fixed'
      textarea.style.opacity = '0'
      document.body.appendChild(textarea)
      textarea.select()
      try {
        copied = document.execCommand('copy')
      } finally {
        document.body.removeChild(textarea)
      }
    }
    if (copied) {
      setCopiedId(id)
      if (copyTimer.current !== null) window.clearTimeout(copyTimer.current)
      copyTimer.current = window.setTimeout(() => {
        setCopiedId(current => current === id ? null : current)
      }, 2000)
    }
  }
  const client = useQueryClient(), base = `/spaces/${space.id}/${kind}s`
  const creativityBase = `/spaces/${space.id}/creativity`
  const availability = useQuery({
    queryKey: ['private', space.id, 'creativity'], enabled: kind === 'agent',
    queryFn: ({ signal }) => request<Schema['CreativityAvailability']>(creativityBase, 'GET', undefined, signal),
  })
  const iterationKey = useRef<{ target: string; key: string } | null>(null)
  const iterate = useMutation({
    mutationFn: (target: string) => {
      if (iterationKey.current?.target !== target) iterationKey.current = { target, key: newRequestKey() }
      return request<Schema['RuntimeSession']>(`${creativityBase}/sessions`, 'POST', {
        idempotency_key: iterationKey.current.key, target_agent_id: target,
      })
    },
    onSuccess: session => {
      iterationKey.current = null
      client.setQueryData(['private', space.id, session.id, 'session'], session)
      navigate(creativitySessionPath(space.id, session.id))
    },
    onError: error => {
      if (error instanceof APIError && error.code === 'iteration_draft_changed') iterationKey.current = null
    },
  })
  const iterationButton = (id: string, name?: string) => {
    const pending = iterate.isPending && iterate.variables === id
    const label = pending ? '正在恢复…' : '在创意中迭代'
    return <button type="button" className={name ? 'icon-button agent-action' : 'secondary'}
      disabled={!availability.data?.available || iterate.isPending} aria-label={name ? `在创意中迭代 ${name}` : undefined}
      title={name ? label : undefined} aria-busy={pending} onClick={() => iterate.mutate(id)}>
      <Icon name={pending ? 'refresh' : 'branch'} />{!name && label}
    </button>
  }
  const query = useQuery({ queryKey: ['private', space.id, kind, 'list'], queryFn: ({ signal }) => list<Schema['Definition']>(base, signal), enabled: !selected, refetchInterval: q => q.state.error ? false : 5000 })
  const refresh = () => { void client.invalidateQueries({ queryKey: ['private', space.id] }) }
  const select = (id: string) => navigate(`${base}/${id}`)
  const create = useMutation({ mutationFn: () => request<Draft>(base, 'POST', { name }), onSuccess: d => { setCreating(false); select(d.id); setName(''); refresh() } })
  const importSkill = useMutation({
    mutationFn: (file: File) => request<Draft>(`${base}/import?${new URLSearchParams({ filename: file.name })}`, 'POST', new Blob([file], { type: 'application/zip' })),
    onSuccess: d => { select(d.id); refresh() },
  })
  const iterationNotice = <>
    {kind === 'agent' && availability.data && !availability.data.available && <p className="muted">创作 Agent 暂不可用，无法开始创意迭代。</p>}
    {(iterate.error || availability.error) && <p role="alert">{(iterate.error || availability.error)?.message}</p>}
  </>
  const isAgent = kind === 'agent', kindLabel = isAgent ? 'Agent' : 'Skill'
  const columns = isAgent ? ['名称', 'ID', '最新版本', '更新日期', '迭代', '对话'] : ['名称', '最新版本', '更新日期']
  if (!selected) return <section aria-label={`${kindLabel}s`}>
    <WorkspaceListToolbar space={space} name={`${kindLabel}s`}>
      {!isAgent && <button type="button" className="secondary" disabled={importSkill.isPending || create.isPending}
        onClick={() => importInput.current?.click()}><Icon name="upload" />{importSkill.isPending ? '正在导入…' : '导入 Skill ZIP'}</button>}
      <button type="button" disabled={importSkill.isPending} onClick={() => setCreating(true)}><Icon name="plus" />创建 {kindLabel}</button>
    </WorkspaceListToolbar>
    {!isAgent && <input ref={importInput} type="file" accept=".zip" aria-label="Skill ZIP 文件" hidden onChange={event => {
      const file = event.currentTarget.files?.[0]
      event.currentTarget.value = ''
      if (file) importSkill.mutate(file)
    }} />}
    {iterationNotice}
    {[query.error, importSkill.error].filter(Boolean).map((error, i) => <p role="alert" key={i}>{errorText(error)}</p>)}
    {query.isPending && <p role="status">正在读取定义…</p>}
    {query.data?.length === 0 && <p className="muted">还没有 {kindLabel}，创建第一个定义开始使用。</p>}
    {!!query.data?.length && <WorkspaceListTable label={`${kindLabel} 列表`} columns={columns} className={isAgent ? 'agent-table' : undefined}
      columnGroup={<colgroup><col />{isAgent && <col className="agent-id-column" />}<col className="definition-version-column" /><col className="definition-date-column" />
        {isAgent && <col className="agent-action-column" span={2} />}</colgroup>}>
      {query.data.map(d => <tr key={d.id}>
          <td><Link className="workspace-list-name" to={`${base}/${d.id}`}>{d.name}{d.archived ? '（已归档）' : ''}</Link></td>
          {isAgent && <td>
            <div className="agent-id-cell">
              <code className="agent-id-value">{d.id}</code>
              <button type="button" className="icon-button openapi-copy-icon"
                aria-label={`复制 ${d.name} 的 ID`} title={copiedId === d.id ? '已复制' : '复制 ID'}
                onClick={() => { void copyAgentId(d.id) }}>
                <Icon name={copiedId === d.id ? 'check' : 'copy'} size={15} />
              </button>
            </div>
          </td>}
          <td>{d.latest_version || (d.default_version_id ? '—' : '未发布')}</td>
          <td>{d.updated_at ? <time className="definition-date" dateTime={d.updated_at}>{definitionDate.format(new Date(d.updated_at))}</time> : '—'}</td>
          {isAgent && <><td>{iterationButton(d.id, d.name)}</td>
            <td><Link className="agent-action" to={agentSessionsPath(space.id, d.id)} aria-label={`打开 ${d.name} 的会话`} title="打开会话">
              <Icon name="chat" size={18} />
            </Link></td></>}
        </tr>)}
    </WorkspaceListTable>}
    {creating && <CreateDefinitionDialog kind={kindLabel} name={name} setName={value => { setName(value); if (create.isError) create.reset() }}
      pending={create.isPending} error={create.error}
      submit={() => create.mutate()} close={() => { setCreating(false); setName(''); create.reset() }} />}
  </section>
  return <section className="panel catalog">
    {iterationNotice}
    <Link className="text-link back-link icon-link" to={base}><Icon name="arrowLeft" />返回 {kindLabel}s 列表</Link>
    {isAgent && iterationButton(selected)}
    <DefinitionEditor key={`${kind}-${selected}`} space={space.id} kind={kind} id={selected} onCopied={select} refresh={refresh} />
  </section>
}
function CreateDefinitionDialog({ kind, name, setName, pending, error, submit, close }: {
  kind: string; name: string; setName: (name: string) => void; pending: boolean; error: Error | null; submit: () => void; close: () => void
}) {
  const errorMessage = error instanceof APIError && error.code === 'definition_name_conflict'
    ? `当前空间已存在同名 ${kind}，请更换名称。` : error?.message
  return <FormDialog title={`创建 ${kind}`} pending={pending} close={close}>{dismiss =>
    <form onSubmit={event => { event.preventDefault(); if (!pending) submit() }}>
      <label>名称<input value={name} onChange={event => setName(event.target.value)} pattern={definitionNamePattern}
        aria-describedby="create-definition-name-hint" placeholder="例如 report_writer" maxLength={64} required autoFocus readOnly={pending} /></label>
      <small id="create-definition-name-hint">{definitionNameHint}</small>
      {errorMessage && <p role="alert">{errorMessage}</p>}
      <div className="dialog-actions">
        <button type="button" className="secondary" disabled={pending} onClick={dismiss}><Icon name="close" />取消</button>
        <button type="submit" disabled={pending || !name}><Icon name="plus" />{pending ? '正在创建…' : '创建'}</button>
      </div>
    </form>
  }</FormDialog>
}
function DefinitionEditor(props: { space: string; kind: Kind; id: string; onCopied: (id: string) => void; refresh: () => void }) {
  const { space, kind, id } = props
  const query = useQuery({ queryKey: ['private', space, kind, id, 'draft'], queryFn: ({ signal }) => request<Draft>(`/spaces/${space}/${kind}s/${id}/draft`, 'GET', undefined, signal) })
  if (query.error && !query.data) return <p role="alert">{query.error.message}</p>
  if (!query.data) return <p role="status">正在读取定义…</p>
  return <EditorForm {...props} initial={query.data} />
}
function EditorForm({ space, kind, id, initial, refresh, onCopied }: { space: string; kind: Kind; id: string; initial: Draft; refresh: () => void; onCopied: (id: string) => void }) {
  const base = `/spaces/${space}/${kind}s/${id}`
  const [draft, setDraft] = useState(initial), [doc, setDoc] = useState<Doc>(initial.document), [notice, setNotice] = useState('')
  const [conflict, setConflict] = useState<Draft | null>(null), [copyName, setCopyName] = useState(''), [versionID, setVersionID] = useState(''), [fromID, setFromID] = useState('')
  const uploadInput = useRef<HTMLInputElement>(null)
  const [file, setFile] = useState<File | null>(null), [path, setPath] = useState<string | null>(null), [preview, setPreview] = useState(false)
  const [builderDirty, setBuilderDirty] = useState(false)
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
  const changed = documentChanged || resourceEdits.size > 0 || builderDirty
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
    mutationFn: ({ directory, configJSON }: { directory?: string; configJSON?: string } = {}) => saveDocument(directory
      ? { ...draft.document, directories: [...(draft.document.directories ?? []), directory] }
      : { ...doc, config_yaml: configJSON ?? doc.config_yaml }),
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
    mutationFn: async ({ debug = false, configJSON }: { debug?: boolean; configJSON?: string } = {}) => {
      let revision = draft.revision
      if (kind === 'agent' && (documentChanged || (configJSON ?? doc.config_yaml) !== doc.config_yaml)) {
        const saved = await saveDocument({ ...doc, config_yaml: configJSON ?? doc.config_yaml })
        accept(saved)
        setNotice('草稿已保存')
        revision = saved.revision
      }
      return request<Version>(`${base}/${debug ? 'debug-snapshots' : 'versions'}`, 'POST', { expected_revision: revision })
    },
    onSuccess: v => { refresh(); setVersionID(v.id); setNotice(v.debug ? '调试快照已固定，可在会话与运行中选择它。' : `已发布 ${v.label}`) },
    onError: failed,
  })
  const mutateState = useMutation({ mutationFn: ({ suffix, body }: { suffix: string; body: unknown }) => request(`${base}/${suffix}`, 'PUT', body), onSuccess: async () => { accept(await request<Draft>(`${base}/draft`)); setNotice('状态已更新') } })
  const visibility = useMutation({
    mutationFn: (value: Schema['AgentVisibility']) => request<Schema['Definition']>(`${base}/visibility`, 'PUT', { visibility: value }),
    onSuccess: definition => {
      setDraft(current => ({ ...current, visibility: definition.visibility }))
      setNotice('权限范围已更新')
      refresh()
    },
  })
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
    {kind === 'agent' && <div className="inline-form">
      <label>权限范围<select value={draft.visibility ?? 'space'} disabled={pending || visibility.isPending}
        aria-describedby="agent-visibility-hint" onChange={event => visibility.mutate(event.target.value === 'public' ? 'public' : 'space')}>
        <option value="space">空间内</option><option value="public">公开</option>
      </select></label>
      <p id="agent-visibility-hint" className="muted">设为公开后，其他空间可使用已发布的默认版本。绑定的 Skill 内容可能通过 Agent 输出被其他空间的用户看到。</p>
      {visibility.error && <p role="alert">{visibility.error.message}</p>}
    </div>}
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
        onDirtyChange={setBuilderDirty}
        onAction={(action, configJSON) => action === 'draft' ? save.mutate({ configJSON }) : publish.mutate({ debug: action === 'debug', configJSON })} />
      <details className="config-builder"><summary>高级配置</summary>
        <Suspense fallback={<p>正在载入编辑器…</p>}><CodeEditor label="agent.json" language="json" value={doc.config_yaml}
          onChange={config_yaml => setDoc(d => ({ ...d, config_yaml }))} readOnly={pending || draft.archived} /></Suspense>
      </details>
    </>}
    {kind === 'skill' && [!save.variables && save.error, !upload.variables?.creating && upload.error].filter(Boolean).map((e, i) => <p role="alert" key={i}>{errorText(e)}</p>)}
    {conflict && <div className="error"><p role="alert">草稿已被其他编辑者修改。本地内容已保留，请查看差异后重新编辑。</p><Suspense fallback={<p>读取差异…</p>}><CodeEditor label="草稿冲突差异" language="yaml" original={JSON.stringify(conflict.document, null, 2)} value={JSON.stringify(doc, null, 2)} /></Suspense><button disabled={pending} onClick={() => { accept(conflict); setResourceEdits(new Map()); save.reset(); upload.reset() }}>载入服务器草稿</button></div>}
    {kind === 'skill' && <div className="actions"><button disabled={pending || changed || draft.archived} onClick={() => publish.mutate({})}>发布版本</button></div>}
    <h3>版本与差异</h3><div className="form-grid"><label>查看版本<select value={versionID} onChange={e => { setVersionID(e.target.value); setFromID('') }}><option value="">选择历史版本</option>{all.map(v => <option key={v.id} value={v.id}>{displayVersion(v)}{v.archived ? '（已归档）' : ''}{draft.default_version_id === v.id ? '（默认）' : ''}</option>)}</select></label><label>对比基准<select value={fromID} onChange={e => setFromID(e.target.value)}><option value="">不比较</option>{all.filter(v => v.id !== versionID).map(v => <option key={v.id} value={v.id}>{displayVersion(v)}</option>)}</select></label></div>
    {detail.data && <div className="version-detail"><h3>不可变版本：{displayVersion(detail.data)}</h3><p className="muted">SHA-256：{detail.data.sha256}</p><pre data-testid="version-markdown">{detail.data.bundle.document.markdown}</pre><pre data-testid="version-config">{detail.data.bundle.document.config_yaml}</pre>{detail.data.bundle.bindings.map(b => <p key={b.skill_id}>Skill {b.skill_id} → {b.version} · {b.version_id}</p>)}{detail.data.bundle.document.assets?.map(a => <p key={a.path}><a download href={`/api/v1${assetPayload(space, a)}`}>{a.path}</a></p>)}
      <div className="actions">{kind === 'agent' && !draft.archived && !detail.data.archived && <Link className="primary-link" to={agentSessionsPath(space, id) + '?version=' + encodeURIComponent(detail.data.id)}>使用此版本开始调试</Link>}{kind === 'agent' && !detail.data.debug && !detail.data.archived && <button disabled={pending || draft.archived} onClick={() => mutateState.mutate({ suffix: 'default-version', body: { version_id: versionID } })}>设为默认版本</button>}<button className="secondary" disabled={pending || kind === 'skill' && changed} onClick={() => mutateState.mutate({ suffix: `versions/${versionID}/archive`, body: { archived: !detail.data!.archived } })}>{detail.data.archived ? '恢复版本选择' : '归档此版本'}</button></div>
      {kind === 'skill' && <div><h4>引用此版本的 Agent</h4>{references.data?.map(r => <p key={r.id}>{r.name} · {r.debug ? '调试快照' : r.label}</p>)}{references.data?.length === 0 && <p className="muted">暂无引用</p>}</div>}
    </div>}
    {diff.data && <Suspense fallback={<p>载入版本差异…</p>}><CodeEditor key={`${fromID}-${versionID}`} label="版本差异" language="yaml" original={JSON.stringify(diff.data.from.bundle, null, 2)} value={JSON.stringify(diff.data.to.bundle, null, 2)} /></Suspense>}
    <div className="inline-form"><label>复制为新名称<input value={copyName} onChange={e => setCopyName(e.target.value)} /></label><button className="secondary" disabled={copy.isPending || changed || !copyName} onClick={() => copy.mutate()}>复制定义</button><button className="secondary" disabled={pending || changed} onClick={() => mutateState.mutate({ suffix: 'archive', body: { archived: !draft.archived } })}>{draft.archived ? '恢复定义' : '归档定义'}</button></div>
    {notice && <p role="status">{notice}</p>}{[kind === 'agent' && save.error, publish.error, mutateState.error, copy.error, versions.error, snapshots.error, detail.error, references.error, diff.error].filter(Boolean).map((e, i) => <p role="alert" key={i}>{errorText(e)}</p>)}
  </div>
}
function ConfigBuilder({ space, name, config, disabled, onDirtyChange, onAction }: {
  space: string; name: string; config: string; disabled: boolean
  onDirtyChange?: (dirty: boolean) => void
  onAction: (action: ConfigAction, config: string) => void
}) {
  const models = useQuery({ queryKey: ['private', space, 'catalog-models'], queryFn: ({ signal }) => list<Schema['Model']>(`/spaces/${space}/models?available=true`, signal) })
  const skills = useQuery({ queryKey: ['private', space, 'catalog-skills'], queryFn: async ({ signal }) => {
    const definitions = await list<Schema['Definition']>(`/spaces/${space}/skills`, signal)
    return Promise.all(definitions.filter(d => !d.archived).map(async d => ({ ...d, versions: (await list<Version>(`/spaces/${space}/skills/${d.id}/versions`, signal)).filter(v => !v.archived) })))
  } })
  type Selection = { model: string; skills: Record<string, string>; tools: string[]; input: number; limits: Record<Limit, string> }
  type Overrides = { model?: string; skills?: Record<string, string>; tools?: string[]; input?: number; limits?: Partial<Record<Limit, string>> }
  const [edits, setEdits] = useState<{ base: string; selection: Partial<Selection> }>()
  const existing = useMemo(() => parseConfig(config), [config])
  const previous = useMemo(() => parseConfig(edits?.base ?? ''), [edits?.base])
  // Apply pending selections only where the raw JSON did not change the same field afterwards.
  const overrides = useMemo<Overrides | undefined>(() => {
    if (!edits) return undefined
    const sel = edits.selection, before = previous, raw = existing
    const result: Overrides = {}
    if (sel.model !== undefined && (before?.model ?? '') === (raw?.model ?? '')) result.model = sel.model
    if (sel.skills) {
      const changed: Record<string, string> = {}
      for (const [id, version] of Object.entries(sel.skills)) {
        const old = before?.skills?.find(skill => skill.id === id)?.version ?? ''
        const now = raw?.skills?.find(skill => skill.id === id)?.version ?? ''
        if (old === now) changed[id] = version
      }
      if (Object.keys(changed).length > 0) result.skills = changed
    }
    if (sel.tools && JSON.stringify(before?.tools ?? []) === JSON.stringify(raw?.tools ?? [])) result.tools = sel.tools
    if (sel.input !== undefined && (before?.context?.max_input_tokens ?? 24000) === (raw?.context?.max_input_tokens ?? 24000)) result.input = sel.input
    if (sel.limits) {
      const changed: Partial<Record<Limit, string>> = {}
      for (const key of limitFields) {
        if (String(before?.limits?.[key] ?? agentLimits[key].initial) === String(raw?.limits?.[key] ?? agentLimits[key].initial)) changed[key] = sel.limits[key]
      }
      if (Object.keys(changed).length > 0) result.limits = changed
    }
    return result
  }, [edits, previous, existing])
  const { limits: editedLimits, ...configEdits } = overrides ?? {}
  const state: Selection = {
    model: existing?.model ?? '',
    skills: Object.fromEntries(Array.isArray(existing?.skills) ? existing.skills.filter(skill => skill?.id).map(skill => [skill.id, skill.version]) : []),
    tools: existing ? (Array.isArray(existing.tools) ? existing.tools.filter(tool => tools.includes(tool)) : []) : ['read_file', 'write_file'],
    input: existing?.context?.max_input_tokens ?? 24000,
    ...configEdits,
    limits: {
      max_steps: String(editedLimits?.max_steps ?? existing?.limits?.max_steps ?? agentLimits.max_steps.initial),
      timeout_seconds: String(editedLimits?.timeout_seconds ?? existing?.limits?.timeout_seconds ?? agentLimits.timeout_seconds.initial),
    },
  }
  const select = (change: Partial<Selection>) => setEdits(current => current
    ? { ...current, selection: { ...current.selection, ...change } }
    : { base: config, selection: { ...change } })
  const model = models.data?.find(m => m.id === state.model)
  const valid = !config.trim() || !!existing
  const limitsValid = limitFields.every(key => validLimit(key, state.limits[key]))
  const pending = !!overrides && Object.keys(overrides).length > 0
  const canSave = limitsValid && valid && (!pending || !!model)
  const canPublish = canSave && (pending || !!config.trim())
  const builderDisabled = disabled || !valid
  useEffect(() => { onDirtyChange?.(pending) }, [pending, onDirtyChange])
  const submit = (action: ConfigAction) => {
    if (!canSave) return
    if (!pending) {
      onAction(action, config)
      return
    }
    if (!model) return
    const output = Math.min(existing?.context?.max_tokens ?? 32000, model.max_output_tokens, Math.floor(model.context_window / 2))
    onAction(action, JSON.stringify({
      schema_version: 1,
      name,
      model: state.model,
      skills: Object.entries(state.skills).filter(([, version]) => version).map(([id, version]) => ({ id, version })),
      tools: state.tools,
      context: {
        max_input_tokens: Math.min(state.input, model.context_window - output),
        max_tokens: output,
        compression: existing?.context?.compression ?? 'summary',
      },
      limits: { max_steps: Number(state.limits.max_steps), timeout_seconds: Number(state.limits.timeout_seconds) },
    }, null, 2))
  }
  return <details className="config-builder" open>
    <summary>模型、Skill、工具与运行限制</summary>
    {!valid && <p className="muted" role="alert">Agent 配置必须是 JSON 对象，请在高级配置中修复。</p>}
    <div className="form-grid">
      <label>绑定模型<select disabled={builderDisabled} value={state.model} onChange={e => select({ model: e.target.value })}>
        <option value="">选择可用模型</option>{models.data?.map(m => <option value={m.id} key={m.id}>{m.name}</option>)}
      </select></label>
      <label>输入 Token 上限<input disabled={builderDisabled} type="number" min={1} max={2000000} value={state.input} onChange={e => select({ input: Number(e.target.value) })} /></label>
      {limitFields.map(key => {
        const { label, max } = agentLimits[key]
        const invalid = !validLimit(key, state.limits[key])
        return <label key={key}>{label}
          <input id={key} aria-describedby={`${key}-hint`} aria-invalid={invalid} disabled={builderDisabled}
            aria-label={label} required type="number" min={1} max={max} step={1} value={state.limits[key]}
            onChange={e => select({ limits: { ...state.limits, [key]: e.target.value } })} />
          <small id={`${key}-hint`} role={invalid ? 'alert' : undefined}>
            {invalid ? '请输入整数，范围 ' : ''}1–{max}{key === 'timeout_seconds' && !invalid && '，包含模型和工具执行时间'}
          </small>
        </label>
      })}
      {skills.data?.map(skill => <label key={skill.id}>Skill 版本：{skill.name}
        <select disabled={builderDisabled} aria-label={`Skill 版本：${skill.name}`} value={state.skills[skill.id] ?? ''} onChange={e => select({ skills: { ...state.skills, [skill.id]: e.target.value } })}>
          <option value="">不绑定</option>{skill.versions.map(v => <option value={v.label} key={v.id}>{v.label}</option>)}
        </select>
      </label>)}
    </div>
    <div className="tool-options">{tools.map(tool => <label className="checkbox" key={tool}>
      <input type="checkbox" disabled={builderDisabled} checked={state.tools.includes(tool)}
        onChange={e => select({ tools: e.target.checked ? [...state.tools, tool] : state.tools.filter(t => t !== tool) })} />{tool}
    </label>)}</div>
    {state.tools.includes('run_command') && <p className="muted">run_command 在 macOS 沙箱中执行 Shell 命令与 PATH 工具，可读写当前会话目录并联网。</p>}
    <div className="actions">
      <button type="button" disabled={disabled || !canPublish} onClick={() => submit('publish')}>发布版本</button>
      <button type="button" className="secondary" disabled={disabled || !canSave} onClick={() => submit('draft')}>保存草稿</button>
      <button type="button" className="secondary" disabled={disabled || !canPublish} onClick={() => submit('debug')}>创建调试快照</button>
    </div>
    {[models.error, skills.error].filter(Boolean).map((e, i) => <p role="alert" key={i}>{errorText(e)}</p>)}
  </details>
}
