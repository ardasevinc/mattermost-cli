// Channel fetching with DM filtering

import type { Channel, User } from '../types'
import { getClient } from './client'
import { getMe, getUserByUsername } from './users'

export async function getMyChannels(): Promise<Channel[]> {
  const me = await getMe()
  const client = getClient()

  // Get all channels for the user's teams first, then their direct channels
  // For DMs, we use the user's direct channels endpoint
  const channels = await client.get<Channel[]>(`/users/${me.id}/channels`)

  return channels
}

export async function getMyDMChannels(): Promise<Channel[]> {
  const channels = await getMyChannels()
  // Filter for direct messages only (type 'D')
  return channels.filter((ch) => ch.type === 'D')
}

export async function getMyGroupDMChannels(): Promise<Channel[]> {
  const channels = await getMyChannels()
  // Filter for group DMs (type 'G')
  return channels.filter((ch) => ch.type === 'G')
}

export async function getDMChannelWithUser(
  userId: string
): Promise<Channel | null> {
  const me = await getMe()
  const client = getClient()

  try {
    // Mattermost DM channel names are the two user IDs sorted and joined
    const channel = await client.get<Channel>(
      `/channels/direct/${me.id}/${userId}`
    )
    return channel
  } catch {
    // Don't create channel - this is a read-only operation
    return null
  }
}

export async function getDMChannelByUsername(
  username: string
): Promise<Channel | null> {
  const user = await getUserByUsername(username)
  return getDMChannelWithUser(user.id)
}

export async function getChannel(channelId: string): Promise<Channel> {
  const client = getClient()
  return client.get<Channel>(`/channels/${channelId}`)
}

// Extract the other user's ID from a DM channel name
// DM channel names are formatted as "{userId1}__{userId2}" sorted alphabetically
export function getOtherUserIdFromDMChannel(
  channel: Channel,
  myUserId: string
): string | null {
  if (channel.type !== 'D') return null

  const parts = channel.name.split('__')
  if (parts.length !== 2) return null

  return parts[0] === myUserId ? parts[1] : parts[0]
}
