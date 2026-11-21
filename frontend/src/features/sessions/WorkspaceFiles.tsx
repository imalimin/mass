import { useState, type ReactNode } from 'react'
import { useQuery } from '@tanstack/react-query'
import { APIError, request, type Schema } from '../../lib/api'

type WorkspaceScope = { space: string; sessionID: string }

function useEntry({ space, sessionID }: WorkspaceScope, path: string) {
  return useQuery({
    queryKey: ['private', space, sessionID, 'workspace-files', path],
    queryFn: ({ signal }) => request<Schema['WorkspaceEntryView']>(
      `/spaces/${space}/sessions/${sessionID}/workspace/files?${new URLSearchParams({ path })}`, 'GET', undefined, signal),
    refetchOnWindowFocus: false,
    retry: false,
  })
}

function WorkspaceDirectory({ scope, path, expanded, toggle, selected, select }: {
  scope: WorkspaceScope; path: string; expanded: Set<string>; toggle: (path: string) => void
  selected: string; select: (path: string) => void
}) {
  const query = useEntry(scope, path)
  if (query.isError) return <p role="alert">{query.error.message}</p>
  if (query.isPending) return <p role="status">正在读取…</p>
  if (!query.data.directory || !query.data.entries.length) return <p className="muted">暂无文件</p>
  return <ul>{query.data.entries.map(entry => {
    const childPath = path === '.' ? entry.name : `${path}/${entry.name}`
    const open = expanded.has(childPath)
    return <li key={childPath}>
      <button type="button" title={entry.name} aria-expanded={entry.directory ? open : undefined}
        aria-current={selected === childPath ? 'true' : undefined}
        onClick={() => entry.directory ? toggle(childPath) : select(childPath)}>
        <span aria-hidden="true">{entry.directory ? open ? '▾ ' : '▸ ' : '· '}</span>{entry.name}
      </button>
      {entry.directory && open && <WorkspaceDirectory scope={scope} path={childPath} expanded={expanded}
        toggle={toggle} selected={selected} select={select} />}
    </li>
  })}</ul>
}

function WorkspacePreview({ scope, path, close }: { scope: WorkspaceScope; path: string; close: () => void }) {
  const query = useEntry(scope, path)
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
    body = <pre>{query.data.text || '（空文件）'}</pre>
  }
  return <section className="workspace-preview" aria-label="只读文件预览">
    <header><strong title={path}>{path}</strong><button type="button" className="secondary" onClick={close}>关闭预览</button></header>
    <div className="workspace-pane-body">{body}</div>
  </section>
}

export function WorkspaceFiles({ space, sessionID, children }: WorkspaceScope & { children: ReactNode }) {
  return <div className="session-file-layout">
    {children}
    <WorkspacePanes key={`${space}/${sessionID}`} scope={{ space, sessionID }} />
  </div>
}

function WorkspacePanes({ scope }: { scope: WorkspaceScope }) {
  const [selected, select] = useState('')
  const [expanded, setExpanded] = useState(() => new Set<string>())
  const toggle = (path: string) => setExpanded(previous => {
    const next = new Set(previous)
    if (!next.delete(path)) next.add(path)
    return next
  })
  return <>
    {selected && <WorkspacePreview scope={scope} path={selected} close={() => select('')} />}
    <aside className="workspace-tree" aria-label="工作目录">
      <header><strong>工作目录</strong></header>
      <div className="workspace-pane-body">
        {scope.sessionID ? <WorkspaceDirectory scope={scope} path="." expanded={expanded} toggle={toggle}
          selected={selected} select={select} /> : <p className="muted">会话开始后可查看文件。</p>}
      </div>
    </aside>
  </>
}
