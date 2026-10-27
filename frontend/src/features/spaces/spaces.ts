import type { Schema } from '../../lib/api'

// Preserve database microseconds when two creation times share a millisecond.
export function sortSpacesByCreation(spaces: Schema['Space'][]) {
  const fraction = (time: string) => (time.match(/\.(\d+)/)?.[1] ?? '').padEnd(9, '0')
  return [...spaces].sort((a, b) => Date.parse(a.created_at) - Date.parse(b.created_at)
    || fraction(a.created_at).localeCompare(fraction(b.created_at)) || a.id.localeCompare(b.id))
}
