import { readFileSync } from 'node:fs'
import { describe, expect, it } from 'vitest'
import { parseCursor, parseEvent } from './agui'
import fixtures from '../../../api/events/fixtures/go-events.json'
import profile from '../../../api/events/agui-profile.json'

describe('Go SDK → TypeScript AG-UI wire compatibility', () => {
  it('validates the complete selected standard/custom subset', () => {
    const parsed = fixtures.map(parseEvent)
    expect(new Set(parsed.map(event => event.type))).toEqual(new Set(profile.standardEvents))
    expect(parsed.filter(event => event.type === 'CUSTOM').map(event => event.name)).toEqual(profile.customNames)
    const toolEnd = parsed.findIndex(event => event.type === 'TOOL_CALL_END')
    expect(parsed[toolEnd + 1].type).toBe('TOOL_CALL_RESULT')
    expect(parsed.find(event => event.type === 'RUN_ERROR')).toMatchObject({ code: 'RUN_CANCELLED' })
  })
  it('decodes actual Go SSE frames without a proprietary wrapper or numeric coercion', () => {
    const frames = readFileSync(new URL('../../../api/events/fixtures/go-events.sse', import.meta.url), 'utf8').trim().split('\n\n')
    expect(frames).toHaveLength(fixtures.length)
    for (const [index, frame] of frames.entries()) {
      const [id, data] = frame.split('\n')
      expect(parseCursor(id.slice(4))).toBe(BigInt(index + 1))
      expect(parseEvent(JSON.parse(data.slice(6)))).toEqual(parseEvent(fixtures[index]))
    }
  })
  it('rejects missing fields, unsupported events and unversioned custom payloads', () => {
    for (const value of [{ type: 'RUN_STARTED', runId: 'r' }, { type: 'TEXT_MESSAGE_CONTENT', messageId: 'm', delta: '' }, { type: 'CUSTOM', name: 'mass.plan.v1', value: {} }, { type: 'STATE_SNAPSHOT', snapshot: {} }, { event_type: 'run.created', payload: {} }]) expect(() => parseEvent(value)).toThrow()
  })
  it('preserves bigint precision and rejects noncanonical cursors', () => {
    expect(parseCursor('9223372036854775807')).toBe(9223372036854775807n)
    for (const value of ['-1', '+1', '01', '1.0', '1e3', ' 1', '9223372036854775808']) expect(() => parseCursor(value)).toThrow()
  })
})
