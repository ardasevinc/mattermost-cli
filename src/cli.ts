// CLI command handlers

import type { CanonicalTeam } from './api'
import {
  buildPostPermalink,
  connectWebSocket,
  createDirectChannel,
  createPost,
  fetchUsers,
  getAllChannelPosts,
  getCachedUser,
  getChannel,
  getChannelByName,
  getChannelMember,
  getDMChannelByUsername,
  getMe,
  getMyChannels,
  getMyDMChannels,
  getMyGroupDMChannels,
  getMyTeams,
  getOtherUserIdFromDMChannel,
  getPostThread,
  getTeamChannelMembers,
  getUser,
  getUserByUsername,
  getUsersByIds,
  initClient,
  MattermostAPIError,
  MattermostMutationOutcomeUnknownError,
  normalizeCanonicalTeams,
  normalizeChannelName,
  parseDuration,
  resolveTeamId,
  searchPosts,
  takeMostRecentPosts,
} from './api'
import { decodeChannelHistoryCursor, encodeChannelHistoryCursor } from './cursor'
import {
  formatJSON,
  formatMarkdown,
  formatPretty,
  formatWatchEvent,
  formatWatchJSON,
} from './formatters'
import { normalizePosts, postUserIds, preprocess, sanitizeTerminalLabel } from './preprocessing'
import type {
  Channel,
  ChannelMember,
  ChannelOptions,
  ChannelTypeFilter,
  CLIOptions,
  DMsOptions,
  GroupDMsOptions,
  IdentityOptions,
  MentionOptions,
  MessageOutput,
  Post,
  ProcessedChannel,
  Redaction,
  RetrievalMetadata,
  SearchOptions,
  SendDirectMessageOptions,
  SendGroupMessageOptions,
  UnreadOptions,
  UsersOptions,
  WatchEvent,
} from './types'
import {
  calculateUnreadMetrics,
  formatDate,
  formatRelativeTime,
  groupIntoThreads,
  sortUnreadEntries,
} from './utils'

interface ChannelListItem {
  id: string
  type: ProcessedChannel['type']
  name: string
  displayName?: string
  team: (Pick<CanonicalTeam, 'id' | 'name'> & { displayName?: string }) | null
  lastPost: string | null
  messageCount: number
}

const INCOMPLETE_EMPTY_RETRIEVAL_ERROR =
  'Message retrieval was incomplete, so an empty result cannot be confirmed.'

function requireConfirmedEmpty(selectedCount: number, queryTruncated: boolean | null): void {
  if (selectedCount === 0 && queryTruncated === null) {
    throw new Error(INCOMPLETE_EMPTY_RETRIEVAL_ERROR)
  }
}

interface UnreadSummaryItem {
  channel: Channel
  processedChannel: ProcessedChannel
  unreadCount: number
  mentionCount: number
  lastViewedAt: number
}

export interface SendReceipt {
  status: 'dry_run' | 'sent'
  destination: {
    type: 'dm' | 'group'
    label: string
    channelId: string | null
    willCreate: boolean
  }
  post?: {
    id: string
    createAt: string
    pendingPostId: string
    permalink: string
  }
}

export class MattermostDeliveryConfirmedError extends Error {
  constructor() {
    super(
      'Mattermost confirmed delivery, but the local receipt could not be written. Do not retry.',
    )
    this.name = 'MattermostDeliveryConfirmedError'
  }
}

export interface WhoAmIOutput {
  id: string
  username: string
  displayName?: string
  nickname?: string
  roles: string[]
}

export interface TeamOutput {
  id: string
  name: string
  displayName?: string
  type: 'open' | 'invite_only'
}

export interface UserDirectoryOutput {
  id: string
  username: string
  displayName?: string
  nickname?: string
}

export function resolveUsersTeamId(teams: unknown, requested: string, redact = true): string {
  const canonical = normalizeCanonicalTeams(teams)
  const match = canonical.find((team) => team.name === requested || team.displayName === requested)
  if (match) return match.id

  const safeRequested = safeString(requested, redact)
  const available = canonical.map((team) => safeString(team.name, redact)).join(', ')
  throw new Error(
    `Team "${safeRequested}" not found.${available ? ` Your teams: ${available}` : ''}`,
  )
}

export function normalizeUsers(users: unknown, redact = true): UserDirectoryOutput[] {
  if (!Array.isArray(users)) throw new Error('Invalid users response.')
  return users
    .map((user) => {
      if (!isRecord(user)) throw new Error('Invalid users response.')
      const id = requiredString(user.id, 'Invalid users response.')
      const username = requiredString(user.username, 'Invalid users response.')
      const firstName = safeString(user.first_name, redact)
      const lastName = safeString(user.last_name, redact)
      const displayName = [firstName, lastName].filter(Boolean).join(' ')
      const nickname = safeString(user.nickname, redact)
      return {
        id: safeString(id, redact),
        username: safeString(username, redact),
        ...(displayName ? { displayName } : {}),
        ...(nickname ? { nickname } : {}),
      }
    })
    .sort((a, b) => {
      if (a.username !== b.username) return a.username < b.username ? -1 : 1
      if (a.id === b.id) return 0
      return a.id < b.id ? -1 : 1
    })
}

function safeString(value: unknown, redact: boolean): string {
  if (typeof value !== 'string') return ''
  return sanitizeTerminalLabel(preprocess(value, { redact }).text)
}

function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === 'object' && value !== null && !Array.isArray(value)
}

function requiredString(value: unknown, errorMessage: string): string {
  if (typeof value !== 'string' || value.length === 0) throw new Error(errorMessage)
  return value
}

export function normalizeWhoAmI(user: unknown, redact = true): WhoAmIOutput {
  if (!isRecord(user)) throw new Error('Invalid identity response.')
  const raw = user
  const id = requiredString(raw.id, 'Invalid identity response.')
  const username = requiredString(raw.username, 'Invalid identity response.')
  const firstName = safeString(raw.first_name, redact)
  const lastName = safeString(raw.last_name, redact)
  const displayName = [firstName, lastName].filter(Boolean).join(' ')
  const nickname = safeString(raw.nickname, redact)
  const roles =
    typeof raw.roles === 'string'
      ? raw.roles
          .split(/\s+/)
          .map((role) => safeString(role, redact))
          .filter(Boolean)
      : []

  return {
    id: safeString(id, redact),
    username: safeString(username, redact),
    ...(displayName ? { displayName } : {}),
    ...(nickname ? { nickname } : {}),
    roles,
  }
}

export function normalizeTeams(teams: unknown, redact = true): TeamOutput[] {
  if (!Array.isArray(teams)) throw new Error('Invalid teams response.')

  return teams
    .map((team) => {
      if (!isRecord(team)) throw new Error('Invalid teams response.')
      const id = requiredString(team.id, 'Invalid teams response.')
      const name = requiredString(team.name, 'Invalid teams response.')
      if (team.type !== 'O' && team.type !== 'I') throw new Error('Invalid teams response.')

      const raw = team
      const displayName = safeString(raw.display_name, redact)
      return {
        id: safeString(id, redact),
        name: safeString(name, redact),
        ...(displayName ? { displayName } : {}),
        type: raw.type === 'O' ? ('open' as const) : ('invite_only' as const),
      }
    })
    .sort((a, b) => {
      if (a.name !== b.name) return a.name < b.name ? -1 : 1
      if (a.id === b.id) return 0
      return a.id < b.id ? -1 : 1
    })
}

