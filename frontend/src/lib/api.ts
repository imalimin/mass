import type { components } from './api.generated'
export type Schema = components['schemas']
export class APIError extends Error {
  constructor(public status: number, public code: string) {
    super(({ unauthenticated: '登录已失效，请重新登录。', forbidden: '没有访问权限，或成员资格已被撤销。', conflict: '内容已变更，请刷新后重试。', not_found: '未找到账号或资源。', invalid_request: '请检查输入内容。', unsupported_capability: '模型能力或端点不受支持。', rate_limited: '登录尝试过于频繁，请稍后重试。' } as Record<string, string>)[code] ?? '操作未完成，请稍后重试。')
  }
}
export async function request<T>(path: string, method = 'GET', body?: unknown, signal?: AbortSignal): Promise<T> {
  const response = await fetch(`/api/v1${path}`, { method, credentials: 'same-origin', signal, headers: method === 'GET' ? {} : { 'Content-Type': 'application/json' }, body: body === undefined ? undefined : JSON.stringify(body) })
  if (!response.ok) {
    const data = await response.json().catch(() => ({})) as { code?: string }
    const error = new APIError(response.status, data.code ?? 'unknown')
    if ((response.status === 401 || response.status === 403) && !path.startsWith('/auth/')) window.dispatchEvent(new CustomEvent('mass-access-lost', { detail: response.status }))
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
export async function getSystem(): Promise<Schema['SystemInfo']> { return request('/system') }
