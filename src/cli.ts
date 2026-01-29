// CLI command handlers

import type {
  Channel,
  DMOutput,
  DMsOptions,
  Post,
  ProcessedChannel,
  ProcessedMessage,
  Redaction,
} from './types'
import {
  initClient,
  getMyDMChannels,
  getOtherUserIdFromDMChannel,
  getAllChannelPosts,
  parseDuration,
  getMe,
  getUser,
  getUsersByIds,
  getDMChannelByUsername,
  getChannel,
} from './api'
import { preprocess } from './preprocessing'
import { formatJSON, formatMarkdown, formatPretty } from './formatters'
import { formatDate, formatRelativeTime } from './utils/date'

// List all DM channels
export async function listChannels(options: {
  url: string
  token: string
  json: boolean
  color: boolean
  relative: boolean
}): Promise<void> {
  initClient(options.url, options.token)

  const me = await getMe()
  const channels = await getMyDMChannels()

  // Get all other user IDs
  const otherUserIds = channels
    .map((ch) => getOtherUserIdFromDMChannel(ch, me.id))
    .filter(Boolean) as string[]

  // Batch fetch users
  const users = await getUsersByIds(otherUserIds)
  const userMap = new Map(users.map((u) => [u.id, u]))

  // Build output
  const output = channels.map((ch) => {
    const otherUserId = getOtherUserIdFromDMChannel(ch, me.id)
    const otherUser = otherUserId ? userMap.get(otherUserId) : null

    return {
      id: ch.id,
      user: otherUser?.username || 'unknown',
      userId: otherUserId || 'unknown',
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
    console.log('DM Channels:\n')
    for (const ch of output) {
      let lastPost = 'never'
      if (ch.lastPost) {
        const date = new Date(ch.lastPost)
        lastPost = options.relative
          ? formatRelativeTime(date)
          : formatDate(date, { includeYear: true })
      }
      console.log(`  @${ch.user.padEnd(20)} ${ch.messageCount} msgs, last: ${lastPost}`)
    }
    console.log(`\nTotal: ${output.length} DM channels`)
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
      } catch (err) {
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

  // Parse time filter
  const since = options.since ? parseDuration(options.since) : undefined

  // Fetch messages from each channel
  const outputs: DMOutput[] = []

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
    const allRedactions: Redaction[] = []
    const messages: ProcessedMessage[] = []

    for (const post of posts) {
      const postUser = await getUser(post.user_id)
      const { text, redactions } = preprocess(post.message)

      allRedactions.push(...redactions)

      messages.push({
        id: post.id,
        user: postUser.id === me.id ? 'you' : postUser.username,
        userId: post.user_id,
        text,
        timestamp: new Date(post.create_at),
        files: post.file_ids || [],
      })
    }

    const processedChannel: ProcessedChannel = {
      id: channel.id,
      otherUser: otherUser.username,
      otherUserId: otherUser.id,
    }

    outputs.push({
      channel: processedChannel,
      messages,
      redactions: allRedactions,
    })
  }

  if (outputs.length === 0) {
    console.error('No messages found')
    process.exit(1)
  }

  // Format output
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