export async function showWhoAmI(options: IdentityOptions): Promise<void> {
  initClient(options.url, options.token)
  const output = normalizeWhoAmI(await getMe(), options.redact)

  if (options.json) {
    console.log(JSON.stringify(output, null, 2))
    return
  }

  const labels = [output.displayName, output.nickname ? `aka ${output.nickname}` : undefined]
    .filter(Boolean)
    .join(', ')
  console.log(`@${output.username}${labels ? ` (${labels})` : ''} [${output.id}]`)
  console.log(`Roles: ${output.roles.length > 0 ? output.roles.join(', ') : 'none'}`)
}

export async function listTeams(options: IdentityOptions): Promise<void> {
  initClient(options.url, options.token)
  const me = await getMe()
  normalizeWhoAmI(me, options.redact)
  if (!isRecord(me)) throw new Error('Invalid identity response.')
  const userId = requiredString(me.id, 'Invalid identity response.')
  const output = normalizeTeams(await getMyTeams(userId), options.redact)

  if (options.json) {
    console.log(JSON.stringify(output, null, 2))
    return
  }

  if (output.length === 0) {
    console.log('No teams found.')
    return
  }
  for (const team of output) {
    const display =
      team.displayName && team.displayName !== team.name ? ` (${team.displayName})` : ''
    console.log(`${team.name}${display} [${team.id}] ${team.type}`)
  }
}

export async function listUsers(options: UsersOptions): Promise<void> {
  initClient(options.url, options.token)
  let teamId: string | undefined
  if (options.team) {
    const me = await getMe()
    if (!isRecord(me)) throw new Error('Invalid identity response.')
    const userId = requiredString(me.id, 'Invalid identity response.')
    teamId = resolveUsersTeamId(await getMyTeams(userId), options.team, options.redact)
  }

  const query = options.query?.trim() || undefined
  const result = await fetchUsers({ query, teamId, limit: options.limit })
  const users = normalizeUsers(result.users, options.redact).slice(0, options.limit)
  const retrieval = {
    selectedCount: users.length,
    requestedLimit: options.limit,
    query: query ? safeString(query, options.redact) : null,
    teamId: teamId ? safeString(teamId, options.redact) : null,
    truncated: result.truncated,
  }

  if (options.json) {
    console.log(JSON.stringify({ users, retrieval }, null, 2))
    return
  }
  if (users.length === 0) {
    console.log('No users found.')
    return
  }
  for (const user of users) {
    const labels = [user.displayName, user.nickname ? `aka ${user.nickname}` : undefined]
      .filter(Boolean)
      .join(', ')
    console.log(`@${user.username}${labels ? ` (${labels})` : ''} [${user.id}]`)
  }
  const coverage =
    result.truncated === true ? 'truncated' : result.truncated === false ? 'complete' : 'unknown'
  console.log(`Showing ${users.length} of up to ${options.limit} users (coverage: ${coverage}).`)
}

export class BoundedPostIdSet {
  private readonly ids = new Set<string>()
  private readonly order: string[] = []

  constructor(private readonly limit = 1000) {}

  add(id: string): boolean {
    if (this.ids.has(id)) return false
    this.ids.add(id)
    this.order.push(id)
    if (this.order.length > this.limit) {
      const expired = this.order.shift()
      if (expired) this.ids.delete(expired)
    }
    return true
  }
}

export function mergeTruncation(
  states: Array<boolean | null>,
  candidateCount: number,
  limit: number,
): boolean | null {
  if (candidateCount > limit || states.includes(true)) return true
  if (states.includes(null)) return null
  return false
}

export function createWatchPostHandler(
  options: Pick<CLIOptions, 'json' | 'color' | 'redact'>,
  write: (line: string) => void = console.log,
): (post: Post, channelName: string, senderName: string) => void {
  const seenPostIds = new BoundedPostIdSet()

  return (post, channelName, senderName) => {
    if (!seenPostIds.add(post.id)) return
    const redactions: Redaction[] = []
    const clean = (value: string, field: string, oneLine = true): string => {
      const result = preprocess(value, { redact: options.redact })
      redactions.push(...result.redactions.map((item) => ({ ...item, field })))
      return oneLine ? result.text.replace(/\n/g, '\\n').replace(/\t/g, '\\t') : result.text
    }
    const username = clean(
      getCachedUser(post.user_id)?.username || senderName || 'unknown',
      'watch.sender',
    )
    const text = clean(post.message, 'watch.message', false)
    const event: WatchEvent = {
      type: 'posted',
      postId: clean(post.id, 'watch.postId'),
      channelId: clean(post.channel_id, 'watch.channelId'),
      channelName: clean(channelName, 'watch.channelName'),
      sender: username,
      senderId: clean(post.user_id, 'watch.senderId'),
      message: text,
      timestamp: new Date(post.create_at).toISOString(),
      rootId: post.root_id ? clean(post.root_id, 'watch.rootId') : undefined,
      fileIds: arrayStringValues(post.file_ids).map((id) => clean(id, 'watch.fileId')),
      redactions,
    }
    write(
      options.json
        ? formatWatchJSON(event)
        : formatWatchEvent(event, options.color && Boolean(process.stdout.isTTY)),
    )
  }
}

function arrayStringValues(value: unknown): string[] {
  return Array.isArray(value)
    ? value.filter((item): item is string => typeof item === 'string')
    : []
}

function nonNegativeInteger(value: unknown): number {
  return typeof value === 'number' && Number.isSafeInteger(value) && value >= 0 ? value : 0
}

function channelTypeLabel(type: Channel['type']): Exclude<ProcessedChannel['type'], 'unknown'> {
  switch (type) {
    case 'O':
      return 'public'
    case 'P':
      return 'private'
    case 'D':
      return 'dm'
    case 'G':
      return 'group'
    default:
      throw new Error('Mattermost returned an unknown channel type.')
  }
}

function channelLabel(channel: ProcessedChannel): string {
  if (channel.type === 'unknown') return `Unknown channel (${channel.id})`
  if (channel.type === 'dm' || channel.type === 'group') return channel.name
  const display = channel.displayName ? ` (${channel.displayName})` : ''
  return `#${channel.name}${display}`
}

function presentOneLine(value: string, redact: boolean): string {
  return sanitizeTerminalLabel(preprocess(value, { redact }).text)
}

export function hasLiteralMention(message: string, terms: string[]): boolean {
  const lowerMessage = message.toLowerCase()
  return terms.some((term) => {
    const literal = term.toLowerCase()
    if (literal.length === 0) return false
    const isAlias = !literal.startsWith('@')

    let index = lowerMessage.indexOf(literal)
    while (index !== -1) {
      const previous = lowerMessage[index - 1]
      const next = lowerMessage[index + literal.length]
      const isBoundaryCharacter = (character: string | undefined) =>
        character !== undefined &&
        (isAlias ? /[\p{L}\p{M}\p{N}]/u.test(character) : /[a-z0-9._-]/i.test(character))
      if (!isBoundaryCharacter(previous) && !isBoundaryCharacter(next)) return true
      index = lowerMessage.indexOf(literal, index + 1)
    }
    return false
  })
}

export function isExactMentionPost(post: Post, term: string, since?: number): boolean {
  return (
    post.delete_at === 0 &&
    (since === undefined || post.create_at >= since) &&
    hasLiteralMention(post.message, [term.startsWith('@') ? term : term.slice(1, -1)])
  )
}

export function mentionSearchAfterDate(since: number): string {
  return new Date(since - 24 * 60 * 60 * 1000).toISOString().slice(0, 10)
}

function groupPostsByChannel(posts: Post[]): Map<string, Post[]> {
  const grouped = new Map<string, Post[]>()

  for (const post of posts) {
    const list = grouped.get(post.channel_id)
    if (list) {
      list.push(post)
    } else {
      grouped.set(post.channel_id, [post])
    }
  }

  return grouped
}

