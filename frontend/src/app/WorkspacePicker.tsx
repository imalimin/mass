import { useEffect, useId, useRef, useState, type KeyboardEvent } from 'react'
import { Link, useLocation } from 'react-router-dom'
import { Icon } from '../components/Icon'
import type { Schema } from '../lib/api'
import { spacePath } from './routes'

type Space = Schema['Space']

export function WorkspacePicker({ spaces, current, pending }: { spaces?: Space[]; current?: Space; pending: boolean }) {
  const [open, setOpen] = useState(false)
  const root = useRef<HTMLElement>(null)
  const trigger = useRef<HTMLButtonElement>(null)
  const menu = useRef<HTMLUListElement>(null)
  const menuID = useId()
  const { pathname } = useLocation()
  const selected = current ?? spaces?.[0]
  const label = selected?.name ?? (pending ? '正在读取空间…' : '暂无空间')

  useEffect(() => { setOpen(false) }, [pathname])
  useEffect(() => {
    if (!open) return
    menu.current?.querySelector<HTMLAnchorElement>('a[aria-current]')?.focus()
    const dismissOutside = (event: PointerEvent) => {
      if (!root.current?.contains(event.target as Node)) setOpen(false)
    }
    document.addEventListener('pointerdown', dismissOutside)
    return () => document.removeEventListener('pointerdown', dismissOutside)
  }, [open])

  const close = () => {
    setOpen(false)
    trigger.current?.focus()
  }
  const onKeyDown = (event: KeyboardEvent<HTMLElement>) => {
    if (open && event.key === 'Escape') {
      event.preventDefault()
      event.stopPropagation()
      close()
      return
    }
    if (!spaces?.length || !['ArrowDown', 'ArrowUp', 'Home', 'End'].includes(event.key)) return
    event.preventDefault()
    if (!open) {
      setOpen(true)
      return
    }
    const links = Array.from(menu.current?.querySelectorAll('a') ?? [])
    const index = links.findIndex(link => link === document.activeElement)
    switch (event.key) {
      case 'Home': links[0]?.focus(); break
      case 'End': links.at(-1)?.focus(); break
      case 'ArrowDown': links[Math.min(index + 1, links.length - 1)]?.focus(); break
      case 'ArrowUp': links[Math.max(index - 1, 0)]?.focus(); break
    }
  }

  return <nav className="workspace-picker" aria-label="主导航" ref={root} onKeyDown={onKeyDown}
    onBlur={event => { if (event.relatedTarget && !event.currentTarget.contains(event.relatedTarget)) setOpen(false) }}>
    <button className="workspace-picker-trigger" ref={trigger} type="button" aria-label={`切换空间：${label}`}
      aria-expanded={open} aria-controls={menuID} title={label} disabled={!spaces?.length} onClick={() => setOpen(value => !value)}>
      <Icon name="layers" />
      <span>{label}</span>
      <Icon name="chevron" size={15} className="workspace-picker-chevron" />
    </button>
    <ul className="workspace-picker-menu" id={menuID} aria-label="可用空间" ref={menu} hidden={!open}>
      {spaces?.map(({ id, name }) => <li key={id}>
        <Link to={spacePath(id)} aria-current={id === selected?.id ? 'true' : undefined} title={name} onClick={close}>
          <span>{name}</span>
          {id === selected?.id && <Icon name="check" size={16} />}
        </Link>
      </li>)}
    </ul>
  </nav>
}
