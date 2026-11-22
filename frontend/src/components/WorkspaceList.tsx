import type { ReactNode } from 'react'
import { Link } from 'react-router-dom'
import { spacePath } from '../app/routes'

export function WorkspaceListToolbar({ space, name, children }: {
  space: { id: string; name: string }; name: string; children: ReactNode
}) {
  return <header className="workspace-list-toolbar">
    <nav className="breadcrumbs" aria-label="面包屑">
      <Link to={spacePath(space.id)} title={space.name}>{space.name}</Link><span aria-hidden="true">/</span>
      <span aria-current="page">{name}</span>
    </nav>
    <div className="workspace-list-actions">{children}</div>
  </header>
}

export function WorkspaceListTable({ label, columns, columnGroup, className, children }: {
  label: string; columns: string[]; columnGroup?: ReactNode; className?: string; children: ReactNode
}) {
  return <div className="workspace-list-scroll">
    <table className={className ? `workspace-list-table ${className}` : 'workspace-list-table'} aria-label={label}>
      {columnGroup}
      <thead><tr>{columns.map(column => <th scope="col" key={column}>{column}</th>)}</tr></thead>
      <tbody>{children}</tbody>
    </table>
  </div>
}
