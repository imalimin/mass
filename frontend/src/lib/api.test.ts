import { afterEach, expect, it, vi } from 'vitest'
import { payloadText } from './api'
afterEach(() => vi.unstubAllGlobals())
it('limits streamed payloads even without Content-Length', async () => {
  let cancelled = false
  const body = new ReadableStream({ start(c) { c.enqueue(new Uint8Array(1024 * 1024)); c.enqueue(new Uint8Array(1)) }, cancel() { cancelled = true } })
  vi.stubGlobal('fetch', vi.fn(async () => new Response(body)))
  await expect(payloadText('/payload')).rejects.toThrow('超过 1 MiB'); expect(cancelled).toBe(true)
})
it('preserves UTF-8 characters split across response chunks', async () => {
  const bytes = new TextEncoder().encode('你好'), body = new ReadableStream({ start(c) { c.enqueue(bytes.slice(0, 1)); c.enqueue(bytes.slice(1)); c.close() } })
  vi.stubGlobal('fetch', vi.fn(async () => new Response(body)))
  await expect(payloadText('/payload')).resolves.toBe('你好')
})
