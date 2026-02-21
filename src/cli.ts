// CLI command handlers

import {
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
} from './api'
import { formatJSON, formatMarkdown, formatPretty } from './formatters'
import { preprocess } from './preprocessing'
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
  ProcessedMessage,
  Redaction,
  SearchOptions,
  UnreadOptions,
} from './types'
import {
  calculateUnreadMetrics,
  dim,
  formatDate,
  formatRelativeTime,
  formatTime,
  groupIntoThreads,
  sortUnreadEntries,
  userColor,
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

function formatSinceDate(duration: string): string {
  const sinceMs = parseDuration(duration)
  return new Date(sinceMs).toISOString().slice(0, 10)
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

async function buildProcessedChannel(
  channel: Channel,
  myUserId: string,
): Promise<ProcessedChannel> {
  const type = channelTypeLabel(channel.type)

  if (type === 'dm') {
    const otherUserId = getOtherUserIdFromDMChannel(channel, myUserId)
    if (!otherUserId) {
      return {
        id: channel.id,
        type: 'dm',
        name: '@unknown',
      }
    }

    const otherUser = await getUser(otherUserId)
    return {
      id: channel.id,
      type: 'dm',
      name: `@${otherUser.username}`,
    }
  }

  return {
    id: channel.id,
    type,
    name: channel.name,
    displayName: channel.display_name || undefined,
  }
}

async function processMessages(
  posts: Post[],
  myUserId: string,
  redact: boolean,
): Promise<{ messages: ProcessedMessage[]; redactions: Redaction[] }> {
  const allRedactions: Redaction[] = []
  const messages: ProcessedMessage[] = []

  for (const post of posts) {
    const postUser = await getUser(post.user_id)
    const { text, redactions } = redact
      ? preprocess(post.message)
      : { text: post.message, redactions: [] }

    allRedactions.push(...redactions)

    messages.push({
      id: post.id,
      user: postUser.id === myUserId ? 'you' : postUser.username,
      userId: post.user_id,
      text,
      timestamp: new Date(post.create_at),
      files: post.file_ids || [],
      rootId: post.root_id || undefined,
      replyCount: post.reply_count || undefined,
    })
  }

  return { messages, redactions: allRedactions }
}

async function buildOutputsFromPosts(
  posts: Post[],
  myUserId: string,
  options: CLIOptions,
): Promise<MessageOutput[]> {
  const userIds = [...new Set(posts.map((post) => post.user_id))]
  if (userIds.length > 0) {
    await getUsersByIds(userIds)
  }

  const grouped = groupPostsByChannel(posts)
  const outputs: MessageOutput[] = []

  for (const [channelId, channelPosts] of grouped) {
    const channel = await getChannel(channelId)
    const processedChannel = await buildProcessedChannel(channel, myUserId)
    const { messages, redactions } = await processMessages(channelPosts, myUserId, options.redact)

    outputs.push({
      channel: processedChannel,
      messages: options.threads ? groupIntoThreads(messages) : messages,
      redactions,
    })
  }

  return outputs
}

function formatOutput(outputs: MessageOutput[], options: CLIOptions): void {
  if (options.json) {
    console.log(formatJSON(outputs))
  } else if (process.stdout.isTTY && options.color) {
    console.log(formatPretty(outputs, { color: true, relative: options.relative }))
  } else if (!options.color) {
    console.log(formatPretty(outputs, { color: false, relative: options.relative }))
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
      const processed = await buildProcessedChannel(channel, me.id)
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
        console.error(`Warning: Could not find DM channel with @${username}`)
      }
    }
  } else {
    channels = await getMyDMChannels()
  }

  if (channels.length === 0) {
    console.error('No DM channels found')
    process.exit(1)
  }

  printRedactionWarning(options.redact)

  const since = options.since ? parseDuration(options.since) : undefined
  const outputs: MessageOutput[] = []

  for (const channel of channels) {
    const posts = await getAllChannelPosts(channel.id, {
      limit: options.limit,
      since,
    })
    if (posts.length === 0) continue

    const processedChannel = await buildProcessedChannel(channel, me.id)
    const userIds = [...new Set(posts.map((post) => post.user_id))]
    if (userIds.length > 0) {
      await getUsersByIds(userIds)
    }
    const { messages, redactions } = await processMessages(posts, me.id, options.redact)

    outputs.push({
      channel: processedChannel,
      messages: options.threads ? groupIntoThreads(messages) : messages,
      redactions,
    })
  }

  if (outputs.length === 0) {
    console.error('No messages found')
    process.exit(1)
  }

  formatOutput(outputs, options)
}

export async function fetchChannel(options: ChannelOptions): Promise<void> {
  initClient(options.url, options.token)

  const me = await getMe()
  const teamId = await resolveTeamId(options.team)
  const channel = await getChannelByName(teamId, options.channel)

  printRedactionWarning(options.redact)

  const since = options.since ? parseDuration(options.since) : undefined
  const posts = await getAllChannelPosts(channel.id, {
    limit: options.limit,
    since,
  })

  if (posts.length === 0) {
    console.error('No messages found in this channel')
    process.exit(1)
  }

  const userIds = [...new Set(posts.map((post) => post.user_id))]
  if (userIds.length > 0) {
    await getUsersByIds(userIds)
  }

  const processedChannel = await buildProcessedChannel(channel, me.id)
  const { messages, redactions } = await processMessages(posts, me.id, options.redact)

  formatOutput(
    [
      {
        channel: processedChannel,
        messages: options.threads ? groupIntoThreads(messages) : messages,
        redactions,
      },
    ],
    options,
  )
}

