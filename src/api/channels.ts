// Channel fetching with DM filtering

import { preprocess, sanitizeTerminalLabel } from '../preprocessing'
import type { Channel, ChannelMember, Team } from '../types'
import { getClient, MattermostMutationOutcomeUnknownError } from './client'
import { getMe, getUserByUsername } from './users'

export interface CanonicalTeam {
  id: string
  name: string
  displayName: string
}

export function normalizeCanonicalTeams(teams: unknown): CanonicalTeam[] {
  if (!Array.isArray(teams)) throw new Error('Invalid teams response.')
  return teams.map((team) => {
    if (typeof team !== 'object' || team === null || Array.isArray(team)) {
      throw new Error('Invalid teams response.')
    }
    const raw = team as Record<string, unknown>
    if (
      typeof raw.id !== 'string' ||
      raw.id.length === 0 ||
      typeof raw.name !== 'string' ||
      raw.name.length === 0 ||
      typeof raw.display_name !== 'string' ||
      (raw.type !== 'O' && raw.type !== 'I')
    ) {
      throw new Error('Invalid teams response.')
    }
    return { id: raw.id, name: raw.name, displayName: raw.display_name }
  })
}

export async function getMyChannels(userId?: string): Promise<Channel[]> {
  const resolvedUserId = userId ?? (await getMe()).id
  const client = getClient()

  // Get all channels for the user's teams first, then their direct channels
  // For DMs, we use the user's direct channels endpoint
  const channels = await client.get<Channel[]>(
    `/users/${encodeURIComponent(resolvedUserId)}/channels`,
  )

  return channels
}

export async function getMyDMChannels(userId?: string): Promise<Channel[]> {
  const channels = await getMyChannels(userId)
  // Filter for direct messages only (type 'D')
  return [...new Map(channels.filter((ch) => ch.type === 'D').map((ch) => [ch.id, ch])).values()]
}

export async function getMyGroupDMChannels(userId?: string): Promise<Channel[]> {
  const channels = await getMyChannels(userId)
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

export async function createDirectChannel(myUserId: string, otherUserId: string): Promise<Channel> {
  if (!myUserId || !otherUserId) throw new Error('Invalid direct-message participants.')
  const client = getClient()
  const channel = await client.post<unknown>('/channels/direct', [myUserId, otherUserId])
  if (
    typeof channel !== 'object' ||
    channel === null ||
    Array.isArray(channel) ||
    typeof (channel as Record<string, unknown>).id !== 'string' ||
    (channel as Record<string, unknown>).id === '' ||
    (channel as Record<string, unknown>).type !== 'D' ||
    typeof (channel as Record<string, unknown>).name !== 'string'
  ) {
    throw new MattermostMutationOutcomeUnknownError()
  }

  const directChannel = channel as Channel
  if (getOtherUserIdFromDMChannel(directChannel, myUserId) !== otherUserId) {
    throw new MattermostMutationOutcomeUnknownError()
  }
  return directChannel
}

export async function getChannel(channelId: string): Promise<Channel> {
  const client = getClient()
  return client.get<Channel>(`/channels/${encodeURIComponent(channelId)}`)
}

export function normalizeChannelName(channelName: string): string {
  return channelName.replace(/^#/, '')
}

export async function getMyTeams(userId?: string): Promise<Team[]> {
  const resolvedUserId = userId ?? (await getMe()).id
  const client = getClient()
  return client.get<Team[]>(`/users/${encodeURIComponent(resolvedUserId)}/teams`)
}

export async function getChannelByName(teamId: string, channelName: string): Promise<Channel> {
  const client = getClient()
  const name = normalizeChannelName(channelName)
  return client.get<Channel>(
    `/teams/${encodeURIComponent(teamId)}/channels/name/${encodeURIComponent(name)}`,
  )
}

export function resolveTeamIdFromList(teams: unknown, teamName?: string): string {
  const safeLabel = (value: string) =>
    sanitizeTerminalLabel(preprocess(value, { redact: false }).text)
  const canonical = normalizeCanonicalTeams(teams)
  if (canonical.length === 0) {
    throw new Error('You are not a member of any teams.')
  }

  if (teamName) {
    const team = canonical.find((t) => t.name === teamName || t.displayName === teamName)
    if (!team) {
      throw new Error(
        `Team "${safeLabel(teamName)}" not found. Your teams: ${canonical
          .map((team) => safeLabel(team.name))
          .join(', ')}`,
      )
    }
    return team.id
  }

  if (canonical.length === 1) {
    const [team] = canonical
    if (!team) throw new Error('You are not a member of any teams.')
    return team.id
  }

  throw new Error(
    `You belong to multiple teams. Use --team to specify:\n` +
      canonical
        .map((team) => `  ${safeLabel(team.name)} (${safeLabel(team.displayName)})`)
        .join('\n'),
  )
}

export async function resolveTeamId(teamName?: string): Promise<string> {
  return resolveTeamIdFromList(await getMyTeams(), teamName)
}

export async function getTeamChannelMembers(teamId: string): Promise<ChannelMember[]> {
  const me = await getMe()
  const client = getClient()
  return client.get<ChannelMember[]>(
    `/users/${encodeURIComponent(me.id)}/teams/${encodeURIComponent(teamId)}/channels/members`,
  )
}

export async function getChannelMember(channelId: string): Promise<ChannelMember> {
  const me = await getMe()
  const client = getClient()
  return client.get<ChannelMember>(
    `/channels/${encodeURIComponent(channelId)}/members/${encodeURIComponent(me.id)}`,
  )
}

// Extract the other user's ID from a DM channel name
// DM channel names are formatted as "{userId1}__{userId2}" sorted alphabetically
export function getOtherUserIdFromDMChannel(channel: Channel, myUserId: string): string | null {
  if (channel.type !== 'D') return null

  const parts = channel.name.split('__')
  if (parts.length !== 2) return null
  const [left, right] = parts
  if (!left || !right) return null

  if (left === myUserId && right === myUserId) return myUserId
  if (left === myUserId) return right
  if (right === myUserId) return left
  return null
}
