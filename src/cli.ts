// CLI command handlers

import {
  buildPostPermalink,
  connectWebSocket,
  getAllChannelPosts,
  getCachedUser,
  getChannel,
  getChannelByName,
  getChannelMember,
  getDMChannelByUsername,
  getMe,
  getMyChannels,
  getMyDMChannels,
  getOtherUserIdFromDMChannel,
  getPostThread,
  getTeamChannelMembers,
  getUser,
  getUsersByIds,
  initClient,
  normalizeChannelName,
  parseDuration,
  resolveTeamId,
  searchPosts,
  takeMostRecentPosts,
} from './api'
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
  CLIOptions,
  DMsOptions,
  MentionOptions,
  MessageOutput,
  Post,
  ProcessedChannel,
  Redaction,
  RetrievalMetadata,
  SearchOptions,
  UnreadOptions,
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
  lastPost: string | null
  messageCount: number
}

interface UnreadSummaryItem {
  channel: Channel
  processedChannel: ProcessedChannel
  unreadCount: number
  mentionCount: number
  lastViewedAt: number
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

function channelTypeLabel(type: Channel['type']): ProcessedChannel['type'] {
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
      throw new Error(`Unknown channel type: ${type satisfies never}`)
  }
}

function channelLabel(channel: ProcessedChannel): string {
  if (channel.type === 'dm') return channel.name
  const display = channel.displayName ? ` (${channel.displayName})` : ''
  return `#${channel.name}${display}`
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
  const type = channelTypeLabel(channel.type)
  const clean = (value: string, field: string): string => {
    const result = preprocess(value, { redact })
    redactions.push(...result.redactions.map((item) => ({ ...item, field })))
    return result.text.replace(/\n/g, '\\n').replace(/\t/g, '\\t')
  }

  if (type === 'dm') {
    const otherUserId = getOtherUserIdFromDMChannel(channel, myUserId)
    if (!otherUserId) {
      return {
        id: clean(channel.id, 'channel.id'),
        type: 'dm',
        name: '@unknown',
      }
    }

    const otherUser = await getUser(otherUserId)
    return {
      id: clean(channel.id, 'channel.id'),
      type: 'dm',
      name: `@${clean(otherUser.username, 'channel.dmUsername')}`,
    }
  }

  return {
    id: clean(channel.id, 'channel.id'),
    type,
    name: clean(channel.name, 'channel.name'),
    displayName: channel.display_name
      ? clean(channel.display_name, 'channel.displayName')
      : undefined,
  }
}