export async function fetchThread(options: CLIOptions & { postId: string }): Promise<void> {
  initClient(options.url, options.token)

  printRedactionWarning(options.redact)

  const me = await getMe()
  const posts = await getPostThread(options.postId)

  if (posts.length === 0) {
    console.error('Thread not found or empty')
    process.exit(1)
  }

  const rootPost = posts.find((post) => !post.root_id)
  const channel = rootPost ? await getChannel(rootPost.channel_id) : null

  const userIds = [...new Set(posts.map((post) => post.user_id))]
  if (userIds.length > 0) {
    await getUsersByIds(userIds)
  }

  const { messages, redactions } = await processMessages(posts, me.id, options.redact)

  const processedChannel = channel
    ? await buildProcessedChannel(channel, me.id)
    : {
        id: rootPost?.channel_id || '',
        type: 'public' as const,
        name: 'unknown',
      }

  formatOutput(
    [
      {
        channel: processedChannel,
        messages: groupIntoThreads(messages),
        redactions,
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
  const response = await searchPosts(teamId, query)

  const posts = response.order
    .map((id) => response.posts[id])
    .filter((post): post is Post => !!post && post.delete_at === 0)
    .slice(0, options.limit)

  if (posts.length === 0) {
    console.error('No results found')
    process.exit(1)
  }

  const outputs = await buildOutputsFromPosts(posts, me.id, options)
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
  if (options.since) {
    modifiers.push(`after:${formatSinceDate(options.since)}`)
  }
  if (options.channel) {
    modifiers.push(`in:${normalizeChannelName(options.channel)}`)
  }

  const dedupedPosts = new Map<string, Post>()

  const searchTerms = [...new Set(baseTerms)]

  for (const term of searchTerms) {
    const searchTerm = [term, ...modifiers].join(' ')
    const response = await searchPosts(teamId, searchTerm)

    for (const id of response.order) {
      const post = response.posts[id]
      if (post && post.delete_at === 0) {
        dedupedPosts.set(post.id, post)
      }
    }
  }

  const posts = [...dedupedPosts.values()]
    .sort((a, b) => b.create_at - a.create_at)
    .slice(0, options.limit)

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

  const outputs = await buildOutputsFromPosts(posts, me.id, options)
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

    const processedChannel = await buildProcessedChannel(channel, me.id)

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
        const posts = await getAllChannelPosts(entry.channel.id, {
          limit: options.peek,
          since: entry.lastViewedAt || undefined,
        })
        if (posts.length === 0) continue

        const userIds = [...new Set(posts.map((post) => post.user_id))]
        if (userIds.length > 0) await getUsersByIds(userIds)

        const { messages, redactions } = await processMessages(posts, me.id, options.redact)

        peekOutputs.push({
          channel: entry.processedChannel,
          messages: options.threads ? groupIntoThreads(messages) : messages,
          redactions,
        })
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
    const posts = await getAllChannelPosts(entry.channel.id, {
      limit: options.peek,
      since: entry.lastViewedAt || undefined,
    })
    if (posts.length === 0) continue

    const userIds = [...new Set(posts.map((post) => post.user_id))]
    if (userIds.length > 0) await getUsersByIds(userIds)

    const { messages, redactions } = await processMessages(posts, me.id, options.redact)

    peekOutputs.push({
      channel: entry.processedChannel,
      messages: options.threads ? groupIntoThreads(messages) : messages,
      redactions,
    })
  }

  if (peekOutputs.length > 0) {
    console.log('')
    formatOutput(peekOutputs, options)
  }
}

export async function watchChannel(
  options: CLIOptions & { channel: string; team?: string },
): Promise<void> {
  initClient(options.url, options.token)

  if (options.json) {
    console.error('Warning: --json is ignored for watch mode.')
  }

  printRedactionWarning(options.redact)

  const teamId = await resolveTeamId(options.team)
  const channel = await getChannelByName(teamId, options.channel)

  console.log(`Watching #${channel.name} (Ctrl+C to stop)`)

  await new Promise<void>((resolve, reject) => {
    let closed = false

    const closeAndCleanup = (closeSocket: () => void): void => {
      if (closed) return
      closed = true
      process.off('SIGINT', handleSigint)
      closeSocket()
    }

    const handleSigint = (): void => {
      closeAndCleanup(connection.close)
      resolve()
    }

    const connection = connectWebSocket(
      options.url,
      options.token,
      (post, _channelName, senderName) => {
        void (async () => {
          const cachedUser = getCachedUser(post.user_id)
          let username = cachedUser?.username || senderName || 'unknown'

          if (!cachedUser) {
            try {
              username = (await getUser(post.user_id)).username
            } catch {
              // keep senderName fallback
            }
          }

          const { text } = options.redact ? preprocess(post.message) : { text: post.message }
          const message = text.replace(/\s+/g, ' ').trim() || '[empty message]'
          const time = formatTime(new Date(post.create_at))

          if (options.color) {
            console.log(`${dim(`[${time}]`)} ${userColor(username)}: ${message}`)
          } else {
            console.log(`[${time}] ${username}: ${message}`)
          }
        })()
      },
      (error) => {
        closeAndCleanup(connection.close)
        reject(error)
      },
      channel.id,
    )

    process.on('SIGINT', handleSigint)
  })
}
