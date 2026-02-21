// Channel fetching with DM filtering

import type { Channel, ChannelMember, Team } from '../types'
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

export async function getDMChannelWithUser(userId: string): Promise<Channel | null> {
  const me = await getMe()
  const channels = await getMyDMChannels()

  // Find the DM channel with this user by checking the channel name
  // DM channel names are "{userId1}__{userId2}" sorted alphabetically
  return (
    channels.find((ch) => {
      const otherId = getOtherUserIdFromDMChannel(ch, me.id)
      return otherId === userId
    }) ?? null
  )
}

export async function getDMChannelByUsername(username: string): Promise<Channel | null> {
  const user = await getUserByUsername(username)
  return getDMChannelWithUser(user.id)
}

export async function getChannel(channelId: string): Promise<Channel> {
  const client = getClient()
  return client.get<Channel>(`/channels/${channelId}`)
}

export function normalizeChannelName(channelName: string): string {
  return channelName.replace(/^#/, '')
}

export async function getMyTeams(): Promise<Team[]> {
  const me = await getMe()
  const client = getClient()
  return client.get<Team[]>(`/users/${me.id}/teams`)
}

export async function getChannelByName(teamId: string, channelName: string): Promise<Channel> {
  const client = getClient()
  const name = normalizeChannelName(channelName)
  return client.get<Channel>(`/teams/${teamId}/channels/name/${name}`)
}

export function resolveTeamIdFromList(teams: Team[], teamName?: string): string {
  if (teams.length === 0) {
    throw new Error('You are not a member of any teams.')
  }

  if (teamName) {
    const team = teams.find((t) => t.name === teamName || t.display_name === teamName)
    if (!team) {
      throw new Error(
        `Team "${teamName}" not found. Your teams: ${teams.map((t) => t.name).join(', ')}`,
      )
    }
    return team.id
  }

  if (teams.length === 1) {
    const [team] = teams
    if (!team) throw new Error('You are not a member of any teams.')
    return team.id
  }

  throw new Error(
    `You belong to multiple teams. Use --team to specify:\n` +
      teams.map((t) => `  ${t.name} (${t.display_name})`).join('\n'),
  )
}

export async function resolveTeamId(teamName?: string): Promise<string> {
  const teams = await getMyTeams()

  try {
    return resolveTeamIdFromList(teams, teamName)
  } catch (error) {
    const message = error instanceof Error ? error.message : String(error)
    console.error(`Error: ${message}`)
    process.exit(1)
  }
}

export async function getTeamChannelMembers(teamId: string): Promise<ChannelMember[]> {
  const me = await getMe()
  const client = getClient()
  return client.get<ChannelMember[]>(`/users/${me.id}/teams/${teamId}/channels/members`)
}

export async function getChannelMember(channelId: string): Promise<ChannelMember> {
  const me = await getMe()
  const client = getClient()
  return client.get<ChannelMember>(`/channels/${channelId}/members/${me.id}`)
}

// Extract the other user's ID from a DM channel name
// DM channel names are formatted as "{userId1}__{userId2}" sorted alphabetically
export function getOtherUserIdFromDMChannel(channel: Channel, myUserId: string): string | null {
  if (channel.type !== 'D') return null

  const parts = channel.name.split('__')
  if (parts.length !== 2) return null
  const [left, right] = parts
  if (!left || !right) return null

  return left === myUserId ? right : left
}
