import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { APIError } from '../../lib/api'
import { AguiRunClient, type RunTransport, type StreamValue } from './AguiRunClient'
const empty = () => ({ status: 'running', cancel_requested: false, messages: [], tools: [], artifacts: [], error: '', usage: { input_tokens: null, output_tokens: null, cache_read_tokens: null, cache_write_tokens: null } })
class Source {
  closed = false
  onopen: EventSource['onopen'] = null; onmessage: EventSource['onmessage'] = null; onerror: EventSource['onerror'] = null
  close() { this.closed = true }
  frame(id: string, data: unknown) { this.onmessage?.call(this as unknown as EventSource, { lastEventId: id, data: JSON.stringify(data) } as MessageEvent) }
  fail() { this.onerror?.call(this as unknown as EventSource, new Event('error')) }
}
function fixture() {
  const sources: Source[] = [], urls: string[] = [], values: StreamValue[] = []
  const get = vi.fn<RunTransport['get']>(async () => ({ session_id: 's', cursor: '0', source_sequence: '7', protocol_profile: 'mass-agui-v1', mapper_version: 1, parser_version: 1, closed: false, state: empty() }))
  const io: RunTransport = { get, open: url => { urls.push(url); const s = new Source(); sources.push(s); return s } }
  const client = new AguiRunClient('space', 's', 'r', v => values.push(v), io)
  return { client, sources, values, get, urls }
}
beforeEach(() => vi.useFakeTimers()); afterEach(() => vi.useRealTimers())
describe('MASS submission and controlled AG-UI subscription', () => {
  it('resumes from the applied UI cursor, deduplicates and waits for the standard terminal event', async () => {
    const f = fixture(); await f.client.start()
    f.sources[0].frame('1', { type: 'TEXT_MESSAGE_START', messageId: 'm', role: 'assistant' })
    f.sources[0].fail(); await vi.advanceTimersByTimeAsync(500)
    expect(f.urls[1]).toContain('after=1'); expect(f.get).toHaveBeenCalledTimes(2)
    f.sources[1].frame('1', { type: 'TEXT_MESSAGE_START', messageId: 'm', role: 'assistant' })
    f.sources[1].frame('2', { type: 'TEXT_MESSAGE_CONTENT', messageId: 'm', delta: 'one' })
    f.sources[1].frame('3', { type: 'TEXT_MESSAGE_END', messageId: 'm' })
    f.sources[1].frame('4', { type: 'CUSTOM', name: 'mass.run.status.v1', value: { schemaVersion: 1, threadId: 's', runId: 'r', sourceEventIds: ['e'], status: 'succeeded' } })
    expect(f.sources[1].closed).toBe(false)
    f.sources[1].frame('5', { type: 'RUN_FINISHED', threadId: 's', runId: 'r' })
    expect(f.sources[1].closed).toBe(true); expect(f.values.at(-1)?.cursor).toBe('5'); expect(f.values.at(-1)?.state?.messages[0].text).toBe('one')
    f.client.stop()
  })
  it('reloads the checkpoint when the in-memory stream changes', async () => {
    const f = fixture()
    const first = '01000000-0000-4000-8000-000000000001'
    const restarted = '01000000-0000-4000-8000-000000000002'
    const checkpoint = { session_id: 's', source_sequence: '7', protocol_profile: 'mass-agui-v1', mapper_version: 1, parser_version: 1, closed: false, state: empty() }
    f.get.mockResolvedValueOnce({ ...checkpoint, stream_id: first, cursor: '5' })
    f.get.mockResolvedValueOnce({ ...checkpoint, stream_id: restarted, cursor: '2' })
    await f.client.start()
    expect(f.urls[0]).toContain(`after=5&stream_id=${first}`)
    f.sources[0].fail()
    await vi.advanceTimersByTimeAsync(500)
    expect(f.urls[1]).toContain(`after=2&stream_id=${restarted}`)
    expect(f.values.at(-1)?.cursor).toBe('2')
    f.client.stop()
  })
  it('does not acknowledge a gap or invalid reducer frame; stale callbacks cannot change a new connection', async () => {
    const f = fixture(); await f.client.start()
    f.sources[0].frame('2', { type: 'TEXT_MESSAGE_START', messageId: 'm', role: 'assistant' })
    expect(f.values.at(-1)?.cursor).toBe('0'); await vi.advanceTimersByTimeAsync(500)
    expect(f.urls[1]).toContain('after=0')
    f.sources[0].frame('1', { type: 'TEXT_MESSAGE_START', messageId: 'stale' })
    expect(f.values.at(-1)?.state?.messages).toEqual([])
    f.sources[1].frame('1', { type: 'TEXT_MESSAGE_CONTENT', messageId: 'missing', delta: 'bad' })
    expect(f.values.at(-1)?.cursor).toBe('0'); f.client.stop(); await vi.advanceTimersByTimeAsync(10000); expect(f.sources.length).toBe(2)
  })
  it('clears private state and stops reconnecting on revoked authorization', async () => {
    const f = fixture(); await f.client.start(); f.get.mockRejectedValueOnce(new APIError(403, 'forbidden'))
    f.sources[0].fail(); await vi.advanceTimersByTimeAsync(10000)
    expect(f.values.at(-1)?.state).toBe(null); expect(f.sources.length).toBe(1); f.client.stop()
  })
  it('validates the snapshot before trusting its cursor and stops terminal snapshots without opening SSE', async () => {
    const f = fixture(); f.get.mockResolvedValueOnce({ session_id: 'other' } as never); await f.client.start()
    expect(f.sources.length).toBe(0); expect(f.values.at(-1)?.cursor).toBe('0'); f.client.stop()
    const done = fixture(); done.get.mockResolvedValueOnce({ session_id: 's', cursor: '50', source_sequence: '101', protocol_profile: 'mass-agui-v1', mapper_version: 1, parser_version: 1, closed: true, state: { ...empty(), status: 'cancelled' } })
    await done.client.start(); expect(done.sources.length).toBe(0); expect(done.values.at(-1)?.cursor).toBe('50'); done.client.stop()
  })
})