function presentVisibleThreads(
  value: RetrievalMetadata['visibleThreads'],
  redact: boolean,
  redactions: Redaction[],
): RetrievalMetadata['visibleThreads'] {
  return {
    ...value,
    failedRootIds: value.failedRootIds.map((id) => {
      const result = preprocess(id, { redact })
      redactions.push(
        ...result.redactions.map((item) => ({ ...item, field: 'retrieval.failedRootId' })),
      )
      return result.text.replace(/\n/g, '\\n').replace(/\t/g, '\\t')
    }),
  }
}

async function buildProcessedChannel(
  channel: Channel,
  myUserId: string,
  redact = true,
  redactions: Redaction[] = [],
): Promise<ProcessedChannel> {
  requireRawChannelShape(channel, channel.id)
  const type = channelTypeLabel(channel.type)
  const clean = (value: string, field: string): string => {
    const result = preprocess(value, { redact })
    redactions.push(...result.redactions.map((item) => ({ ...item, field })))
    return result.text.replace(/\n/g, '\\n').replace(/\t/g, '\\t')
  }

  if (type === 'dm') {
    const otherUserId = getOtherUserIdFromDMChannel(channel, myUserId)
    if (!otherUserId) {
      throw new Error('Mattermost returned an invalid channel response.')
    }

    const otherUser = await getUser(otherUserId)
    return {
      id: clean(channel.id, 'channel.id'),
      type: 'dm',
      name: `@${clean(otherUser.username, 'channel.dmUsername')}`,
      metadataStatus: 'resolved',
    }
  }

  if (type === 'group') {
    return {
      id: clean(channel.id, 'channel.id'),
      type,
      name: clean(channel.display_name || channel.name, 'channel.displayName'),
      metadataStatus: 'resolved',
    }
  }

  return {
    id: clean(channel.id, 'channel.id'),
    type,
    name: clean(channel.name, 'channel.name'),
    displayName: channel.display_name
      ? clean(channel.display_name, 'channel.displayName')
      : undefined,
    metadataStatus: 'resolved',
  }
}

function isRequiredRawChannelShape(value: unknown, expectedId?: string): value is Channel {
  if (typeof value !== 'object' || value === null) return false
  const channel = value as Record<string, unknown>
  if (
    typeof channel.id !== 'string' ||
    channel.id.trim().length === 0 ||
    (expectedId !== undefined && channel.id !== expectedId) ||
    (channel.type !== 'O' &&
      channel.type !== 'P' &&
      channel.type !== 'D' &&
      channel.type !== 'G') ||
    typeof channel.name !== 'string' ||
    typeof channel.display_name !== 'string' ||
    typeof channel.team_id !== 'string'
  ) {
    return false
  }

  if (channel.type === 'O' || channel.type === 'P') {
    return channel.name.trim().length > 0 && channel.team_id.trim().length > 0
  }
  if (channel.team_id !== '') return false
  if (channel.type === 'G') return channel.name.trim().length > 0

  const userIds = channel.name.split('__')
  return userIds.length === 2 && userIds.every((id) => id.trim().length > 0)
}

function requireRawChannelShape(value: unknown, expectedId?: string): Channel {
  if (!isRequiredRawChannelShape(value, expectedId)) {
    throw new Error('Mattermost returned an invalid channel response.')
  }
  return value
}

function requireConversationChannelContext(channel: Channel, myUserId: string): Channel {
  requireRawChannelShape(channel, channel.id)
  if (channel.type === 'D' && !getOtherUserIdFromDMChannel(channel, myUserId)) {
    throw new Error('Mattermost returned an invalid channel response.')
  }
  return channel
}

function validateAndDedupeConversationChannels(channels: Channel[], myUserId: string): Channel[] {
  const validated = channels.map((channel) => requireConversationChannelContext(channel, myUserId))
  return [...new Map(validated.map((channel) => [channel.id, channel])).values()]
}

function unavailableProcessedChannel(
  channelId: string,
  redact: boolean,
  redactions: Redaction[],
): ProcessedChannel {
  const result = preprocess(channelId, { redact })
  redactions.push(...result.redactions.map((item) => ({ ...item, field: 'channel.id' })))
  const safeId = sanitizeTerminalLabel(result.text)
  console.error(`Warning: Channel metadata is unavailable for ${safeId || 'an unknown channel'}.`)
  return {
    id: safeId,
    type: 'unknown',
    name: 'unknown',
    metadataStatus: 'unavailable',
  }
}

async function resolveProcessedChannel(
  channelId: string,
  myUserId: string,
  redact: boolean,
  knownChannel?: Channel,
): Promise<{ channel: ProcessedChannel; redactions: Redaction[] }> {
  const redactions: Redaction[] = []
  let channel: Channel

  if (knownChannel) {
    channel = requireRawChannelShape(knownChannel, channelId)
  } else {
    if (!channelId) {
      return {
        channel: unavailableProcessedChannel(channelId, redact, redactions),
        redactions,
      }
    }
    try {
      const candidate: unknown = await getChannel(channelId)
      if (!isRequiredRawChannelShape(candidate, channelId)) {
        return {
          channel: unavailableProcessedChannel(channelId, redact, redactions),
          redactions,
        }
      }
      channel = candidate
    } catch (error) {
      if (error instanceof MattermostAPIError && error.status === 401) throw error
      return {
        channel: unavailableProcessedChannel(channelId, redact, redactions),
        redactions,
      }
    }
  }

  if (!knownChannel && channel.type === 'D' && !getOtherUserIdFromDMChannel(channel, myUserId)) {
    return {
      channel: unavailableProcessedChannel(channelId, redact, redactions),
      redactions,
    }
  }

  return {
    channel: await buildProcessedChannel(channel, myUserId, redact, redactions),
    redactions,
  }
}

async function buildOutputsFromPosts(
  posts: Post[],
  myUserId: string,
  options: CLIOptions,
  retrieval: Pick<
    RetrievalMetadata['selection'],
    'source' | 'requestedLimit' | 'since' | 'queryTruncated'
  > &
    Partial<Pick<RetrievalMetadata['selection'], 'inputCursor' | 'nextCursor'>>,
  knownChannels: Map<string, Channel> = new Map(),
): Promise<MessageOutput[]> {
  const grouped = groupPostsByChannel(posts)
  const outputs: MessageOutput[] = []
  const hydratedGroups: Array<{
    channelId: string
    seedPosts: Post[]
    channelPosts: Post[]
    visibleThreads: RetrievalMetadata['visibleThreads']
  }> = []

  for (const [channelId, seedPosts] of grouped) {
    const { posts: channelPosts, visibleThreads } = await hydrateVisibleThreads(
      seedPosts,
      options.threads,
      options.redact,
    )
    hydratedGroups.push({ channelId, seedPosts, channelPosts, visibleThreads })
  }

  const userIds = [
    ...new Set(hydratedGroups.flatMap(({ channelPosts }) => postUserIds(channelPosts))),
  ]
  if (userIds.length > 0) await getUsersByIds(userIds)
  const usersById = new Map(
    userIds.flatMap((id) => {
      const user = getCachedUser(id)
      return user ? ([[id, user]] as const) : []
    }),
  )

  for (const { channelId, seedPosts, channelPosts, visibleThreads } of hydratedGroups) {
    const { channel: processedChannel, redactions: channelRedactions } =
      await resolveProcessedChannel(
        channelId,
        myUserId,
        options.redact,
        knownChannels.get(channelId),
      )
    const { messages, redactions } = normalizePosts(
      channelPosts,
      usersById,
      myUserId,
      options.url,
      buildPostPermalink,
      options.redact,
    )
    const presentedVisibleThreads = presentVisibleThreads(
      visibleThreads,
      options.redact,
      redactions,
    )

    outputs.push({
      channel: processedChannel,
      messages: options.threads ? groupIntoThreads(messages) : messages,
      redactions: [...channelRedactions, ...redactions],
      retrieval: retrievalMetadata(
        retrieval,
        seedPosts.length,
        presentedVisibleThreads,
        channelPosts.length,
      ),
    })
  }

  return outputs
}

