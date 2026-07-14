import { describe, expect, test } from 'vitest'
import { parseDuration, takeMostRecentPosts } from '../../src/api/posts'

describe('parseDuration', () => {
  test('parses hours', () => {
    const now = Date.now()
    const since = parseDuration('24h')

    // Should be roughly 24 hours ago (within 1 second tolerance)
    const expected = now - 24 * 60 * 60 * 1000
    expect(Math.abs(since - expected)).toBeLessThan(1000)
  })

  test('parses days', () => {
    const now = Date.now()
    const since = parseDuration('7d')

    const expected = now - 7 * 24 * 60 * 60 * 1000
    expect(Math.abs(since - expected)).toBeLessThan(1000)
  })

  test('parses weeks', () => {
    const now = Date.now()
    const since = parseDuration('2w')

    const expected = now - 2 * 7 * 24 * 60 * 60 * 1000
    expect(Math.abs(since - expected)).toBeLessThan(1000)
  })

  test('parses months (30 days)', () => {
    const now = Date.now()
    const since = parseDuration('1m')

    const expected = now - 30 * 24 * 60 * 60 * 1000
    expect(Math.abs(since - expected)).toBeLessThan(1000)
  })

  test('throws on invalid format', () => {
    expect(() => parseDuration('invalid')).toThrow()
    expect(() => parseDuration('24')).toThrow()
    expect(() => parseDuration('h')).toThrow()
  })
})

describe('takeMostRecentPosts', () => {
  test('keeps the most recent posts across channels under a global limit', () => {
    const posts = [
      { id: 'a1', channel_id: 'chan-a', create_at: 1000 },
      { id: 'b1', channel_id: 'chan-b', create_at: 5000 },
      { id: 'a2', channel_id: 'chan-a', create_at: 4000 },
      { id: 'b2', channel_id: 'chan-b', create_at: 3000 },
    ]

    const result = takeMostRecentPosts(posts, 2)

    expect(result.map((post) => post.id)).toEqual(['b1', 'a2'])
  })

  test('uses post id as a stable tiebreaker when timestamps match', () => {
    const posts = [
      { id: 'post-b', channel_id: 'chan-a', create_at: 1000 },
      { id: 'post-a', channel_id: 'chan-b', create_at: 1000 },
    ]

    const result = takeMostRecentPosts(posts, 2)

    expect(result.map((post) => post.id)).toEqual(['post-a', 'post-b'])
  })

  test('does not let duplicate post ids consume the global limit', () => {
    const posts = [
      { id: 'new', create_at: 3000 },
      { id: 'new', create_at: 3000 },
      { id: 'older', create_at: 2000 },
    ]

    expect(takeMostRecentPosts(posts, 2).map((post) => post.id)).toEqual(['new', 'older'])
  })
})
