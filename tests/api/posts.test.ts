import { test, expect, describe } from 'bun:test'
import { parseDuration } from '../../src/api/posts'

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