export async function hydrateVisibleThreads(
  seedPosts: Post[],
  requested: boolean,
  redact = true,
): Promise<{ posts: Post[]; visibleThreads: RetrievalMetadata['visibleThreads'] }> {
  if (!requested) {
    return {
      posts: seedPosts,
      visibleThreads: { status: 'not_requested', hydratedRootCount: 0, failedRootIds: [] },
    }
  }

  const postsById = new Map(seedPosts.map((post) => [post.id, post]))
  const rootIds = [
    ...new Set(
      seedPosts.flatMap((post) =>
        post.root_id ? [post.root_id] : post.reply_count > 0 ? [post.id] : [],
      ),
    ),
  ]
  if (rootIds.length === 0) {
    return {
      posts: seedPosts,
      visibleThreads: { status: 'complete', hydratedRootCount: 0, failedRootIds: [] },
    }
  }

  const failedRootIdSet = new Set<string>()
  let hydratedRootCount = 0
  let nextIndex = 0
  const hydrateRoot = async (rootId: string) => {
    const existingRoot = postsById.get(rootId)
    const loadedReplyCount = seedPosts.filter((post) => post.root_id === rootId).length
    if (existingRoot && loadedReplyCount >= existingRoot.reply_count) {
      hydratedRootCount += 1
      return
    }

    try {
      const result = await getPostThread(rootId)
      for (const post of result.posts) postsById.set(post.id, post)
      const root = result.posts.find((post) => post.id === rootId && !post.root_id)
      if (result.truncated === false && root) {
        hydratedRootCount += 1
      } else {
        failedRootIdSet.add(rootId)
        console.error(
          `Warning: Thread ${preprocess(rootId, { redact }).text.replace(/\n/g, '\\n').replace(/\t/g, '\\t')} could only be partially hydrated.`,
        )
      }
    } catch {
      failedRootIdSet.add(rootId)
      console.error(
        `Warning: Could not hydrate thread ${preprocess(rootId, { redact }).text.replace(/\n/g, '\\n').replace(/\t/g, '\\t')}.`,
      )
    }
  }
  const worker = async () => {
    while (true) {
      const index = nextIndex
      nextIndex += 1
      const rootId = rootIds[index]
      if (rootId === undefined) return
      await hydrateRoot(rootId)
    }
  }
  await Promise.all(Array.from({ length: Math.min(4, rootIds.length) }, () => worker()))
  const failedRootIds = rootIds.filter((rootId) => failedRootIdSet.has(rootId))

  return {
    posts: [...postsById.values()],
    visibleThreads: {
      status: failedRootIds.length === 0 ? 'complete' : 'partial',
      hydratedRootCount,
      failedRootIds,
    },
  }
}

function retrievalMetadata(
  selection: Pick<
    RetrievalMetadata['selection'],
    'source' | 'requestedLimit' | 'since' | 'queryTruncated'
  > &
    Partial<Pick<RetrievalMetadata['selection'], 'inputCursor' | 'nextCursor'>>,
  selectedCount: number,
  visibleThreads: RetrievalMetadata['visibleThreads'] = {
    status: 'not_requested',
    hydratedRootCount: 0,
    failedRootIds: [],
  },
  visiblePostCount = selectedCount,
): RetrievalMetadata {
  return {
    selection: {
      ...selection,
      selectedCount,
      inputCursor: selection.inputCursor ?? null,
      nextCursor: selection.nextCursor ?? null,
    },
    visibleThreads,
    visiblePostCount,
    deletedPostsIncluded: false,
  }
}

export type OutputMode = 'json' | 'pretty' | 'markdown'

export function selectOutputMode(json: boolean, isTTY: boolean): OutputMode {
  if (json) return 'json'
  return isTTY ? 'pretty' : 'markdown'
}

function formatOutput(outputs: MessageOutput[], options: CLIOptions): void {
  if (outputs.length === 0 && !options.json) {
    console.log('No messages found.')
    return
  }

  const mode = selectOutputMode(options.json, Boolean(process.stdout.isTTY))

  if (mode === 'json') {
    console.log(formatJSON(outputs))
  } else if (mode === 'pretty') {
    console.log(formatPretty(outputs, { color: options.color, relative: options.relative }))
  } else {
    console.log(formatMarkdown(outputs, { relative: options.relative }))
  }
}

function printRedactionWarning(enabled: boolean): void {
  if (!enabled) {
    console.error('Warning: Secret redaction is disabled. Output may contain secrets.')
  }
}

function buildChannelListItem(
  channel: ProcessedChannel,
  rawChannel: Channel,
  team: CanonicalTeam | null,
  redact: boolean,
): ChannelListItem {
  const lastPostAt = finiteTimestamp(rawChannel.last_post_at)
  const presentedTeam = team
    ? {
        id: safeString(team.id, redact),
        name: safeString(team.name, redact),
        ...(team.displayName ? { displayName: safeString(team.displayName, redact) } : {}),
      }
    : null
  return {
    id: channel.id,
    type: channel.type,
    name: channel.name,
    displayName: channel.displayName,
    team: presentedTeam,
    lastPost: lastPostAt > 0 ? new Date(lastPostAt).toISOString() : null,
    messageCount: nonNegativeInteger(rawChannel.total_msg_count),
  }
}

function finiteTimestamp(value: unknown): number {
  return typeof value === 'number' &&
    Number.isFinite(value) &&
    Number.isFinite(new Date(value).getTime())
    ? value
    : 0
}

