import { expect, it } from 'vitest'
import { sortSpacesByCreation } from './spaces'
const space = (id: string, created_at: string) => ({ id, created_at, name: id, is_owner: false })
it('orders accessible spaces by creation time rather than ID without mutating the response', () => {
  const spaces = [space('a-new', '2026-10-27T00:00:00Z'), space('z-old', '2026-10-24T00:00:00Z')]
  expect(sortSpacesByCreation(spaces).map(s => s.id)).toEqual(['z-old', 'a-new'])
  expect(spaces[0].id).toBe('a-new')
})
it('preserves sub-millisecond creation order and uses ID for equal timestamps', () => {
  expect(sortSpacesByCreation([
    space('a-later', '2026-10-24T00:00:00.000002Z'),
    space('z-first', '2026-10-24T00:00:00Z'),
    space('z-earlier', '2026-10-24T00:00:00.000001Z'),
    space('b-tie', '2026-10-24T00:00:00.000001Z'),
  ]).map(s => s.id)).toEqual(['z-first', 'b-tie', 'z-earlier', 'a-later'])
})
it('handles a user with no spaces', () => expect(sortSpacesByCreation([])).toEqual([]))
