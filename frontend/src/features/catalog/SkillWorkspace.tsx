import { lazy, Suspense, useState, type ReactNode } from 'react'
import { useQuery } from '@tanstack/react-query'
import { payloadText, type Schema } from '../../lib/api'

const CodeEditor = lazy(() => import('./CodeEditor'))
type Asset = Schema['DefinitionAsset']
type Folder = { folders: Map<string, Folder>; files: Asset[] }
const newFolder = (): Folder => ({ folders: new Map(), files: [] })
const languages: Record<string, string> = {
  md: 'markdown', yaml: 'yaml', yml: 'yaml', js: 'javascript', mjs: 'javascript', cjs: 'javascript',
  ts: 'typescript', py: 'python', json: 'json', sh: 'shell', bash: 'shell', html: 'html',
  css: 'css', sql: 'sql', go: 'go',
}

export const skillDocument = 'SKILL.md'
export const resourceParent = (path: string) => path.slice(0, Math.max(0, path.lastIndexOf('/')))
export function assetPayload(space: string, asset: Asset) {
  const path = `/spaces/${space}/payloads/${asset.object_id}`
  return asset.package ? `${path}?${new URLSearchParams({ path: asset.path })}` : path
}

export const maxResourceBytes = 8 * 1024 * 1024

type Props = {
  space: string
  markdown: string
  assets: Asset[]
  directories?: string[]
  activePath: string
  edits: ReadonlyMap<string, string>
  documentChanged: boolean
  pending: boolean
  archived: boolean
  previewControl: ReactNode
  preview: ReactNode
  uploadForm: ReactNode
  onSelect: (path: string) => void
  onCreateDirectory: (path: string) => Promise<unknown>
  onCreateFile: (path: string) => Promise<unknown>
  onMarkdownChange: (text: string) => void
  onResourceChange: (id: string, text: string | undefined) => void
  onSaveDocument: () => void
  onSaveResource: (asset: Asset, text: string) => void
  onRemove: (asset: Asset) => void
}