export async function listChannels(options: {
  url: string
  token: string
  json: boolean
  color: boolean
  relative: boolean
  redact: boolean
  typeFilter: ChannelTypeFilter
}): Promise<void> {
  initClient(options.url, options.token)
  const validTypeFilters: readonly string[] = ['all', 'dm', 'public', 'private', 'group']
  if (!validTypeFilters.includes(options.typeFilter)) {
    throw new Error(
      `Invalid channel type "${presentOneLine(String(options.typeFilter), false)}". Expected one of: ${validTypeFilters.join(', ')}.`,
    )
  }

  const me = await getMe()
  let channels = [
    ...new Map((await getMyChannels(me.id)).map((channel) => [channel.id, channel])).values(),
  ]

  if (options.typeFilter !== 'all') {
    const typeMap: Record<Exclude<ChannelTypeFilter, 'all'>, Channel['type']> = {
      dm: 'D',
      public: 'O',
      private: 'P',
      group: 'G',
    }
    const filterType = typeMap[options.typeFilter]
    channels = channels.filter((ch) => ch.type === filterType)
  }

  let teamById = new Map<string, CanonicalTeam>()
  if (channels.some((channel) => channel.type === 'O' || channel.type === 'P')) {
    teamById = new Map(
      normalizeCanonicalTeams(await getMyTeams(me.id)).map((team) => [team.id, team]),
    )
    for (const channel of channels) {
      if (
        (channel.type === 'O' || channel.type === 'P') &&
        (typeof channel.team_id !== 'string' ||
          channel.team_id.length === 0 ||
          !teamById.has(channel.team_id))
      ) {
        throw new Error('Invalid channels response.')
      }
    }
  }

  const dmChannels = channels.filter((ch) => ch.type === 'D')
  const otherUserIds = dmChannels
    .map((ch) => getOtherUserIdFromDMChannel(ch, me.id))
    .filter((id): id is string => !!id)

  if (otherUserIds.length > 0) {
    await getUsersByIds(otherUserIds)
  }

  const output = await Promise.all(
    channels.map(async (channel) => {
      const processed = await buildProcessedChannel(channel, me.id, options.redact)
      const team =
        channel.type === 'O' || channel.type === 'P' ? teamById.get(channel.team_id) : null
      return buildChannelListItem(processed, channel, team ?? null, options.redact)
    }),
  )

  output.sort((a, b) => {
    if (!a.lastPost) return 1
    if (!b.lastPost) return -1
    return new Date(b.lastPost).getTime() - new Date(a.lastPost).getTime()
  })

  if (options.json) {
    console.log(JSON.stringify(output, null, 2))
    return
  }

  const grouped = Map.groupBy(output, (channel) => channel.type)
  const typeLabels: Record<string, string> = {
    public: 'Public Channels',
    private: 'Private Channels',
    dm: 'Direct Messages',
    group: 'Group Messages',
  }
  const typeOrder = ['public', 'private', 'group', 'dm'] as const

  for (const type of typeOrder) {
    const items = grouped.get(type)
    if (!items || items.length === 0) continue

    console.log(`\n${typeLabels[type]}:\n`)
    for (const channel of items) {
      let lastPost = 'never'
      if (channel.lastPost) {
        const date = new Date(channel.lastPost)
        lastPost = options.relative
          ? formatRelativeTime(date)
          : formatDate(date, { includeYear: true })
      }

      const label =
        channel.type === 'dm' || channel.type === 'group'
          ? channel.name
          : `${channel.team?.name}/#${channel.name}`
      const display = channel.displayName ? ` (${channel.displayName})` : ''
      console.log(
        `  ${label.padEnd(25)}${display ? display.padEnd(25) : ''.padEnd(25)} [${channel.id}] ${channel.messageCount} msgs, last: ${lastPost}`,
      )
    }
  }

  console.log(`\nTotal: ${output.length} channels`)
}

function renderSendReceipt(receipt: SendReceipt, json: boolean): string {
  if (json) {
    return JSON.stringify(receipt, null, 2)
  }
  const destination = receipt.destination.channelId
    ? `${receipt.destination.label} [${receipt.destination.channelId}]`
    : receipt.destination.label
  if (receipt.status === 'dry_run') {
    return receipt.destination.willCreate
      ? `Would create a conversation with ${destination}, then send one message.`
      : `Would send one message to ${destination}.`
  }
  return `Sent one message to ${destination}. Post ${receipt.post?.id}.`
}

function emitSendReceipt(receipt: SendReceipt, json: boolean): void {
  console.log(renderSendReceipt(receipt, json))
}

async function writeConfirmedSendReceipt(receipt: SendReceipt, json: boolean): Promise<void> {
  try {
    const output = `${renderSendReceipt(receipt, json)}\n`
    await new Promise<void>((resolve, reject) => {
      const onError = (error: Error) => {
        cleanup()
        reject(error)
      }
      const cleanup = () => process.stdout.off('error', onError)
      process.stdout.once('error', onError)
      try {
        process.stdout.write(output, (error) => {
          cleanup()
          if (error) reject(error)
          else resolve()
        })
      } catch (error) {
        cleanup()
        reject(error)
      }
    })
  } catch {
    throw new MattermostDeliveryConfirmedError()
  }
}

function safeSendLabel(value: string, redact: boolean): string {
  return safeString(value, redact).replace(/\r?\n/g, '\\n').replace(/\t/g, '\\t')
}

export async function sendDirectMessage(options: SendDirectMessageOptions): Promise<void> {
  if (options.message?.includes(options.token)) {
    throw new Error('Refusing to send the active Mattermost credential.')
  }
  const username = options.username.replace(/^@/, '').trim()
  if (!username) throw new Error('A direct-message username is required.')
  initClient(options.url, options.token)

  const me = await getMe()
  if (typeof me?.id !== 'string' || me.id.trim().length === 0) {
    throw new Error('Mattermost returned an invalid identity response.')
  }
  const recipient = await getUserByUsername(username)
  if (
    typeof recipient?.id !== 'string' ||
    recipient.id.trim().length === 0 ||
    typeof recipient.username !== 'string' ||
    recipient.username.length === 0 ||
    recipient.username.toLowerCase() !== username.toLowerCase()
  ) {
    throw new Error('Mattermost returned an invalid user response.')
  }

  const channels = validateAndDedupeConversationChannels(await getMyDMChannels(me.id), me.id)
  let channel =
    channels.find((candidate) => getOtherUserIdFromDMChannel(candidate, me.id) === recipient.id) ??
    null
  const label = `@${safeSendLabel(recipient.username, options.redact)}`

  if (options.dryRun) {
    emitSendReceipt(
      {
        status: 'dry_run',
        destination: {
          type: 'dm',
          label,
          channelId: channel ? safeSendLabel(channel.id, options.redact) : null,
          willCreate: channel === null,
        },
      },
      options.json,
    )
    return
  }

  if (options.message === undefined) throw new Error('Message content is required.')
  if (!channel) {
    try {
      channel = requireConversationChannelContext(
        await createDirectChannel(me.id, recipient.id),
        me.id,
      )
    } catch (error) {
      if (error instanceof MattermostMutationOutcomeUnknownError) {
        throw new Error(
          'Mattermost did not confirm DM setup. The message was not attempted; run a dry-run before retrying.',
        )
      }
      throw error
    }
  }
  const post = await createPost(channel.id, options.message)
  if (post.userId !== me.id) throw new MattermostMutationOutcomeUnknownError()
  const safePostId = safeSendLabel(post.id, options.redact)
  await writeConfirmedSendReceipt(
    {
      status: 'sent',
      destination: {
        type: 'dm',
        label,
        channelId: safeSendLabel(channel.id, options.redact),
        willCreate: false,
      },
      post: {
        id: safePostId,
        createAt: new Date(post.createAt).toISOString(),
        pendingPostId: safeSendLabel(post.pendingPostId, options.redact),
        permalink: safeSendLabel(buildPostPermalink(options.url, safePostId), options.redact),
      },
    },
    options.json,
  )
}

export async function sendGroupMessage(options: SendGroupMessageOptions): Promise<void> {
  if (options.message?.includes(options.token)) {
    throw new Error('Refusing to send the active Mattermost credential.')
  }
  if (!options.channelId.trim()) throw new Error('A group-DM channel ID is required.')
  initClient(options.url, options.token)
  const channel = requireRawChannelShape(await getChannel(options.channelId), options.channelId)
  if (channel.type !== 'G') {
    throw new Error(`Channel "${presentOneLine(channel.id, options.redact)}" is not a group DM.`)
  }
  const label = safeSendLabel(channel.display_name || channel.name, options.redact)
  const channelId = safeSendLabel(channel.id, options.redact)

  if (options.dryRun) {
    emitSendReceipt(
      {
        status: 'dry_run',
        destination: { type: 'group', label, channelId, willCreate: false },
      },
      options.json,
    )
    return
  }

  if (options.message === undefined) throw new Error('Message content is required.')
  const me = await getMe()
  if (typeof me?.id !== 'string' || me.id.trim().length === 0) {
    throw new Error('Mattermost returned an invalid identity response.')
  }
  const post = await createPost(channel.id, options.message)
  if (post.userId !== me.id) throw new MattermostMutationOutcomeUnknownError()
  const safePostId = safeSendLabel(post.id, options.redact)
  await writeConfirmedSendReceipt(
    {
      status: 'sent',
      destination: { type: 'group', label, channelId, willCreate: false },
      post: {
        id: safePostId,
        createAt: new Date(post.createAt).toISOString(),
        pendingPostId: safeSendLabel(post.pendingPostId, options.redact),
        permalink: safeSendLabel(buildPostPermalink(options.url, safePostId), options.redact),
      },
    },
    options.json,
  )
}

