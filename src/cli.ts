// CLI command handlers

import {
  getAllChannelPosts,
  getChannel,
  getChannelByName,
  getDMChannelByUsername,
  getMe,
  getMyChannels,
  getMyDMChannels,
  getOtherUserIdFromDMChannel,
  getPostThread,
  getUser,
  getUsersByIds,
  initClient,
  parseDuration,
  resolveTeamId,
} from './api'
import { formatJSON, formatMarkdown, formatPretty } from './formatters'
import { preprocess } from './preprocessing'
import type {
  Channel,
  ChannelOptions,
  CLIOptions,
  DMsOptions,
  MessageOutput,
  Post,
  ProcessedChannel,
  ProcessedMessage,
  Redaction,
} from './types'
import { formatDate, formatRelativeTime, groupIntoThreads } from './utils'

// Map Mattermost channel type codes to our ProcessedChannel types
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
  }
}

// List channels the user belongs to
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

  // Apply type filter
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

  // Get DM user info for DM channels
  const dmChannels = channels.filter((ch) => ch.type === 'D')
  const otherUserIds = dmChannels
    .map((ch) => getOtherUserIdFromDMChannel(ch, me.id))
    .filter(Boolean) as string[]

  const users = otherUserIds.length > 0 ? await getUsersByIds(otherUserIds) : []
  const userMap = new Map(users.map((u) => [u.id, u]))

  // Build output
  const output = channels.map((ch) => {
    const type = channelTypeLabel(ch.type)
    let name: string
    let displayName: string | undefined

    if (ch.type === 'D') {
      const otherUserId = getOtherUserIdFromDMChannel(ch, me.id)
      const otherUser = otherUserId ? userMap.get(otherUserId) : null
      name = `@${otherUser?.username || 'unknown'}`
    } else {
      name = ch.name
      displayName = ch.display_name || undefined
    }

    return {
      id: ch.id,
      type,
      name,
      displayName,
      lastPost: ch.last_post_at ? new Date(ch.last_post_at).toISOString() : null,
      messageCount: ch.total_msg_count,
    }
  })

  // Sort by last activity
  output.sort((a, b) => {
    if (!a.lastPost) return 1
    if (!b.lastPost) return -1
    return new Date(b.lastPost).getTime() - new Date(a.lastPost).getTime()
  })

  if (options.json) {
    console.log(JSON.stringify(output, null, 2))
  } else {
    // Group by type for display
    const grouped = Map.groupBy(output, (ch) => ch.type)
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
      for (const ch of items) {
        let lastPost = 'never'
        if (ch.lastPost) {
          const date = new Date(ch.lastPost)
          lastPost = options.relative
            ? formatRelativeTime(date)
            : formatDate(date, { includeYear: true })
        }
        const label = ch.type === 'dm' ? ch.name : `#${ch.name}`
        const display = ch.displayName ? ` (${ch.displayName})` : ''
        console.log(
          `  ${label.padEnd(25)}${display ? display.padEnd(25) : ''.padEnd(25)} ${ch.messageCount} msgs, last: ${lastPost}`,
        )
      }
    }

    console.log(`\nTotal: ${output.length} channels`)
  }
}

