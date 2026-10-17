import { EventSchema } from '@ag-ui/core/schemas'
import Ajv2020 from 'ajv/dist/2020'
import profile from '../../../api/events/agui-profile.json'
import customSchema from '../../../api/events/custom.schema.json'

const validateCustom = new Ajv2020({ strict: true }).compile(customSchema)

export function parseCursor(value: string): bigint {
  if (!/^(0|[1-9][0-9]*)$/.test(value) || value.length > 19) throw new Error('invalid_cursor')
  const cursor = BigInt(value)
  if (cursor > 9223372036854775807n) throw new Error('invalid_cursor')
  return cursor
}

// D00 validates wire contracts. D08 adds the persisted-state reducer and
// controlled reconnect; parsing a frame alone never advances a live cursor.
export function parseEvent(input: unknown) {
  const event = EventSchema.parse(input)
  if (!profile.standardEvents.includes(event.type)) throw new Error('unsupported_event_schema')
  if ((event.type === 'TEXT_MESSAGE_CONTENT' || event.type === 'TOOL_CALL_ARGS') && event.delta.length === 0) throw new Error('empty_event_delta')
  if (event.type === 'CUSTOM' && !validateCustom(event)) throw new Error('unsupported_event_schema')
  return event
}