export async function fetchDMs(options: DMsOptions): Promise<void> {
  if (options.cursor !== undefined) decodeChannelHistoryCursor(options.cursor)
  if (options.cursor !== undefined && !options.channel) {
    throw new Error('A cursor requires --channel for direct-message history.')
  }
  if (options.cursor !== undefined && options.user.length > 0) {
    throw new Error('A cursor cannot be combined with --user.')
  }
  if (options.cursor !== undefined && options.sinceExplicit) {
    throw new Error('A cursor cannot be combined with --since.')
  }
  initClient(options.url, options.token)

  let myUserId: string
  let channels: Channel[] = []

  if (options.channel) {
    const channel = requireRawChannelShape(await getChannel(options.channel), options.channel)
    if (channel.type !== 'D') {
      throw new Error(
        `Channel "${presentOneLine(channel.id, options.redact)}" is not a direct-message channel.`,
      )
    }
    channels = [channel]
    myUserId = (await getMe()).id
  } else if (options.user.length > 0) {
    const me = await getMe()
    myUserId = me.id
    const discoveredDMChannels = (await getMyChannels(me.id)).filter(
      (channel) => channel.type === 'D',
    )
    const dmChannels = validateAndDedupeConversationChannels(discoveredDMChannels, me.id)
    for (const username of options.user) {
      try {
        const user = await getUserByUsername(username)
        if (typeof user?.id !== 'string' || user.id.length === 0) {
          throw new Error('Mattermost returned an invalid user response.')
        }
        const channel = dmChannels.find(
          (candidate) => getOtherUserIdFromDMChannel(candidate, me.id) === user.id,
        )
        if (channel) {
          channels.push(channel)
        } else {
          console.error(
            `Warning: No direct-message channel exists with @${presentOneLine(username, false)}.`,
          )
        }
      } catch (error) {
        if (!(error instanceof MattermostAPIError) || error.status !== 404) throw error
        console.error(`Warning: User @${presentOneLine(username, false)} was not found.`)
      }
    }
  } else {
    const me = await getMe()
    myUserId = me.id
    channels = await getMyDMChannels(me.id)
  }

  channels = [...new Map(channels.map((channel) => [channel.id, channel])).values()]

  await fetchConversationChannels(channels, myUserId, options)
}

export async function fetchGroupDMs(options: GroupDMsOptions): Promise<void> {
  if (options.cursor !== undefined) decodeChannelHistoryCursor(options.cursor)
  if (options.cursor !== undefined && !options.channel) {
    throw new Error('A cursor requires --channel for group-DM history.')
  }
  if (options.cursor !== undefined && options.sinceExplicit) {
    throw new Error('A cursor cannot be combined with --since.')
  }
  initClient(options.url, options.token)

  if (options.channel) {
    const channel = requireRawChannelShape(await getChannel(options.channel), options.channel)
    if (channel.type !== 'G') {
      throw new Error(`Channel "${presentOneLine(channel.id, options.redact)}" is not a group DM.`)
    }
    const me = await getMe()
    await fetchConversationChannels([channel], me.id, options)
    return
  }

  const me = await getMe()
  const channels = await getMyGroupDMChannels(me.id)
  await fetchConversationChannels(
    [...new Map(channels.map((channel) => [channel.id, channel])).values()],
    me.id,
    options,
  )
}

async function fetchConversationChannels(
  channels: Channel[],
  myUserId: string,
  options: GroupDMsOptions | DMsOptions,
): Promise<void> {
  channels = validateAndDedupeConversationChannels(channels, myUserId)
  if (channels.length === 0) {
    formatOutput([], options)
    return
  }

  printRedactionWarning(options.redact)

  const cursor =
    options.cursor !== undefined ? decodeChannelHistoryCursor(options.cursor) : undefined
  if (cursor && (channels.length !== 1 || cursor.channelId !== channels[0]?.id)) {
    throw new Error('Cursor does not match the selected channel.')
  }
  const since = cursor
    ? (cursor.since ?? undefined)
    : options.since
      ? parseDuration(options.since)
      : undefined
  const channelPosts = new Map<string, Post[]>()
  const truncationStates: Array<boolean | null> = []
  const allPosts: Post[] = []
  let inputSafeBeforeValid = true

  for (const channel of channels) {
    const result = await getAllChannelPosts(channel.id, {
      limit: options.limit,
      since,
      boundary: cursor?.boundary,
      safeBeforePostId: cursor?.safeBeforePostId,
    })
    const posts = result.posts
    truncationStates.push(result.truncated)
    if (cursor && result.safeBeforeValid === false) inputSafeBeforeValid = false
    if (posts.length === 0) continue

    channelPosts.set(channel.id, posts)
    allPosts.push(...posts)
  }

  if (allPosts.length === 0 && !(cursor && truncationStates.some((state) => state === null))) {
    requireConfirmedEmpty(0, mergeTruncation(truncationStates, 0, options.limit))
    formatOutput([], options)
    return
  }

  // `--limit` is a total output budget across all matched conversation channels.
  const selectedPostIds = new Set(
    takeMostRecentPosts(allPosts, options.limit).map((post) => post.id),
  )
  const queryTruncated = mergeTruncation(truncationStates, allPosts.length, options.limit)
  const selectedPosts = allPosts.filter((post) => selectedPostIds.has(post.id))
  const lastSelected = takeMostRecentPosts(selectedPosts, options.limit).at(-1)
  const safeBeforePostId = lastSelected
    ? ([...selectedPosts].reverse().find((post) => post.create_at > lastSelected.create_at)?.id ??
      (inputSafeBeforeValid ? cursor?.safeBeforePostId : undefined))
    : undefined
  const nextCursor =
    options.channel && lastSelected && queryTruncated !== false
      ? encodeChannelHistoryCursor({
          v: 1,
          scope: 'channel',
          channelId: channels[0]?.id ?? '',
          boundary: { createAt: lastSelected.create_at, id: lastSelected.id },
          since: since ?? null,
          ...(safeBeforePostId === undefined ? {} : { safeBeforePostId }),
        })
      : cursor && selectedPosts.length === 0 && queryTruncated === null
        ? (options.cursor ?? null)
        : null
  if (selectedPosts.length === 0 && cursor && queryTruncated === null) {
    const channel = channels[0]
    if (!channel) throw new Error('Cursor channel is unavailable.')
    const channelRedactions: Redaction[] = []
    const processedChannel = await buildProcessedChannel(
      channel,
      myUserId,
      options.redact,
      channelRedactions,
    )
    formatOutput(
      [
        {
          channel: processedChannel,
          messages: [],
          redactions: channelRedactions,
          retrieval: retrievalMetadata(
            {
              source: 'recent',
              requestedLimit: options.limit,
              since: since === undefined ? null : new Date(since).toISOString(),
              queryTruncated,
              inputCursor: options.cursor ?? null,
              nextCursor,
            },
            0,
            {
              status: options.threads ? 'complete' : 'not_requested',
              hydratedRootCount: 0,
              failedRootIds: [],
            },
            0,
          ),
        },
      ],
      options,
    )
    return
  }
  const outputs = await buildOutputsFromPosts(
    selectedPosts,
    myUserId,
    options,
    {
      source: 'recent',
      requestedLimit: options.limit,
      since: since === undefined ? null : new Date(since).toISOString(),
      queryTruncated,
      inputCursor: options.cursor ?? null,
      nextCursor,
    },
    new Map(channels.map((channel) => [channel.id, channel])),
  )

  formatOutput(outputs, options)
}

