import { describe, expect, test } from 'bun:test'
import type { Channel, ChannelMember } from '../../src/types'
import { calculateUnreadMetrics, sortUnreadEntries } from '../../src/utils/unread'

function makeChannel(totalMessages: number): Channel {
  return {
    id: 'ch1',
    type: 'O',
    display_name: 'General',
    name: 'general',
    header: '',
    purpose: '',
    last_post_at: 0,
    total_msg_count: totalMessages,
    creator_id: 'u1',
  }
}

function makeMember(msgCount: number, mentionCount: number): ChannelMember {
  return {
    channel_id: 'ch1',
    user_id: 'u1',
    msg_count: msgCount,
    mention_count: mentionCount,
    last_viewed_at: 0,
  }
}

describe('calculateUnreadMetrics', () => {
  test('computes unread and mention counts', () => {
    const metrics = calculateUnreadMetrics(makeChannel(42), makeMember(30, 3))
    expect(metrics.unreadCount).toBe(12)
    expect(metrics.mentionCount).toBe(3)
  })

  test('clamps negative unread values to 0', () => {
    const metrics = calculateUnreadMetrics(makeChannel(10), makeMember(20, 1))
    expect(metrics.unreadCount).toBe(0)
  })
})

describe('sortUnreadEntries', () => {
  test('sorts by mention count desc then unread count desc', () => {
    const sorted = sortUnreadEntries([
      { unreadCount: 9, mentionCount: 0, id: 'a' },
      { unreadCount: 2, mentionCount: 3, id: 'b' },
      { unreadCount: 7, mentionCount: 3, id: 'c' },
      { unreadCount: 10, mentionCount: 1, id: 'd' },
    ])

    expect(sorted.map((entry) => entry.id)).toEqual(['c', 'b', 'd', 'a'])
  })
})
