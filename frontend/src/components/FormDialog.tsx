import { useEffect, useId, useRef, type ReactNode } from 'react'

export function FormDialog({ title, pending, close, className, children }: {
  title: string; pending: boolean; close: () => void; className?: string; children: (dismiss: () => void) => ReactNode
}) {
  const dialog = useRef<HTMLDialogElement>(null)
  const headingID = useId()
  useEffect(() => {
    const node = dialog.current
    node?.showModal()
    return () => node?.close()
  }, [])
  const dismiss = () => {
    if (pending) return
    dialog.current?.close()
    close()
  }
  return <dialog className={className ? `workspace-form-dialog ${className}` : 'workspace-form-dialog'} ref={dialog}
    aria-labelledby={headingID} onCancel={event => { event.preventDefault(); dismiss() }}>
    <h2 id={headingID}>{title}</h2>
    {children(dismiss)}
  </dialog>
}
