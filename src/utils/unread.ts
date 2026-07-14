import type { Channel, ChannelMember } from '../types'

export interface UnreadMetrics {
  unreadCount: number
  mentionCount: number
}

export interface UnreadSortable {
  unreadCount: number
  mentionCount: number
}

export function calculateUnreadMetrics(channel: Channel, member: ChannelMember): UnreadMetrics {
  const total = nonNegativeInteger(channel.total_msg_count)
  const read = nonNegativeInteger(member.msg_count)
  return {
    unreadCount: Math.max(0, total - read),
    mentionCount: nonNegativeInteger(member.mention_count),
  }
}

function nonNegativeInteger(value: unknown): number {
  return typeof value === 'number' && Number.isSafeInteger(value) && value >= 0 ? value : 0
}

export function sortUnreadEntries<T extends UnreadSortable>(entries: T[]): T[] {
  return [...entries].sort((a, b) => {
    if (b.mentionCount !== a.mentionCount) return b.mentionCount - a.mentionCount
    return b.unreadCount - a.unreadCount
  })
}
