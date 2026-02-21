import { describe, expect, test } from 'bun:test'
import type { Channel } from '../../src/types'
import { getOtherUserIdFromDMChannel } from '../../src/api/channels'

function makeDMChannel(name: string): Channel {
  return {
    id: 'ch1',
    type: 'D',
    display_name: '',
    name,
    header: '',
    purpose: '',
    last_post_at: 0,
    total_msg_count: 0,
    creator_id: '',
  }
}

describe('getOtherUserIdFromDMChannel', () => {
  test('extracts other user ID from DM channel name', () => {
    const channel = makeDMChannel('abc123__def456')
    expect(getOtherUserIdFromDMChannel(channel, 'abc123')).toBe('def456')
    expect(getOtherUserIdFromDMChannel(channel, 'def456')).toBe('abc123')
  })

  test('returns null for non-DM channels', () => {
    const channel = { ...makeDMChannel('general'), type: 'O' as const }
    expect(getOtherUserIdFromDMChannel(channel, 'abc123')).toBeNull()
  })

  test('returns null for malformed channel names', () => {
    const channel = makeDMChannel('no-separator')
    expect(getOtherUserIdFromDMChannel(channel, 'abc123')).toBeNull()
  })

  test('returns null when a name part is empty', () => {
    const channel = makeDMChannel('__def456')
    expect(getOtherUserIdFromDMChannel(channel, 'abc123')).toBeNull()
  })
})
