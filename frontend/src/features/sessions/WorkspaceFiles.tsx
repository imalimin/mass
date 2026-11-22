import { lazy, Suspense, useEffect, useId, useRef, useState, type ReactNode } from 'react'
import { useQuery } from '@tanstack/react-query'
import { Icon } from '../../components/Icon'
import { APIError, request, type Schema } from '../../lib/api'

type WorkspaceScope = { space: string; sessionID: string }

const SafeMarkdown = lazy(() => import('../debug/SafeMarkdown'))

function useEntry({ space, sessionID }: WorkspaceScope, path: string, enabled: boolean) {
  return useQuery({
    queryKey: ['private', space, sessionID, 'workspace-files', path],
    queryFn: ({ signal }) => request<Schema['WorkspaceEntryView']>(
      `/spaces/${space}/sessions/${sessionID}/workspace/files?${new URLSearchParams({ path })}`, 'GET', undefined, signal),
    enabled,
    refetchOnWindowFocus: false,
    retry: false,
  })
}

function WorkspaceDirectory({ scope, path, visible, expanded, toggle, selected, select }: {
  scope: WorkspaceScope; path: string; visible: boolean; expanded: Set<string>; toggle: (path: string) => void
  selected: string; select: (path: string, trigger: HTMLButtonElement) => void
}) {
  const query = useEntry(scope, path, visible)
  if (query.isError) return <p role="alert">{query.error.message}</p>
  if (query.isPending) return <p role="status">正在读取…</p>
  if (!query.data.directory || !query.data.entries.length) return <p className="muted">暂无文件</p>
  return <ul>{query.data.entries.map(entry => {
    const childPath = path === '.' ? entry.name : `${path}/${entry.name}`
    const open = expanded.has(childPath)
    return <li key={childPath}>
      <button type="button" title={entry.name} aria-expanded={entry.directory ? open : undefined}
        aria-current={selected === childPath ? 'true' : undefined}
        onClick={event => entry.directory ? toggle(childPath) : select(childPath, event.currentTarget)}>
        {entry.directory && <Icon name="chevron" size={13} className="directory-chevron" />}
        <Icon name={entry.directory ? 'folder' : 'file'} size={16} />
        <span>{entry.name}</span>
      </button>
      {entry.directory && open && <WorkspaceDirectory scope={scope} path={childPath} visible={visible} expanded={expanded}
        toggle={toggle} selected={selected} select={select} />}
    </li>
  })}</ul>
}

function WorkspacePreview({ scope, path, hidden, close }: { scope: WorkspaceScope; path: string; hidden: boolean; close: () => void }) {
  const query = useEntry(scope, path, !hidden)
  const [showSource, setShowSource] = useState(false)
  const closeButton = useRef<HTMLButtonElement>(null)
  useEffect(() => { closeButton.current?.focus({ preventScroll: true }) }, [])
  let markdown = false
  let body: ReactNode
  if (query.isError) {
    body = <p role="alert">{query.error instanceof APIError && query.error.status === 404 ? '此文件已不存在。' : query.error.message}</p>
  } else if (query.isPending) {
    body = <p role="status">正在读取文件…</p>
  } else if (query.data.directory) {
    body = <p role="status">该路径已变为目录。</p>
  } else if (query.data.status) {
    body = <p role="status">{query.data.status === 'too_large' ? '文件超过预览大小限制。' : '仅支持预览 UTF-8 文本。'}</p>
  } else {
    markdown = /\.md$/i.test(path)
    const text = query.data.text
    body = markdown && !showSource && text
      ? <Suspense fallback={<p role="status">正在渲染 Markdown…</p>}><SafeMarkdown text={text} showSource={false} /></Suspense>
      : <pre>{text || '（空文件）'}</pre>
  }
  return <section className="workspace-preview" aria-label="只读文件预览" hidden={hidden} onKeyDown={event => {
    if (event.key === 'Escape') { event.stopPropagation(); close() }
  }}>
    <header>
      <strong title={path}><Icon name="file" />{path}</strong>
      <div className="workspace-preview-actions">
        {markdown && <button type="button" className="secondary" onClick={() => setShowSource(value => !value)}>
          <Icon name={showSource ? 'file' : 'code'} />{showSource ? '渲染展示' : '查看原文'}
        </button>}
        <button ref={closeButton} type="button" className="icon-button" aria-label="关闭预览" title="关闭预览" onClick={close}>
          <Icon name="close" />
        </button>
      </div>
    </header>
    <div className="workspace-pane-body">{body}</div>
  </section>
}

const compactWorkspace = '(max-width: 760px)'

export function WorkspaceFiles({ space, sessionID, children }: WorkspaceScope & { children: (workspaceToggle: ReactNode) => ReactNode }) {
  const treeID = useId()
  const [compact, setCompact] = useState(() => window.matchMedia(compactWorkspace).matches)
  const [visibility, setVisibility] = useState<boolean>()
  const open = visibility ?? !compact
  const label = open ? '收起工作目录' : '展开工作目录'
  useEffect(() => {
    const media = window.matchMedia(compactWorkspace)
    const changed = (event: MediaQueryListEvent) => setCompact(event.matches)
    media.addEventListener('change', changed)
    return () => media.removeEventListener('change', changed)
  }, [])
  return <div className="session-file-layout">
    {children(<button type="button" className="icon-button workspace-toggle" aria-label={label} title={label}
      aria-controls={treeID} aria-expanded={open} onClick={() => setVisibility(!open)}><Icon name="panel" size={20} /></button>)}
    <WorkspacePanes key={`${space}/${sessionID}`} scope={{ space, sessionID }} id={treeID} open={open} />
  </div>
}

function WorkspacePanes({ scope, id, open }: { scope: WorkspaceScope; id: string; open: boolean }) {
  const [selected, select] = useState('')
  const [expanded, setExpanded] = useState(() => new Set<string>())
  const selectedTrigger = useRef<HTMLButtonElement>(null)
  const toggle = (path: string) => setExpanded(previous => {
    const next = new Set(previous)
    if (!next.delete(path)) next.add(path)
    return next
  })
  return <>
    {selected && <WorkspacePreview key={selected} scope={scope} path={selected} hidden={!open} close={() => {
      select('')
      requestAnimationFrame(() => selectedTrigger.current?.focus({ preventScroll: true }))
    }} />}
    <aside className="workspace-tree" id={id} aria-label="工作目录" hidden={!open}>
      <header><strong><Icon name="folder" size={20} />工作目录</strong></header>
      <div className="workspace-pane-body">
        {scope.sessionID ? <WorkspaceDirectory scope={scope} path="." visible={open} expanded={expanded} toggle={toggle}
          selected={selected} select={(path, trigger) => { selectedTrigger.current = trigger; select(path) }} /> : <p className="muted">会话开始后可查看文件。</p>}
      </div>
    </aside>
  </>
}