export async function fetchChannel(options: ChannelOptions): Promise<void> {
  const cursor =
    options.cursor !== undefined ? decodeChannelHistoryCursor(options.cursor) : undefined
  if (options.cursor !== undefined && options.sinceExplicit) {
    throw new Error('A cursor cannot be combined with --since.')
  }
  initClient(options.url, options.token)

  const me = await getMe()
  const teamId = await resolveTeamId(options.team)
  const channel = requireRawChannelShape(await getChannelByName(teamId, options.channel))
  if (
    (channel.type !== 'O' && channel.type !== 'P') ||
    channel.team_id !== teamId ||
    channel.name !== normalizeChannelName(options.channel)
  ) {
    throw new Error('Mattermost returned an invalid channel response.')
  }
  if (cursor && cursor.channelId !== channel.id) {
    throw new Error('Cursor does not match the selected channel.')
  }

  printRedactionWarning(options.redact)

  const since = cursor
    ? (cursor.since ?? undefined)
    : options.since
      ? parseDuration(options.since)
      : undefined
  const result = await getAllChannelPosts(channel.id, {
    limit: options.limit,
    since,
    boundary: cursor?.boundary,
    safeBeforePostId: cursor?.safeBeforePostId,
  })
  const posts = result.posts

  if (posts.length === 0 && !(cursor && result.truncated === null)) {
    requireConfirmedEmpty(0, result.truncated)
    formatOutput([], options)
    return
  }

  if (posts.length === 0 && cursor && result.truncated === null) {
    const channelRedactions: Redaction[] = []
    const processedChannel = await buildProcessedChannel(
      channel,
      me.id,
      options.redact,
      channelRedactions,
    )
    formatOutput(
      [
        {
          channel: processedChannel,
          messages: [],
          redactions: channelRedactions,
          retrieval: retrievalMetadata(
            {
              source: 'recent',
              requestedLimit: options.limit,
              since: since === undefined ? null : new Date(since).toISOString(),
              queryTruncated: null,
              inputCursor: options.cursor ?? null,
              nextCursor: options.cursor ?? null,
            },
            0,
            {
              status: options.threads ? 'complete' : 'not_requested',
              hydratedRootCount: 0,
              failedRootIds: [],
            },
            0,
          ),
        },
      ],
      options,
    )
    return
  }

  const boundaryPost = posts.at(-1)
  const safeBeforePostId = boundaryPost
    ? ([...posts].reverse().find((post) => post.create_at > boundaryPost.create_at)?.id ??
      (result.safeBeforeValid === false ? undefined : cursor?.safeBeforePostId))
    : undefined

  const outputs = await buildOutputsFromPosts(
    posts,
    me.id,
    options,
    {
      source: 'recent',
      requestedLimit: options.limit,
      since: since === undefined ? null : new Date(since).toISOString(),
      queryTruncated: result.truncated,
      inputCursor: options.cursor ?? null,
      nextCursor:
        posts.length > 0 && result.truncated !== false
          ? encodeChannelHistoryCursor({
              v: 1,
              scope: 'channel',
              channelId: channel.id,
              boundary: { createAt: posts.at(-1)?.create_at ?? 0, id: posts.at(-1)?.id ?? '' },
              since: since ?? null,
              ...(safeBeforePostId === undefined ? {} : { safeBeforePostId }),
            })
          : null,
    },
    new Map([[channel.id, channel]]),
  )
  formatOutput(outputs, options)
}

export async function fetchThread(options: CLIOptions & { postId: string }): Promise<void> {
  initClient(options.url, options.token)

  printRedactionWarning(options.redact)

  const me = await getMe()
  const result = await getPostThread(options.postId)
  const posts = result.posts

  if (posts.length === 0) {
    console.error('Thread not found or empty')
    process.exit(1)
  }

  const rootPost = posts.find((post) => !post.root_id)
  const channelPost = rootPost ?? posts[0]
  const missingRootId = rootPost ? undefined : posts.find((post) => post.root_id)?.root_id
  const requestedRootId = rootPost?.id ?? missingRootId ?? options.postId
  const threadComplete = result.truncated === false && rootPost !== undefined
  if (result.truncated !== false || !rootPost) {
    const safeRequestedRootId = preprocess(requestedRootId, { redact: options.redact })
      .text.replace(/\n/g, '\\n')
      .replace(/\t/g, '\\t')
    console.error(`Warning: Thread ${safeRequestedRootId} could only be partially hydrated.`)
  }

  const userIds = postUserIds(posts)
  if (userIds.length > 0) {
    await getUsersByIds(userIds)
  }

  const usersById = new Map(
    userIds.flatMap((id) => {
      const user = getCachedUser(id)
      return user ? ([[id, user]] as const) : []
    }),
  )
  const { messages, redactions } = normalizePosts(
    posts,
    usersById,
    me.id,
    options.url,
    buildPostPermalink,
    options.redact,
  )
  const threadState: RetrievalMetadata['visibleThreads'] = {
    status: threadComplete ? 'complete' : 'partial',
    hydratedRootCount: threadComplete ? 1 : 0,
    failedRootIds: threadComplete ? [] : [requestedRootId],
  }
  const presentedThreadState = presentVisibleThreads(threadState, options.redact, redactions)

  const { channel: processedChannel, redactions: channelRedactions } =
    await resolveProcessedChannel(channelPost?.channel_id ?? '', me.id, options.redact)

  formatOutput(
    [
      {
        channel: processedChannel,
        messages: groupIntoThreads(messages),
        redactions: [...channelRedactions, ...redactions],
        retrieval: retrievalMetadata(
          {
            source: 'thread',
            requestedLimit: null,
            since: null,
            queryTruncated: result.truncated,
          },
          posts.length,
          presentedThreadState,
        ),
      },
    ],
    options,
  )
}

export async function searchMessages(options: SearchOptions): Promise<void> {
  initClient(options.url, options.token)

  const query = options.query.trim()
  if (!query) {
    console.error('Error: Search query cannot be empty')
    process.exit(1)
  }

  printRedactionWarning(options.redact)

  const me = await getMe()
  const teamId = await resolveTeamId(options.team)
  const response = await searchPosts(teamId, query, options.limit)

  const posts = response.order
    .map((id) => response.posts[id])
    .filter((post): post is Post => !!post && post.delete_at === 0)
    .slice(0, options.limit)

  if (posts.length === 0) {
    requireConfirmedEmpty(0, response.truncated)
    formatOutput([], options)
    return
  }

  const outputs = await buildOutputsFromPosts(posts, me.id, options, {
    source: 'search',
    requestedLimit: options.limit,
    since: null,
    queryTruncated: response.truncated,
  })
  formatOutput(outputs, options)
}