// Fetch DMs
export async function fetchDMs(options: DMsOptions): Promise<void> {
  initClient(options.url, options.token)

  const me = await getMe()
  let channels: Channel[] = []

  // If specific channel ID provided
  if (options.channel) {
    const ch = await getChannel(options.channel)
    channels = [ch]
  }
  // If filtering by usernames
  else if (options.user && options.user.length > 0) {
    for (const username of options.user) {
      try {
        const ch = await getDMChannelByUsername(username)
        if (ch) channels.push(ch)
      } catch (_err) {
        console.error(`Warning: Could not find DM channel with @${username}`)
      }
    }
  }
  // Otherwise get all DM channels
  else {
    channels = await getMyDMChannels()
  }

  if (channels.length === 0) {
    console.error('No DM channels found')
    process.exit(1)
  }

  if (!options.redact) {
    console.error('Warning: Secret redaction is disabled. Output may contain secrets.')
  }

  // Parse time filter
  const since = options.since ? parseDuration(options.since) : undefined

  // Fetch messages from each channel
  const outputs: MessageOutput[] = []

  for (const channel of channels) {
    const otherUserId = getOtherUserIdFromDMChannel(channel, me.id)
    if (!otherUserId) continue

    const otherUser = await getUser(otherUserId)

    // Fetch posts
    const posts = await getAllChannelPosts(channel.id, {
      limit: options.limit,
      since,
    })

    if (posts.length === 0) continue

    // Get all user IDs from posts and fetch them
    const postUserIds = [...new Set(posts.map((p) => p.user_id))]
    await getUsersByIds(postUserIds)

    // Process messages
    const { messages, redactions } = await processMessages(posts, me.id, options.redact)

    const processedChannel: ProcessedChannel = {
      id: channel.id,
      type: 'dm',
      name: `@${otherUser.username}`,
    }

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

// Fetch messages from a named channel
export async function fetchChannel(options: ChannelOptions): Promise<void> {
  initClient(options.url, options.token)

  const me = await getMe()
  const teamId = await resolveTeamId(options.team)
  const channel = await getChannelByName(teamId, options.channel)

  if (!options.redact) {
    console.error('Warning: Secret redaction is disabled. Output may contain secrets.')
  }

  const since = options.since ? parseDuration(options.since) : undefined

  const posts = await getAllChannelPosts(channel.id, {
    limit: options.limit,
    since,
  })

  if (posts.length === 0) {
    console.error('No messages found in this channel')
    process.exit(1)
  }

  // Get all user IDs from posts and fetch them
  const postUserIds = [...new Set(posts.map((p) => p.user_id))]
  await getUsersByIds(postUserIds)

  const { messages, redactions } = await processMessages(posts, me.id, options.redact)

  const processedChannel: ProcessedChannel = {
    id: channel.id,
    type: channelTypeLabel(channel.type),
    name: channel.name,
    displayName: channel.display_name || undefined,
  }

  const outputs: MessageOutput[] = [
    {
      channel: processedChannel,
      messages: options.threads ? groupIntoThreads(messages) : messages,
      redactions,
    },
  ]

  formatOutput(outputs, options)
}

// Fetch and display a specific thread
export async function fetchThread(options: CLIOptions & { postId: string }): Promise<void> {
  initClient(options.url, options.token)

  if (!options.redact) {
    console.error('Warning: Secret redaction is disabled. Output may contain secrets.')
  }

  const me = await getMe()
  const posts = await getPostThread(options.postId)

  if (posts.length === 0) {
    console.error('Thread not found or empty')
    process.exit(1)
  }

  // Get all user IDs from posts and fetch them
  const postUserIds = [...new Set(posts.map((p) => p.user_id))]
  await getUsersByIds(postUserIds)

  const { messages, redactions } = await processMessages(posts, me.id, options.redact)

  // Find the root post's channel to determine context
  const rootPost = posts.find((p) => !p.root_id)
  const channel = rootPost ? await getChannel(rootPost.channel_id) : null

  let processedChannel: ProcessedChannel
  if (channel && channel.type === 'D') {
    const otherUserId = getOtherUserIdFromDMChannel(channel, me.id)
    const otherUser = otherUserId ? await getUser(otherUserId) : null
    processedChannel = {
      id: rootPost?.channel_id || '',
      type: 'dm',
      name: `@${otherUser?.username || 'unknown'}`,
    }
  } else if (channel) {
    processedChannel = {
      id: channel.id,
      type: channelTypeLabel(channel.type),
      name: channel.name,
      displayName: channel.display_name || undefined,
    }
  } else {
    processedChannel = {
      id: rootPost?.channel_id || '',
      type: 'public',
      name: 'unknown',
    }
  }

  const outputs: MessageOutput[] = [
    {
      channel: processedChannel,
      messages: groupIntoThreads(messages),
      redactions,
    },
  ]

  formatOutput(outputs, options)
}

// Shared: process posts into messages with optional redaction
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

function formatOutput(outputs: MessageOutput[], options: CLIOptions): void {
  if (options.json) {
    console.log(formatJSON(outputs))
  } else if (process.stdout.isTTY && options.color) {
    console.log(formatPretty(outputs, { color: true, relative: options.relative }))
  } else if (!options.color) {
    console.log(formatPretty(outputs, { color: false, relative: options.relative }))
  } else {
    // Pipe or non-TTY: use markdown
    console.log(formatMarkdown(outputs, { relative: options.relative }))
  }
}