async function buildOutputsFromPosts(
  posts: Post[],
  myUserId: string,
  options: CLIOptions,
  retrieval: Pick<
    RetrievalMetadata['selection'],
    'source' | 'requestedLimit' | 'since' | 'queryTruncated'
  >,
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
    const channel = knownChannels.get(channelId) ?? (await getChannel(channelId))
    const channelRedactions: Redaction[] = []
    const processedChannel = await buildProcessedChannel(
      channel,
      myUserId,
      options.redact,
      channelRedactions,
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
  >,
  selectedCount: number,
  visibleThreads: RetrievalMetadata['visibleThreads'] = {
    status: 'not_requested',
    hydratedRootCount: 0,
    failedRootIds: [],
  },
  visiblePostCount = selectedCount,
): RetrievalMetadata {
  return {
    selection: { ...selection, selectedCount },
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

function buildChannelListItem(channel: ProcessedChannel, rawChannel: Channel): ChannelListItem {
  return {
    id: channel.id,
    type: channel.type,
    name: channel.name,
    displayName: channel.displayName,
    lastPost: rawChannel.last_post_at ? new Date(rawChannel.last_post_at).toISOString() : null,
    messageCount: rawChannel.total_msg_count,
  }
}

export async function listChannels(options: {
  url: string
  token: string
  json: boolean
  color: boolean
  relative: boolean
  redact: boolean
  typeFilter: string
}): Promise<void> {
  initClient(options.url, options.token)

  const me = await getMe()
  let channels = await getMyChannels()

  if (options.typeFilter !== 'all') {
    const typeMap: Record<string, Channel['type']> = {
      dm: 'D',
      public: 'O',
      private: 'P',
      group: 'G',
    }
    const filterType = typeMap[options.typeFilter]
    if (filterType) {
      channels = channels.filter((ch) => ch.type === filterType)
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
      return buildChannelListItem(processed, channel)
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

      const label = channel.type === 'dm' ? channel.name : `#${channel.name}`
      const display = channel.displayName ? ` (${channel.displayName})` : ''
      console.log(
        `  ${label.padEnd(25)}${display ? display.padEnd(25) : ''.padEnd(25)} ${channel.messageCount} msgs, last: ${lastPost}`,
      )
    }
  }

  console.log(`\nTotal: ${output.length} channels`)
}

export async function fetchDMs(options: DMsOptions): Promise<void> {
  initClient(options.url, options.token)

  const me = await getMe()
  let channels: Channel[] = []

  if (options.channel) {
    channels = [await getChannel(options.channel)]
  } else if (options.user.length > 0) {
    for (const username of options.user) {
      try {
        const channel = await getDMChannelByUsername(username)
        if (channel) channels.push(channel)
      } catch {
        console.error(`Warning: Could not find DM channel with @${sanitizeTerminalLabel(username)}`)
      }
    }
  } else {
    channels = await getMyDMChannels()
  }

  channels = [...new Map(channels.map((channel) => [channel.id, channel])).values()]

  if (channels.length === 0) {
    console.error('No DM channels found')
    process.exit(1)
  }

  printRedactionWarning(options.redact)

  const since = options.since ? parseDuration(options.since) : undefined
  const channelPosts = new Map<string, Post[]>()
  const truncationStates: Array<boolean | null> = []
  const allPosts: Post[] = []

  for (const channel of channels) {
    const result = await getAllChannelPosts(channel.id, {
      limit: options.limit,
      since,
    })
    const posts = result.posts
    truncationStates.push(result.truncated)
    if (posts.length === 0) continue

    channelPosts.set(channel.id, posts)
    allPosts.push(...posts)
  }

  if (allPosts.length === 0) {
    console.error('No messages found')
    process.exit(1)
  }

  // `--limit` for DMs is a total output budget across all matched DM channels.
  const selectedPostIds = new Set(
    takeMostRecentPosts(allPosts, options.limit).map((post) => post.id),
  )
  const queryTruncated = mergeTruncation(truncationStates, allPosts.length, options.limit)
  const selectedPosts = allPosts.filter((post) => selectedPostIds.has(post.id))
  const outputs = await buildOutputsFromPosts(
    selectedPosts,
    me.id,
    options,
    {
      source: 'recent',
      requestedLimit: options.limit,
      since: since === undefined ? null : new Date(since).toISOString(),
      queryTruncated,
    },
    new Map(channels.map((channel) => [channel.id, channel])),
  )

  formatOutput(outputs, options)
}

export async function fetchChannel(options: ChannelOptions): Promise<void> {
  initClient(options.url, options.token)

  const me = await getMe()
  const teamId = await resolveTeamId(options.team)
  const channel = await getChannelByName(teamId, options.channel)

  printRedactionWarning(options.redact)

  const since = options.since ? parseDuration(options.since) : undefined
  const result = await getAllChannelPosts(channel.id, {
    limit: options.limit,
    since,
  })
  const posts = result.posts

  if (posts.length === 0) {
    console.error('No messages found in this channel')
    process.exit(1)
  }

  const outputs = await buildOutputsFromPosts(
    posts,
    me.id,
    options,
    {
      source: 'recent',
      requestedLimit: options.limit,
      since: since === undefined ? null : new Date(since).toISOString(),
      queryTruncated: result.truncated,
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
  const channel = channelPost ? await getChannel(channelPost.channel_id) : null
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

  const channelRedactions: Redaction[] = []
  const fallbackChannelId = preprocess(channelPost?.channel_id || '', {
    redact: options.redact,
  })
  if (!channel) {
    redactions.push(
      ...fallbackChannelId.redactions.map((item) => ({ ...item, field: 'channel.id' })),
    )
  }
  const processedChannel = channel
    ? await buildProcessedChannel(channel, me.id, options.redact, channelRedactions)
    : {
        id: fallbackChannelId.text.replace(/\n/g, '\\n').replace(/\t/g, '\\t'),
        type: 'public' as const,
        name: 'unknown',
      }

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
    console.error('No results found')
    process.exit(1)
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

  if (posts.length === 0) {
    if (options.mentionNames.length === 0) {
      console.error(
        'No mentions found. Hint: configure mention_names in your config to include aliases.',
      )
    } else {
      console.error('No mentions found')
    }
    process.exit(1)
  }

  const queryTruncated = mergeTruncation(truncationStates, dedupedPosts.size, options.limit)
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
  const channels = await getMyChannels()
  const teamMembers = await getTeamChannelMembers(teamId)

  const memberByChannelId = new Map(teamMembers.map((member) => [member.channel_id, member]))
  const unreadEntries: UnreadSummaryItem[] = []

  for (const channel of channels) {
    let member: ChannelMember | undefined = memberByChannelId.get(channel.id)

    if (!member && (channel.type === 'D' || channel.type === 'G')) {
      try {
        member = await getChannelMember(channel.id)
      } catch {
        continue
      }
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
      lastViewedAt: member.last_viewed_at,
    })
  }

  const sortedEntries = sortUnreadEntries(unreadEntries)

  if (sortedEntries.length === 0) {
    console.log('All caught up!')
    return
  }

  const buildPeekOutput = async (entry: UnreadSummaryItem): Promise<MessageOutput | undefined> => {
    const result = await getAllChannelPosts(entry.channel.id, {
      limit: options.peek,
      since: entry.lastViewedAt || undefined,
    })
    if (result.posts.length === 0) return undefined
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
