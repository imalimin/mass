import { z } from 'zod'
import type { Schema } from '../../lib/api'
import { parseCursor } from '../../lib/agui'
const count = z.number().int().nonnegative().safe().nullable()
const usage = z.object({ input_tokens: count, output_tokens: count, cache_read_tokens: count, cache_write_tokens: count })
const step = z.object({ id: z.string(), title: z.string(), status: z.enum(['pending', 'in_progress', 'completed', 'skipped']) })
const state = z.object({
  status: z.enum(['queued', 'running', 'succeeded', 'failed', 'cancelled']), cancel_requested: z.boolean(), error: z.string(), usage,
  messages: z.array(z.object({ id: z.string(), text: z.string(), closed: z.boolean(), incomplete: z.boolean() })),
  tools: z.array(z.object({ id: z.string(), name: z.string(), arguments: z.string(), result: z.string(), status: z.string(), standard: z.boolean(), parent_message_id: z.string().optional() })),
  artifacts: z.array(z.object({ id: z.string(), space_id: z.string(), session_id: z.string(), run_id: z.string(), call_id: z.string(), object_id: z.string(), sha256: z.string(), name: z.string(), media_type: z.string(), path: z.string(), download_url: z.string() })),
  plan: z.object({ revision: z.number().int().positive(), steps: z.array(step).max(100) }).optional(),
})
const checkpoint = z.object({ session_id: z.string(), stream_id: z.string().uuid().optional(), cursor: z.string(), source_sequence: z.string(), protocol_profile: z.literal('mass-agui-v1'), mapper_version: z.literal(1), parser_version: z.literal(1), closed: z.boolean(), state })
export function parseCheckpoint(input: unknown, space: string, session: string, run: string): Schema['UICheckpoint'] {
  const cp = checkpoint.parse(input)
  parseCursor(cp.cursor); parseCursor(cp.source_sequence)
  if (cp.session_id !== session || cp.state.artifacts.some(a => a.space_id !== space || a.session_id !== session || a.run_id !== run || a.download_url !== `/api/v1/spaces/${space}/artifacts/${a.id}/download`)) throw new Error('snapshot_scope_mismatch')
  const unique = (ids: string[]) => new Set(ids).size === ids.length
  if (!unique(cp.state.messages.map(m => m.id)) || !unique(cp.state.tools.map(t => t.id)) || !unique(cp.state.artifacts.map(a => a.id)) || cp.state.plan && !unique(cp.state.plan.steps.map(s => s.id))) throw new Error('duplicate_snapshot_identity')
  if (cp.closed && !['succeeded', 'failed', 'cancelled'].includes(cp.state.status)) throw new Error('invalid_snapshot_status')
  return cp
}
