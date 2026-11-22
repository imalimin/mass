import type { Schema } from '../../lib/api'

export type OpenAPIToken = Schema['OpenAPIToken']

const DAY_MS = 24 * 60 * 60 * 1000
export const OPENAPI_DEFAULT_TTL_DAYS = 180

const fraction = (time: string) => (time.match(/\.(\d+)/)?.[1] ?? '').padEnd(9, '0')

export function sortOpenAPITokens(tokens: OpenAPIToken[]): OpenAPIToken[] {
  return [...tokens].sort((a, b) => Date.parse(b.created_at) - Date.parse(a.created_at)
    || fraction(b.created_at).localeCompare(fraction(a.created_at))
    || a.id.localeCompare(b.id))
}

export function defaultCreateExpiry(now = new Date()): Date {
  return new Date(now.getTime() + OPENAPI_DEFAULT_TTL_DAYS * DAY_MS)
}

export function defaultRenewExpiry(token: Pick<OpenAPIToken, 'status' | 'expires_at'>, now = new Date()): Date {
  const base = token.status === 'active' ? Date.parse(token.expires_at) : now.getTime()
  const anchor = Number.isNaN(base) ? now.getTime() : base
  return new Date(anchor + OPENAPI_DEFAULT_TTL_DAYS * DAY_MS)
}

const pad2 = (value: number) => String(value).padStart(2, '0')

export function toDateTimeLocalValue(date: Date): string {
  return `${date.getFullYear()}-${pad2(date.getMonth() + 1)}-${pad2(date.getDate())}T${pad2(date.getHours())}:${pad2(date.getMinutes())}`
}

export function parseDateTimeLocalToISO(value: string): string | null {
  if (!value.trim()) return null
  const parsed = new Date(value)
  if (Number.isNaN(parsed.getTime())) return null
  return parsed.toISOString()
}

export function formatLocalDateTime(iso: string): string {
  const date = new Date(iso)
  if (Number.isNaN(date.getTime())) return iso
  return `${date.getFullYear()}-${pad2(date.getMonth() + 1)}-${pad2(date.getDate())} ${pad2(date.getHours())}:${pad2(date.getMinutes())}`
}

export function openAPITokenStatusLabel(status: OpenAPIToken['status']): string {
  if (status === 'active') return '有效'
  if (status === 'expired') return '已过期'
  return '已吊销'
}
