import type { Schema } from '../../lib/api'
import { parseEvent } from '../../lib/agui'
export type UIState = Schema['UIState']
export function reduceEvent(previous: UIState, input: unknown, session: string, run: string): UIState {
  const event = parseEvent(input), state = structuredClone(previous)
  if (event.type === 'RUN_STARTED' || event.type === 'RUN_FINISHED') { if (event.threadId !== session || event.runId !== run) throw new Error('stream_scope_mismatch') }
  switch (event.type) {
    case 'RUN_STARTED': state.status = 'queued'; break
    case 'RUN_FINISHED': state.status = 'succeeded'; break
    case 'RUN_ERROR': state.status = event.code === 'RUN_CANCELLED' ? 'cancelled' : 'failed'; state.error = event.code === 'RUN_CANCELLED' ? 'run_cancelled' : event.code ?? 'run_failed'; break
    case 'TEXT_MESSAGE_START': if (state.messages.some(m => m.id === event.messageId)) throw new Error('duplicate_message'); state.messages.push({ id: event.messageId, text: '', closed: false, incomplete: false }); break
    case 'TEXT_MESSAGE_CONTENT': { const m = state.messages.find(m => m.id === event.messageId); if (!m || m.closed) throw new Error('message_not_open'); m.text += event.delta; break }
    case 'TEXT_MESSAGE_END': { const m = state.messages.find(m => m.id === event.messageId); if (!m || m.closed) throw new Error('message_not_open'); m.closed = true; break }
    case 'TOOL_CALL_START': if (state.tools.some(t => t.id === event.toolCallId)) throw new Error('duplicate_tool'); state.tools.push({ id: event.toolCallId, name: event.toolCallName, arguments: '', result: '', status: 'parameters', standard: true }); break
    case 'TOOL_CALL_ARGS': { const t = state.tools.find(t => t.id === event.toolCallId); if (!t) throw new Error('tool_not_open'); t.arguments += event.delta; break }
    case 'TOOL_CALL_END': { const t = state.tools.find(t => t.id === event.toolCallId); if (!t) throw new Error('tool_not_open'); t.status = 'parameters_complete'; break }
    case 'TOOL_CALL_RESULT': { const t = state.tools.find(t => t.id === event.toolCallId); if (!t) throw new Error('tool_not_open'); if (typeof event.content !== 'string') throw new Error('unsupported_tool_result'); t.result = event.content; t.status = 'completed'; break }
    case 'CUSTOM': {
      const v = event.value as Record<string, unknown>
      if (v.threadId !== session || v.runId !== run) throw new Error('stream_scope_mismatch')
      switch (event.name) {
        case 'mass.run.status.v1': if (typeof v.status === 'string') state.status = v.status; if (typeof v.cancelRequested === 'boolean') state.cancel_requested = v.cancelRequested; break
        case 'mass.message.status.v1': { const m = state.messages.find(m => m.id === v.messageId); if (m && v.status === 'incomplete') m.incomplete = true; break }
        case 'mass.tool.status.v1': { let t = state.tools.find(t => t.id === v.toolCallId); if (!t && typeof v.toolCallId === 'string') { t = { id: v.toolCallId, name: typeof v.toolName === 'string' ? v.toolName : '工具参数见 Trace', arguments: '', result: '', status: '', standard: false }; state.tools.push(t) }; if (t && typeof v.status === 'string') t.status = v.status; break }
        case 'mass.artifact.v1': { const a = v.artifact as Schema['RuntimeArtifact']; if (!a || typeof a.id !== 'string' || a.run_id !== run || !a.download_url.startsWith('/api/v1/spaces/')) throw new Error('invalid_artifact'); if (!state.artifacts.some(x => x.id === a.id)) state.artifacts.push(a); break }
        case 'mass.usage.v1': { const usage = v.usage as Schema['RuntimeUsage']; if (!usage || !['input_tokens', 'output_tokens', 'cache_read_tokens', 'cache_write_tokens'].every(k => usage[k as keyof typeof usage] === null || typeof usage[k as keyof typeof usage] === 'number')) throw new Error('invalid_usage'); state.usage = usage; break }
      }
      break
    }
  }
  return state
}
