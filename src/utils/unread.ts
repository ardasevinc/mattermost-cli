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
  return {
    unreadCount: Math.max(0, channel.total_msg_count - member.msg_count),
    mentionCount: Math.max(0, member.mention_count),
  }
}

export function sortUnreadEntries<T extends UnreadSortable>(entries: T[]): T[] {
  return [...entries].sort((a, b) => {
    if (b.mentionCount !== a.mentionCount) return b.mentionCount - a.mentionCount
    return b.unreadCount - a.unreadCount
  })
}
