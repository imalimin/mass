import { APIError, request } from '../../lib/api'
import { parseCursor } from '../../lib/agui'
import { parseCheckpoint } from './checkpoint'
import { reduceEvent, type UIState } from './stream'
export type StreamValue = { state: UIState | null; cursor: string; error: string; connected: boolean }
type Source = Pick<EventSource, 'close' | 'onopen' | 'onmessage' | 'onerror'>
export type RunTransport = { get: (path: string, signal: AbortSignal) => Promise<unknown>; open: (url: string) => Source }
const transport: RunTransport = { get: (path, signal) => request(path, 'GET', undefined, signal), open: url => new EventSource(url) }
export class AguiRunClient {
  private abort = new AbortController()
  private source: Source | null = null
  private timer?: ReturnType<typeof setTimeout>
  private stopped = false
  private terminal = false
  private failures = 0
  private delay = 500
  private state: UIState | null = null
  private cursor = 0n
  private streamID?: string
  constructor(private space: string, private session: string, private run: string, private emit: (v: StreamValue) => void, private io = transport) {}
  private publish(error = '', connected = false) { if (!this.stopped) this.emit({ state: this.state, cursor: this.cursor.toString(), error, connected }) }
  async start() {
    try {
      const cp = parseCheckpoint(await this.io.get(`/spaces/${this.space}/runs/${this.run}/ui-snapshot`, this.abort.signal), this.space, this.session, this.run)
      if (this.stopped) return
      this.state = cp.state; this.cursor = parseCursor(cp.cursor); this.terminal = cp.closed; this.streamID = cp.stream_id
      this.publish(); if (!cp.closed) this.subscribe()
    } catch (e) { this.schedule(e, true) }
  }
  private subscribe() {
    if (this.stopped || this.terminal) return
    const source = this.io.open(`/api/v1/spaces/${this.space}/runs/${this.run}/events?after=${this.cursor}${this.streamID ? `&stream_id=${this.streamID}` : ''}`); this.source = source
    source.onopen = () => { if (this.source === source) this.publish('', true) }
    source.onmessage = frame => {
      if (this.stopped || this.source !== source || !this.state) return
      try {
        const next = parseCursor(frame.lastEventId); if (next <= this.cursor) return
        if (next !== this.cursor + 1n) throw new Error('运行事件序号不连续')
        const event: unknown = JSON.parse(frame.data), state = reduceEvent(this.state, event, this.session, this.run, this.space)
        this.state = state; this.cursor = next; this.failures = 0; this.delay = 500
        // A CUSTOM status precedes the final standard lifecycle event. Receive
        // that event before closing, even when status already says succeeded.
        const type = (event as { type: string }).type
        this.terminal = type === 'RUN_FINISHED' || type === 'RUN_ERROR'
        this.publish('', !this.terminal)
        if (this.terminal) { source.close(); this.source = null }
      } catch (e) { this.disconnect(); this.schedule(e) }
    }
    source.onerror = () => { if (this.source !== source || this.stopped) return; this.disconnect(); this.schedule(new Error('连接中断，正在恢复')) }
  }
  private disconnect() { this.source?.close(); this.source = null }
  private schedule(e: unknown, initial = false) {
    if (this.stopped || this.terminal) return
    clearTimeout(this.timer); this.disconnect()
    if (e instanceof APIError && [401, 403].includes(e.status)) { this.state = null; this.publish(e.message); return }
    this.failures++; this.publish(e instanceof Error ? e.message : '事件连接失败')
    if (this.failures > 5) { this.publish('自动重连已停止，请重新同步事件'); return }
    this.timer = setTimeout(() => { void this.recover(initial) }, this.delay); this.delay = Math.min(this.delay * 2, 5000)
  }
  private async recover(initial: boolean) {
    try {
      if (initial || !this.state || this.streamID) {
        await this.start()
        return
      }
      await this.io.get(`/spaces/${this.space}/runs/${this.run}`, this.abort.signal)
      if (!this.stopped) this.subscribe()
    } catch (e) { this.schedule(e, initial) }
  }
  stop() { this.stopped = true; this.abort.abort(); clearTimeout(this.timer); this.disconnect() }
}