export async function fetchMentions(options: MentionOptions): Promise<void> {
  initClient(options.url, options.token)

  printRedactionWarning(options.redact)

  const me = await getMe()
  const teamId = await resolveTeamId(options.team)

  const baseTerms = [`@${me.username}`]
  for (const mentionName of options.mentionNames) {
    if (mentionName.trim().length > 0) {
      baseTerms.push(`"${mentionName.trim()}"`)
    }
  }

  const modifiers: string[] = []
  const since = options.since ? parseDuration(options.since) : undefined
  if (since !== undefined) {
    modifiers.push(`after:${mentionSearchAfterDate(since)}`)
  }
  if (options.channel) {
    modifiers.push(`in:${normalizeChannelName(options.channel)}`)
  }

  const dedupedPosts = new Map<string, Post>()
  const truncationStates: Array<boolean | null> = []

  const searchTerms = [...new Set(baseTerms)]

  for (const term of searchTerms) {
    const searchTerm = [term, ...modifiers].join(' ')
    const response = await searchPosts(teamId, searchTerm, options.limit, (post) =>
      isExactMentionPost(post, term, since),
    )
    truncationStates.push(response.truncated)

    for (const id of response.order) {
      const post = response.posts[id]
      if (post) {
        dedupedPosts.set(post.id, post)
      }
    }
  }

  const posts = takeMostRecentPosts([...dedupedPosts.values()], options.limit)
  const queryTruncated = mergeTruncation(truncationStates, dedupedPosts.size, options.limit)

  if (posts.length === 0) {
    requireConfirmedEmpty(0, queryTruncated)
    formatOutput([], options)
    return
  }

  const outputs = await buildOutputsFromPosts(posts, me.id, options, {
    source: 'mentions',
    requestedLimit: options.limit,
    since: since === undefined ? null : new Date(since).toISOString(),
    queryTruncated,
  })
  formatOutput(outputs, options)
}

export async function showUnread(options: UnreadOptions): Promise<void> {
  initClient(options.url, options.token)

  const me = await getMe()
  const teamId = await resolveTeamId(options.team)
  const channels = [
    ...new Map((await getMyChannels()).map((channel) => [channel.id, channel])).values(),
  ].filter(
    (channel) =>
      channel.type === 'D' ||
      channel.type === 'G' ||
      ((channel.type === 'O' || channel.type === 'P') && channel.team_id === teamId),
  )
  const teamMembers = await getTeamChannelMembers(teamId)

  const memberByChannelId = new Map(teamMembers.map((member) => [member.channel_id, member]))
  const unreadEntries: UnreadSummaryItem[] = []

  for (const channel of channels) {
    let member: ChannelMember | undefined = memberByChannelId.get(channel.id)

    if (!member && (channel.type === 'D' || channel.type === 'G')) {
      member = await getChannelMember(channel.id)
    }

    if (!member) continue

    const { unreadCount, mentionCount } = calculateUnreadMetrics(channel, member)
    if (unreadCount <= 0) continue

    const processedChannel = await buildProcessedChannel(channel, me.id, options.redact)

    unreadEntries.push({
      channel,
      processedChannel,
      unreadCount,
      mentionCount,
      lastViewedAt: nonNegativeInteger(member.last_viewed_at),
    })
  }

  const sortedEntries = sortUnreadEntries(unreadEntries)

  if (sortedEntries.length === 0) {
    console.log(options.json ? JSON.stringify({ unread: [] }, null, 2) : 'All caught up!')
    return
  }

  const buildPeekOutput = async (entry: UnreadSummaryItem): Promise<MessageOutput | undefined> => {
    const result = await getAllChannelPosts(entry.channel.id, {
      limit: options.peek,
      since: entry.lastViewedAt || undefined,
    })
    if (result.posts.length === 0) {
      requireConfirmedEmpty(0, result.truncated)
      return undefined
    }
    const outputs = await buildOutputsFromPosts(
      result.posts,
      me.id,
      options,
      {
        source: 'unread',
        requestedLimit: options.peek ?? null,
        since: entry.lastViewedAt ? new Date(entry.lastViewedAt).toISOString() : null,
        queryTruncated: result.truncated,
      },
      new Map([[entry.channel.id, entry.channel]]),
    )
    return outputs[0]
  }

  if (options.json) {
    const result: {
      unread: Array<{
        channel: ProcessedChannel
        unreadCount: number
        mentionCount: number
        lastViewedAt: number
      }>
      peek?: MessageOutput[]
    } = {
      unread: sortedEntries.map((entry) => ({
        channel: entry.processedChannel,
        unreadCount: entry.unreadCount,
        mentionCount: entry.mentionCount,
        lastViewedAt: entry.lastViewedAt,
      })),
    }

    if (options.peek && options.peek > 0) {
      const peekOutputs: MessageOutput[] = []

      for (const entry of sortedEntries) {
        const output = await buildPeekOutput(entry)
        if (output) peekOutputs.push(output)
      }

      result.peek = peekOutputs
    }

    console.log(JSON.stringify(result, null, 2))
    return
  }

  console.log('Unread Channels:\n')
  for (const entry of sortedEntries) {
    const summary = `${entry.unreadCount} unread${
      entry.mentionCount > 0 ? `, ${entry.mentionCount} mentions` : ''
    }`
    console.log(`  ${channelLabel(entry.processedChannel).padEnd(32)} ${summary}`)
  }

  console.log(`\nTotal: ${sortedEntries.length} channels with unread messages`)

  if (!options.peek || options.peek <= 0) return

  const peekOutputs: MessageOutput[] = []

  for (const entry of sortedEntries) {
    const output = await buildPeekOutput(entry)
    if (output) peekOutputs.push(output)
  }

  if (peekOutputs.length > 0) {
    console.log('')
    formatOutput(peekOutputs, options)
  }
}

export async function watchChannel(
  options: CLIOptions & { channel?: string; team?: string; dm?: string },
): Promise<void> {
  initClient(options.url, options.token)
  const presentLabel = (value: string): string =>
    preprocess(value, { redact: options.redact }).text.replace(/\n/g, '\\n').replace(/\t/g, '\\t')

  printRedactionWarning(options.redact)

  if (options.channel && options.dm) {
    console.error('Error: Use either a channel target or --dm <username>, not both.')
    process.exit(1)
  }
  if (!options.channel && !options.dm) {
    console.error('Error: Provide a channel name or use --dm <username>.')
    process.exit(1)
  }

  const channel = options.dm
    ? await getDMChannelByUsername(options.dm)
    : await getChannelByName(await resolveTeamId(options.team), options.channel as string)
  if (!channel) {
    const target = options.dm
      ? `DM channel with @${presentLabel(options.dm)}`
      : `channel #${presentLabel(options.channel ?? '')}`
    console.error(`Error: ${target} not found.`)
    process.exit(1)
  }

  const watchTarget =
    channel.type === 'D'
      ? `DMs with @${presentLabel(options.dm ?? '')}`
      : `#${presentLabel(channel.name)}`
  console.error(`Watching ${watchTarget} (Ctrl+C to stop)`)

  await new Promise<void>((resolve, reject) => {
    let closed = false
    const handlePost = createWatchPostHandler(options)

    const closeAndCleanup = (closeSocket: () => void): void => {
      if (closed) return
      closed = true
      process.off('SIGINT', handleSigint)
      process.off('SIGTERM', handleSigterm)
      closeSocket()
    }

    const handleSignal = (): void => {
      closeAndCleanup(connection.close)
      resolve()
    }
    const handleSigint = handleSignal
    const handleSigterm = handleSignal

    const connection = connectWebSocket(
      options.url,
      options.token,
      handlePost,
      (error) => {
        closeAndCleanup(connection.close)
        reject(error)
      },
      {
        channelId: channel.id,
        diagnostics: {
          reconnect: (attempt, delayMs) =>
            console.error(
              `WebSocket disconnected; reconnecting in ${delayMs}ms (attempt ${attempt}).`,
            ),
          gap: ({ expected, received }) =>
            console.error(
              `Warning: WebSocket sequence gap detected (expected ${expected}, received ${received}); live events may be missing.`,
            ),
          malformed: (message) => console.error(`Warning: ${message}`),
        },
      },
    )

    process.on('SIGINT', handleSigint)
    process.on('SIGTERM', handleSigterm)
  })
}