export function SkillWorkspace(props: Props) {
  const { space, markdown, assets, edits, documentChanged, pending, archived } = props
  const [closed, setClosed] = useState<string[]>([])
  const [creating, setCreating] = useState<'directory' | 'file' | null>(null)
  const [newPath, setNewPath] = useState('')
  const [createError, setCreateError] = useState('')
  const root = newFolder()
  const folders = new Set<string>()
  const ensureFolder = (path: string) => {
    if (!path) return root
    let folder = root
    let prefix = ''
    for (const segment of path.split('/')) {
      prefix = prefix ? `${prefix}/${segment}` : segment
      folders.add(prefix)
      if (!folder.folders.has(segment)) folder.folders.set(segment, newFolder())
      folder = folder.folders.get(segment)!
    }
    return folder
  }
  for (const path of props.directories ?? []) ensureFolder(path)
  for (const file of assets) ensureFolder(resourceParent(file.path)).files.push(file)

  const asset = assets.find(a => a.path === props.activePath)
  const directory = folders.has(props.activePath) ? props.activePath : undefined
  const activePath = asset?.path ?? directory ?? skillDocument
  const parent = directory ?? resourceParent(activePath)
  const payload = asset ? assetPayload(space, asset) : undefined
  const content = useQuery({
    queryKey: ['private', space, 'skill-file', payload],
    enabled: !!payload,
    queryFn: ({ signal }) => payloadText(payload!, signal, { maxBytes: maxResourceBytes, utf8Only: true }),
    retry: false,
    staleTime: Infinity,
  })

  const renderFile = (path: string, dirty: boolean) => <li key={path}>
    <button type="button" className="skill-file" aria-current={path === activePath ? 'true' : undefined} aria-label={path} title={path} onClick={() => props.onSelect(path)}>
      <span className="skill-file-icon" aria-hidden="true" />
      <span className="skill-file-name">{path.split('/').at(-1)}</span>
      {dirty && <span className="skill-file-dot" title="未保存" aria-hidden="true" />}
    </button>
  </li>
  const renderFolder = (folder: Folder, prefix = ''): ReactNode => <>
    {[...folder.folders].sort(([a], [b]) => a.localeCompare(b)).map(([name, child]) => {
      const path = prefix ? `${prefix}/${name}` : name
      const expanded = !closed.includes(path)
      return <li key={path}>
        <div className="skill-folder-row">
          <button type="button" className="skill-folder-toggle" aria-label={`${expanded ? '折叠' : '展开'} ${path}`} aria-expanded={expanded} onClick={() => {
            setClosed(previous => expanded ? [...previous, path] : previous.filter(p => p !== path))
          }}>{expanded ? '▾' : '▸'}</button>
          <button type="button" className="skill-file" aria-current={path === activePath ? 'true' : undefined} title={path} aria-label={`目录 ${path}`} onClick={() => props.onSelect(path)}>
            <span className="skill-folder-icon" aria-hidden="true" />
            <span className="skill-file-name">{name}</span>
          </button>
        </div>
        {expanded && <ul>{renderFolder(child, path)}</ul>}
      </li>
    })}
    {[...folder.files].sort((a, b) => a.path.localeCompare(b.path)).map(file => renderFile(file.path, edits.has(file.path)))}
  </>

  const openCreate = (kind: 'directory' | 'file') => {
    setCreating(kind)
    setNewPath(parent ? `${parent}/` : '')
    setCreateError('')
  }
  const create = async () => {
    const path = newPath.trim()
    if (!creating || !path) return
    if (/[\\:]/.test(path) || path.split('/').some(segment => !segment || segment === '.' || segment === '..')) {
      setCreateError('请输入相对于 Skill 根目录的路径。')
      return
    }
    if ([...folders, skillDocument, ...assets.map(file => file.path)].some(existing => existing.toLowerCase() === path.toLowerCase())) {
      setCreateError('此位置已有同名文件或目录。')
      return
    }
    setCreateError('')
    try {
      await (creating === 'file' ? props.onCreateFile(path) : props.onCreateDirectory(path))
      setClosed(previous => previous.filter(folder => !path.startsWith(`${folder}/`)))
      setCreating(null)
    } catch (error) {
      setCreateError(error instanceof Error ? error.message : '创建失败')
    }
  }

  const edit = asset ? edits.get(asset.path) : undefined
  const value = asset ? (content.isError ? undefined : edit ?? content.data) : directory ? undefined : markdown
  const changed = asset ? edit !== undefined : !directory && documentChanged
  const canSave = changed && !pending && !archived && value !== undefined
  const save = () => {
    if (!canSave) return
    if (asset && edit !== undefined) props.onSaveResource(asset, edit)
    else props.onSaveDocument()
  }
  const extension = activePath.split('.').at(-1)?.toLowerCase() ?? ''
  const language = Object.hasOwn(languages, extension) ? languages[extension] : 'plaintext'

  return <div className="skill-workspace" onKeyDownCapture={event => {
    if ((event.metaKey || event.ctrlKey) && event.key.toLowerCase() === 's') {
      event.preventDefault()
      save()
    }
  }}>
    <nav className="skill-files" aria-label="Skill 文件目录">
      <div className="skill-files-heading">
        <h4>文件目录</h4>
        {!archived && <div className="skill-editor-actions">
          <button type="button" className="secondary" disabled={pending || creating !== null} onClick={() => openCreate('file')}>新建文件</button>
          <button type="button" className="secondary" disabled={pending || creating !== null} onClick={() => openCreate('directory')}>新建目录</button>
        </div>}
      </div>
      {creating && <form className="skill-directory-form" onSubmit={event => {
        event.preventDefault()
        if (!pending) void create()
      }}>
        <label>{creating === 'file' ? '文件路径' : '目录路径'}<input autoFocus value={newPath} placeholder={creating === 'file' ? '例如 scripts/example.py' : '例如 scripts/python'} maxLength={512} onChange={event => setNewPath(event.target.value)} disabled={pending} required /></label>
        <div className="skill-editor-actions">
          <button disabled={pending || !newPath.trim()}>创建</button>
          <button type="button" className="secondary" disabled={pending} onClick={() => setCreating(null)}>取消</button>
        </div>
        {createError && <p role="alert">{createError}</p>}
      </form>}
      <ul className="skill-tree">
        {renderFile(skillDocument, documentChanged)}
        {renderFolder(root)}
      </ul>
      {!archived && <details className="skill-upload"><summary>添加资源</summary>{props.uploadForm}</details>}
    </nav>
    <section className="skill-edit-pane" aria-label="文件编辑">
      <header className="skill-editor-bar">
        <div className="skill-editor-title">
          <strong title={activePath}>{activePath}</strong>
          <span className="skill-file-status" data-dirty={changed} role="status">
            {pending ? '正在处理…' : archived ? '已归档 · 只读' : changed ? '未保存' : '已保存'}
          </span>
        </div>
        {!directory && <div className="skill-editor-actions">
          {!asset && props.previewControl}
          {asset && <a className="text-link" href={`/api/v1${payload}`} download={asset.path.split('/').at(-1)}>下载</a>}
          {asset && !archived && (changed
            ? <button type="button" className="secondary" disabled={pending} onClick={() => props.onResourceChange(asset.path, undefined)}>还原修改</button>
            : <button type="button" className="secondary" disabled={pending} onClick={() => props.onRemove(asset)}>移除</button>)}
          <button type="button" disabled={!canSave} onClick={save} title="Ctrl / ⌘ + S">{asset ? '保存文件' : '保存草稿'}</button>
        </div>}
      </header>
      {directory && <div className="skill-editor-empty"><p className="muted">选择目录内的文件进行编辑，或向此目录添加资源。</p></div>}
      {asset && content.isPending && <div className="skill-editor-empty" role="status">正在读取文件…</div>}
      {asset && content.isError && <div className="skill-editor-empty">
        <p role="alert">读取文件失败：{content.error.message}</p>
        <button type="button" className="secondary" onClick={() => { void content.refetch() }}>重新读取</button>
      </div>}
      {value !== undefined && <Suspense fallback={<div className="skill-editor-empty" role="status">正在载入编辑器…</div>}>
        <CodeEditor key={activePath} label={activePath} language={language} value={value} readOnly={archived || pending}
          onChange={asset
            ? text => props.onResourceChange(asset.path, text === content.data ? undefined : text)
            : props.onMarkdownChange}
        />
      </Suspense>}
      {!asset && !directory && props.preview}
    </section>
  </div>
}
