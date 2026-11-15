import type { components } from './api.generated'
export type Schema = components['schemas']
export class APIError extends Error {
  constructor(public status: number, public code: string) {
    super(({ run_active: '会话正在运行或维护文件，请稍后重试。', run_state_conflict: '运行状态已变化，请刷新。', runtime_unavailable: '执行服务暂不可用，请联系管理员。', context_summary_failed: '上下文摘要失败，历史原文已保留。请检查 Trace 或调整容量。', context_overflow: '完整上下文超出配置容量，请缩小输入或新建会话。', unsupported_tool: '所选工具或版本不可用，请更新 Agent 工具配置。', invalid_cursor: '事件历史游标无效，请重新同步。', workspace_unavailable: '本地目录不可用，可恢复最近已提交快照。', draft_conflict: '草稿已更新，请比较差异后重新编辑。', version_conflict: '此版本号已经发布，请使用新的版本号。', version_referenced: '此版本正在被引用，不能删除或归档默认版本。', invalid_definition: '请检查定义内容、准确版本、资源路径及配置限额。', skill_document_missing: 'ZIP 根目录必须包含 SKILL.md。', content_unavailable: '持久化内容暂不可用，请稍后重试。', unauthenticated: '登录已失效，请重新登录。', forbidden: '没有访问权限，或成员资格已被撤销。', conflict: '内容已变更，请刷新后重试。', not_found: '未找到账号或资源。', invalid_request: '请检查输入内容。', unsupported_capability: '模型能力或端点不受支持。', rate_limited: '登录尝试过于频繁，请稍后重试。' } as Record<string, string>)[code] ?? '操作未完成，请稍后重试。')
  }
}
export async function request<T>(path: string, method = 'GET', body?: unknown, signal?: AbortSignal): Promise<T> {
  const binary = body instanceof Blob
  const response = await fetch(`/api/v1${path}`, {
    method, credentials: 'same-origin', signal,
    headers: method === 'GET' ? {} : { 'Content-Type': binary ? body.type : 'application/json' },
    body: binary ? body : body === undefined ? undefined : JSON.stringify(body),
  })
  if (!response.ok) {
    const data = await response.json().catch(() => ({})) as { code?: string }
    const error = new APIError(response.status, data.code ?? 'unknown')
    if (response.status === 401 && !path.startsWith('/auth/')) window.dispatchEvent(new Event('mass-session-expired'))
    throw error
  }
  return response.status === 204 ? undefined as T : await response.json() as T
}
export async function list<T>(path: string, signal?: AbortSignal): Promise<T[]> {
  const items: T[] = []; let cursor: string | null = null
  do {
    const query = new URLSearchParams({ limit: '100', ...(cursor ? { cursor } : {}) })
    const page: { items: T[]; next_cursor: string | null } = await request(`${path}${path.includes('?') ? '&' : '?'}${query}`, 'GET', undefined, signal)
    items.push(...page.items); cursor = page.next_cursor
  } while (cursor)
  return items
}
export async function payloadText(path: string, signal?: AbortSignal, { maxBytes = 1024 * 1024, utf8Only = false } = {}): Promise<string> {
  const response = await fetch(`/api/v1${path}`, { credentials: 'same-origin', signal })
  if (!response.ok) {
    const data = await response.json().catch(() => ({})) as { code?: string }
    if (response.status === 401) window.dispatchEvent(new Event('mass-session-expired'))
    throw new APIError(response.status, data.code ?? 'unknown')
  }
  const limitError = `载荷超过 ${maxBytes / (1024 * 1024)} MiB，请下载完整内容查看。`
  if (Number(response.headers.get('Content-Length')) > maxBytes) {
    await response.body?.cancel()
    throw new Error(limitError)
  }
  if (!response.body) return ''
  const reader = response.body.getReader()
  const decoder = new TextDecoder('utf-8', { fatal: utf8Only, ignoreBOM: utf8Only })
  let size = 0, text = ''
  const decode = (bytes?: Uint8Array) => {
    try {
      return decoder.decode(bytes, { stream: bytes !== undefined })
    } catch {
      throw new Error('此文件不是 UTF-8 文本，可下载原文件。')
    }
  }
  try {
    for (;;) {
      const { value, done } = await reader.read()
      if (done) break
      size += value.byteLength
      if (size > maxBytes) throw new Error(limitError)
      text += decode(value)
    }
    text += decode()
    if (utf8Only && text.includes('\0')) throw new Error('此文件不是 UTF-8 文本，可下载原文件。')
    return text
  } catch (error) {
    await reader.cancel().catch(() => {})
    throw error
  } finally {
    reader.releaseLock()
  }
}
