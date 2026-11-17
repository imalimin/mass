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
  it('keeps tool parent messages in order even for steps without text', () => {
    let state = empty()
    const events = [
      { type: 'TEXT_MESSAGE_START', messageId: 'm1', role: 'assistant' },
      { type: 'TEXT_MESSAGE_CONTENT', messageId: 'm1', delta: 'Inspect workspace' },
      { type: 'TEXT_MESSAGE_END', messageId: 'm1' },
      { type: 'TOOL_CALL_START', toolCallId: 'normal', toolCallName: 'list_files', parentMessageId: 'm1' },
      { type: 'CUSTOM', name: 'mass.tool.status.v1', value: {
        schemaVersion: 1, threadId: 's', runId: 'r', sourceEventIds: ['e'],
        toolCallId: 'large', toolName: 'write_file', parentMessageId: 'm2', status: 'parameters_complete',
      } },
      { type: 'TEXT_MESSAGE_START', messageId: 'm3', role: 'assistant' },
    ]
    for (const event of events) state = reduceEvent(state, event, 's', 'r')
    expect(state.messages.map(m => m.id)).toEqual(['m1', 'm2', 'm3'])
    expect(state.messages[1]).toMatchObject({ text: '', closed: true })
    expect(state.tools.map(t => [t.id, t.parent_message_id])).toEqual([['normal', 'm1'], ['large', 'm2']])
  })
  it('restores plan progress independently from the run outcome', () => {
    const plan = { revision: 2, steps: [{ id: 'report', title: 'Create summary', status: 'completed' }] }
    const event = { type: 'CUSTOM', name: 'mass.plan.v1', value: { schemaVersion: 1, threadId: 's', runId: 'r', sourceEventIds: ['e'], plan } }
    const before = empty(), after = reduceEvent(before, event, 's', 'r')
    expect(after.plan).toEqual(plan); expect(after.status).toBe('queued'); expect(before.plan).toBeUndefined()
    expect(() => reduceEvent(before, { ...event, value: { ...event.value, runId: 'other' } }, 's', 'r')).toThrow('stream_scope_mismatch')
    expect(() => reduceEvent(before, { ...event, value: { ...event.value, plan: { ...plan, steps: [{ id: 'x', title: 'x', status: 'pretended' }] } } }, 's', 'r')).toThrow('invalid_plan')
  })
})
