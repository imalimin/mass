import type { Schema } from '../../lib/api'

type Message = Schema['RuntimeMessage']
type Tool = Schema['UITool']
type RunSummary = Pick<Schema['RuntimeRun'], 'status' | 'final_response_event_id'>

export type AssistantStep = {
  id: string
  sourceEventID?: string
  text: string
  live?: Schema['UIMessage']
  contentID?: string
  tools: Tool[]
}

export type AssistantTurnModel = {
  steps: AssistantStep[]
  processSteps: AssistantStep[]
  finalStep: AssistantStep | null
  unattached: Tool[]
  collapseProcess: boolean
  reasoningCount: number
  toolCount: number
}

const isActiveStatus = (status?: string) => status === 'queued' || status === 'running'

const isCompleteFinalCandidate = (step?: AssistantStep) => Boolean(
  step
  && step.tools.length === 0
  && !step.live?.incomplete
  && (!step.live || step.live.closed)
  && (step.text || step.contentID),
)

export function buildAssistantTurn(
  messages: Message[],
  state: Schema['UIState'] | null,
  run?: RunSummary,
  isLatest = false,
): AssistantTurnModel {
  const positions = new Map(messages.map((message, index) => [message.id, index]))
  const liveTools = new Map(state?.tools.map(tool => [tool.id, tool]))
  const toolsByMessage = new Map<string, Tool[]>()
  for (const tool of liveTools.values()) {
    if (!tool.parent_message_id) continue
    const tools = toolsByMessage.get(tool.parent_message_id) ?? []
    tools.push(tool)
    toolsByMessage.set(tool.parent_message_id, tools)
  }
  const attached = new Set<string>()
  const steps: AssistantStep[] = []
  const addStep = (message?: Message, live?: Schema['UIMessage']) => {
    const id = live?.id ?? message?.id
    if (!id) return
    const saved = message?.content ?? ''
    const text = live && live.text.length >= saved.length ? live.text : saved
    const contentID = message?.truncated && text.length <= saved.length ? message.content_id : undefined
    const tools = new Map(message?.tools?.map(tool => [tool.id, tool]))
    for (const tool of toolsByMessage.get(id) ?? []) {
      if (!tools.has(tool.id)) tools.set(tool.id, tool)
    }
    const calls = [...tools.values()].map(tool => {
      attached.add(tool.id)
      const active = liveTools.get(tool.id)
      return active ? { ...active, arguments: active.arguments || tool.arguments, result: active.result || tool.result } : tool
    })
    if (!text && !contentID) {
      if (!calls.length) return
      const previous = steps.at(-1)
      if (previous) {
        previous.tools.push(...calls)
        return
      }
    }
    steps.push({ id, sourceEventID: message?.source_event_id, text, live, contentID, tools: calls })
  }

  // Merge by message ID before grouping textless tool steps. History may start mid-run.
  let next = 0
  for (const live of state?.messages ?? []) {
    const position = positions.get(live.id)
    if (position === undefined) addStep(undefined, live)
    else {
      while (next < position) addStep(messages[next++])
      addStep(messages[next++], live)
    }
  }
  while (next < messages.length) addStep(messages[next++])
  const unattached = [...liveTools.values()].filter(tool => !attached.has(tool.id))

  const runStatus = (run && !isActiveStatus(run.status)) ? run.status : state?.status || run?.status
  let finalIndex = -1
  if (run?.final_response_event_id) {
    const matched = steps.findIndex(step => step.sourceEventID === run.final_response_event_id)
    if (matched >= 0 && isCompleteFinalCandidate(steps[matched])) {
      finalIndex = matched
    }
  }
  if (finalIndex === -1 && (runStatus === 'succeeded' || (!isLatest && !runStatus))) {
    const last = steps.at(-1)
    if (isCompleteFinalCandidate(last)) {
      finalIndex = steps.length - 1
    }
  }

  const finalStep = finalIndex >= 0 ? steps[finalIndex] : null
  const processSteps = finalIndex >= 0 ? steps.filter((_, index) => index !== finalIndex) : steps
  const reasoningCount = processSteps.filter(step => Boolean(step.text.trim() || step.contentID)).length
  const toolCount = processSteps.reduce((sum, step) => sum + step.tools.length, 0) + unattached.length
  const collapseProcess = finalStep !== null && (processSteps.length > 0 || unattached.length > 0)

  return { steps, processSteps, finalStep, unattached, collapseProcess, reasoningCount, toolCount }
}

export function processSummaryLabel(reasoningCount: number, toolCount: number): string {
  const parts: string[] = []
  if (reasoningCount > 0) parts.push(`${reasoningCount} 段思考`)
  if (toolCount > 0) parts.push(`${toolCount} 次工具调用`)
  const title = reasoningCount > 0 && toolCount > 0 ? '思考与工具过程'
    : toolCount > 0 ? '工具执行过程'
      : '思考过程'
  return parts.length ? `${title} · ${parts.join(' · ')}` : title
}
