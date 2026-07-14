import { describe, expect, test } from 'vitest'
import {
  comparePostIds,
  decodeChannelHistoryCursor,
  encodeChannelHistoryCursor,
} from '../src/cursor'

const cursor = {
  v: 1 as const,
  scope: 'channel' as const,
  channelId: 'channel',
  boundary: { createAt: 123, id: 'post' },
  since: 10,
}

describe('channel history cursors', () => {
  test('round trips as bounded base64url', () => {
    const encoded = encodeChannelHistoryCursor(cursor)
    expect(encoded).toMatch(/^[A-Za-z0-9_-]+$/)
    expect(decodeChannelHistoryCursor(encoded)).toEqual(cursor)
  })

  test.each(['', 'not json', 'e30=', 'a'.repeat(2049)])('rejects malformed cursor %j', (value) => {
    expect(() => decodeChannelHistoryCursor(value)).toThrow('Invalid cursor.')
  })

  test('rejects unsupported versions and invalid fields', () => {
    const encode = (value: unknown) => Buffer.from(JSON.stringify(value)).toString('base64url')
    expect(() => decodeChannelHistoryCursor(encode({ ...cursor, v: 2 }))).toThrow('Invalid cursor.')
    expect(() =>
      decodeChannelHistoryCursor(encode({ ...cursor, boundary: { createAt: -1, id: '' } })),
    ).toThrow('Invalid cursor.')
    expect(() => decodeChannelHistoryCursor(encode({ ...cursor, extra: true }))).toThrow(
      'Invalid cursor.',
    )
    expect(() =>
      decodeChannelHistoryCursor(
        encode({ ...cursor, boundary: { ...cursor.boundary, extra: true } }),
      ),
    ).toThrow('Invalid cursor.')
    expect(() => decodeChannelHistoryCursor(encode({ ...cursor, since: 124 }))).toThrow(
      'Invalid cursor.',
    )
    expect(() => encodeChannelHistoryCursor({ ...cursor, channelId: 'not safe!' })).toThrow(
      'Invalid cursor.',
    )
  })

  test('uses one deterministic ASCII ID ordering', () => {
    expect(['z', 'A', '_', '-'].sort(comparePostIds)).toEqual(['-', 'A', '_', 'z'])
  })
})
