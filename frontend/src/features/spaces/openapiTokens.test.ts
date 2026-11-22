import { describe, expect, it } from 'vitest'
import {
  defaultCreateExpiry,
  defaultRenewExpiry,
  openAPITokenStatusLabel,
  parseDateTimeLocalToISO,
  sortOpenAPITokens,
  toDateTimeLocalValue,
  type OpenAPIToken,
} from './openapiTokens'

const token = (overrides: Partial<OpenAPIToken>): OpenAPIToken => ({
  id: '00000000-0000-0000-0000-000000000001',
  name: 'token',
  status: 'active',
  created_at: '2026-11-22T06:00:00Z',
  expires_at: '2027-05-21T06:00:00Z',
  revoked_at: null,
  ...overrides,
})

describe('openapi token helpers', () => {
  it('sorts tokens by created_at descending with microsecond precision', () => {
    const older = token({ id: '00000000-0000-0000-0000-000000000001', created_at: '2026-11-22T06:00:00.100000Z' })
    const newer = token({ id: '00000000-0000-0000-0000-000000000002', created_at: '2026-11-22T06:00:00.100005Z' })
    const newest = token({ id: '00000000-0000-0000-0000-000000000003', created_at: '2026-11-22T07:00:00Z' })
    expect(sortOpenAPITokens([older, newest, newer]).map(item => item.id)).toEqual([
      newest.id,
      newer.id,
      older.id,
    ])
  })

  it('computes default 180-day expiry for creation and active/expired renewal', () => {
    const now = new Date('2026-11-22T06:00:00Z')
    expect(defaultCreateExpiry(now).toISOString()).toBe('2027-05-21T06:00:00.000Z')

    const active = token({ status: 'active', expires_at: '2027-01-01T00:00:00Z' })
    expect(defaultRenewExpiry(active, now).toISOString()).toBe('2027-06-30T00:00:00.000Z')

    const expired = token({ status: 'expired', expires_at: '2026-01-01T00:00:00Z' })
    expect(defaultRenewExpiry(expired, now).toISOString()).toBe('2027-05-21T06:00:00.000Z')
  })

  it('formats and parses datetime-local values and status labels', () => {
    const localValue = toDateTimeLocalValue(new Date('2026-11-22T06:30:00Z'))
    expect(localValue).toMatch(/^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}$/)
    const parsed = parseDateTimeLocalToISO(localValue)
    expect(parsed).toBe('2026-11-22T06:30:00.000Z')
    expect(parseDateTimeLocalToISO('')).toBeNull()
    expect(openAPITokenStatusLabel('active')).toBe('有效')
    expect(openAPITokenStatusLabel('expired')).toBe('已过期')
    expect(openAPITokenStatusLabel('revoked')).toBe('已吊销')
  })
})
