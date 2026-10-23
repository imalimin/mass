import { useEffect, useState } from 'react'
import { APIError, request, type Schema } from '../../lib/api'
import { parseCursor } from '../../lib/agui'
import { reduceEvent, type UIState } from './stream'
export function useRunStream(space: string, session: string, run: string, generation: number) {
  const [value, setValue] = useState<{ state: UIState | null; cursor: string; error: string; connected: boolean }>({ state: null, cursor: '0', error: '', connected: false })
  useEffect(() => {
    if (!run) return
    const abort = new AbortController(); let closed = false, source: EventSource | null = null, timer: ReturnType<typeof setTimeout> | undefined, delay = 500
    let applied: UIState | null = null, cursor = 0n
    setValue({ state: null, cursor: '0', error: '', connected: false })
    const connect = async () => {
      try {
        const cp = await request<Schema['UICheckpoint']>(`/spaces/${space}/runs/${run}/ui-snapshot`, 'GET', undefined, abort.signal)
        if (closed) return
        if (cp.protocol_profile !== 'mass-agui-v1' || cp.mapper_version !== 1 || cp.parser_version !== 1 || cp.session_id !== session) throw new Error('不兼容的运行协议')
        applied = cp.state; cursor = parseCursor(cp.cursor); setValue({ state: applied, cursor: cp.cursor, error: '', connected: !cp.closed })
        if (cp.closed) return
        source = new EventSource(`/api/v1/spaces/${space}/runs/${run}/events?after=${cursor}`)
        source.onopen = () => { delay = 500 }
        source.onmessage = frame => {
          try {
            if (closed || !applied) return
            const next = parseCursor(frame.lastEventId); if (next <= cursor) return; if (next !== cursor + 1n) throw new Error('运行事件序号不连续，请重新同步')
            const state = reduceEvent(applied, JSON.parse(frame.data), session, run)
            applied = state; cursor = next // The cursor moves only after parsing and reducing succeeded.
            const terminal = ['succeeded', 'failed', 'cancelled'].includes(state.status)
            setValue({ state, cursor: next.toString(), error: '', connected: !terminal }); if (terminal) source?.close()
          } catch (e) { source?.close(); setValue(v => ({ ...v, connected: false, error: e instanceof Error ? e.message : '事件协议错误' })) }
        }
        source.onerror = () => { source?.close(); void recover() }
      } catch (e) {
        if (closed) return
        setValue(v => ({ ...v, connected: false, error: e instanceof Error ? e.message : '运行事件暂不可用' }))
        if (!(e instanceof APIError && [401, 403].includes(e.status))) { timer = setTimeout(() => { void recover() }, delay); delay = Math.min(delay * 2, 5000) }
      }
    }
    const recover = async () => {
      if (closed) return
      try { await request(`/spaces/${space}/runs/${run}`, 'GET', undefined, abort.signal); if (!closed) await connect() } catch (e) {
        if (closed) return
        setValue(v => ({ ...v, connected: false, error: e instanceof Error ? e.message : '连接中断' }))
        if (!(e instanceof APIError && [401, 403].includes(e.status))) { timer = setTimeout(() => { void recover() }, delay); delay = Math.min(delay * 2, 5000) }
      }
    }
    void connect()
    return () => { closed = true; abort.abort(); source?.close(); clearTimeout(timer) }
  }, [space, session, run, generation])
  return value
}
