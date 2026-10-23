import { describe, expect, it } from 'vitest'
import { reduceEvent, type UIState } from './stream'
const empty = (): UIState => ({ status: 'queued', cancel_requested: false, messages: [], tools: [], artifacts: [], error: '', usage: { input_tokens: null, output_tokens: null, cache_read_tokens: null, cache_write_tokens: null } })
describe('persisted AG-UI state application', () => {
  it('separates parameter completion from tool execution and handles cancellation', () => {
    let s = empty()
    for (const e of [{ type: 'RUN_STARTED', threadId: 's', runId: 'r' }, { type: 'TOOL_CALL_START', toolCallId: 'c', toolCallName: 'read_file' }, { type: 'TOOL_CALL_ARGS', toolCallId: 'c', delta: '{"path":"a"}' }, { type: 'TOOL_CALL_END', toolCallId: 'c' }]) s = reduceEvent(s, e, 's', 'r')
    expect(s.tools[0].status).toBe('parameters_complete'); expect(s.tools[0].result).toBe('')
    s = reduceEvent(s, { type: 'TOOL_CALL_RESULT', messageId: 'm', toolCallId: 'c', content: '{"status":"completed"}', role: 'tool' }, 's', 'r')
    expect(s.tools[0].status).toBe('completed')
    s = reduceEvent(s, { type: 'RUN_ERROR', message: 'cancelled', code: 'RUN_CANCELLED' }, 's', 'r')
    expect(s.status).toBe('cancelled'); expect(s.error).toBe('run_cancelled')
  })
  it('retains incomplete text without treating it as a successful response', () => {
    let s = empty(); s = reduceEvent(s, { type: 'TEXT_MESSAGE_START', messageId: 'm', role: 'assistant' }, 's', 'r')
    s = reduceEvent(s, { type: 'TEXT_MESSAGE_CONTENT', messageId: 'm', delta: 'partial' }, 's', 'r')
    s = reduceEvent(s, { type: 'CUSTOM', name: 'mass.message.status.v1', value: { schemaVersion: 1, threadId: 's', runId: 'r', sourceEventIds: ['e'], messageId: 'm', status: 'incomplete' } }, 's', 'r')
    s = reduceEvent(s, { type: 'TEXT_MESSAGE_END', messageId: 'm' }, 's', 'r'); expect(s.messages[0]).toMatchObject({ text: 'partial', incomplete: true, closed: true })
    expect(() => reduceEvent(s, { type: 'RUN_FINISHED', threadId: 'other', runId: 'r' }, 's', 'r')).toThrow()
    expect(() => reduceEvent(s, { type: 'TEXT_MESSAGE_CONTENT', messageId: 'm', delta: 'late' }, 's', 'r')).toThrow()
  })
})
