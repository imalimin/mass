import { describe, expect, it } from 'vitest'
import type { Schema } from '../../lib/api'
import { buildAssistantTurn, processSummaryLabel } from './assistantTurn'

type Message = Schema['RuntimeMessage']
type Tool = Schema['UITool']
type UIState = Schema['UIState']
type Run = Schema['RuntimeRun']

const makeTool = (id: string, parentMessageID?: string): Tool => ({
  id,
  name: 'read_file',
  arguments: '{}',
  result: '{"ok":true}',
  status: 'completed',
  standard: true,
  parent_message_id: parentMessageID,
})

const makeMessage = (id: string, content: string, opts?: { sourceEventID?: string; tools?: Tool[] }): Message => ({
  id,
  run_id: 'run-1',
  role: 'assistant',
  content_id: `content-${id}`,
  source_event_id: opts?.sourceEventID ?? `evt-${id}`,
  sequence: '1',
  complete: true,
  content,
  truncated: false,
  tools: opts?.tools,
})

const makeState = (partial: Partial<UIState>): UIState => ({
  status: 'running',
  cancel_requested: false,
  messages: [],
  tools: [],
  artifacts: [],
  usage: { input_tokens: 0, output_tokens: 0, cache_read_tokens: 0, cache_write_tokens: 0 },
  error: '',
  ...partial,
})

const makeRun = (status: Run['status'], finalResponseEventID = '') => ({
  status,
  final_response_event_id: finalResponseEventID,
})

describe('buildAssistantTurn', () => {
  it('keeps intermediate steps expanded while a run is still running', () => {
    const state = makeState({
      status: 'running',
      messages: [
        { id: 'm1', text: '先检查目录', closed: true, incomplete: false },
        { id: 'm2', text: '正在输出最终结论…', closed: false, incomplete: false },
      ],
      tools: [makeTool('t1', 'm1')],
    })

    const model = buildAssistantTurn([], state, makeRun('running'), true)
    expect(model.collapseProcess).toBe(false)
    expect(model.finalStep).toBeNull()
    expect(model.processSteps).toHaveLength(2)
  })

  it('auto-collapses reasoning and tools once the live run succeeds with a final response', () => {
    const state = makeState({
      status: 'succeeded',
      messages: [
        { id: 'm1', text: '先读取文件', closed: true, incomplete: false },
        { id: 'm2', text: '再执行单测', closed: true, incomplete: false },
        { id: 'm3', text: '全部完成，结果如下。', closed: true, incomplete: false },
      ],
      tools: [makeTool('t1', 'm1'), makeTool('t2', 'm1'), makeTool('t3', 'm2')],
    })

    const model = buildAssistantTurn([], state, makeRun('running'), true)
    expect(model.collapseProcess).toBe(true)
    expect(model.finalStep?.id).toBe('m3')
    expect(model.processSteps.map(step => step.id)).toEqual(['m1', 'm2'])
    expect(model.reasoningCount).toBe(2)
    expect(model.toolCount).toBe(3)
    expect(processSummaryLabel(model.reasoningCount, model.toolCount)).toBe('思考与工具过程 · 2 段思考 · 3 次工具调用')
  })

  it('collapses persisted historical runs using final_response_event_id', () => {
    const messages = [
      makeMessage('m1', '', { sourceEventID: 'evt-1', tools: [makeTool('t1', 'm1'), makeTool('t2', 'm1')] }),
      makeMessage('m2', '最终结果。', { sourceEventID: 'evt-2' }),
    ]

    const model = buildAssistantTurn(messages, null, makeRun('succeeded', 'evt-2'), false)
    expect(model.collapseProcess).toBe(true)
    expect(model.finalStep?.id).toBe('m2')
    expect(model.processSteps.map(step => step.id)).toEqual(['m1'])
    expect(model.reasoningCount).toBe(0)
    expect(model.toolCount).toBe(2)
    expect(processSummaryLabel(model.reasoningCount, model.toolCount)).toBe('工具执行过程 · 2 次工具调用')
  })

  it('does not show a collapsible drawer for single-step direct replies', () => {
    const messages = [makeMessage('m1', '你好！', { sourceEventID: 'evt-1' })]
    const model = buildAssistantTurn(messages, null, makeRun('succeeded', 'evt-1'), false)
    expect(model.collapseProcess).toBe(false)
    expect(model.finalStep?.id).toBe('m1')
    expect(model.processSteps).toEqual([])
  })

  it('keeps steps visible when a run fails without producing a final result', () => {
    const state = makeState({
      status: 'failed',
      error: 'tool_failed',
      messages: [
        { id: 'm1', text: '准备执行命令', closed: true, incomplete: false },
        { id: 'm2', text: '半截输出', closed: true, incomplete: true },
      ],
      tools: [makeTool('t1', 'm1')],
    })

    const model = buildAssistantTurn([], state, makeRun('failed'), true)
    expect(model.collapseProcess).toBe(false)
    expect(model.finalStep).toBeNull()
    expect(model.processSteps).toHaveLength(2)
  })
})
