import type { components } from './api.generated'

export async function getSystem(): Promise<components['schemas']['SystemInfo']> {
  const response = await fetch('/api/v1/system', { credentials: 'same-origin' })
  if (!response.ok) throw new Error('Service information unavailable')
  const data: unknown = await response.json()
  if (typeof data !== 'object' || data === null || !('name' in data) || data.name !== 'MASS' || !('stage' in data) || data.stage !== 'D00' || !('version' in data) || typeof data.version !== 'string' || !('protocol_profile' in data) || data.protocol_profile !== 'mass-agui-v1') throw new Error('Invalid system response')
  return { name: data.name, stage: data.stage, version: data.version, protocol_profile: data.protocol_profile }
}
