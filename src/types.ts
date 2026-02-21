// Mattermost API types

export interface User {
  id: string
  username: string
  nickname: string
  first_name: string
  last_name: string
  email: string
}

export interface Team {
  id: string
  name: string
  display_name: string
  type: 'O' | 'I' // Open, Invite-only
}

export interface Channel {
  id: string
  type: 'O' | 'P' | 'D' | 'G' // Open, Private, Direct, Group
  display_name: string
  name: string
  header: string
  purpose: string
  last_post_at: number
  total_msg_count: number
  creator_id: string
}

export interface Post {
  id: string
  create_at: number
  update_at: number
  delete_at: number
  edit_at: number
  user_id: string
  channel_id: string
  message: string
  type: string
  props: Record<string, unknown>
  hashtags: string
  root_id: string // empty = root post, non-empty = reply to this post
  reply_count: number // number of replies (root posts only)
  file_ids: string[]
  pending_post_id: string
  metadata?: PostMetadata
}

export interface PostMetadata {
  files?: FileInfo[]
  reactions?: Reaction[]
}

export interface FileInfo {
  id: string
  name: string
  extension: string
  size: number
  mime_type: string
}

export interface Reaction {
  user_id: string
  post_id: string
  emoji_name: string
  create_at: number
}

export interface PostsResponse {
  order: string[]
  posts: Record<string, Post>
  next_post_id: string
  prev_post_id: string
}

export interface SearchResponse {
  order: string[]
  posts: Record<string, Post>
  matches: Record<string, string[]>
}

export interface ChannelMember {
  channel_id: string
  user_id: string
  msg_count: number
  mention_count: number
  last_viewed_at: number
}

// CLI types

export interface CLIOptions {
  url: string
  token: string
  json: boolean
  color: boolean
  relative: boolean
  redact: boolean
  threads: boolean
}

export interface DMsOptions extends CLIOptions {
  user: string[]
  limit: number
  since: string
  channel?: string
}

export interface ChannelOptions extends CLIOptions {
  channel: string // channel name or ID
  team?: string // team name (required if multi-team)
  limit: number
  since: string
}

export interface SearchOptions extends CLIOptions {
  query: string
  team?: string
  limit: number
}

export interface MentionOptions extends CLIOptions {
  team?: string
  limit: number
  since?: string
  channel?: string
  mentionNames: string[]
}

export interface UnreadOptions extends CLIOptions {
  team?: string
  peek?: number
}

// Processed message for output

export interface ProcessedMessage {
  id: string
  user: string
  userId: string
  text: string
  timestamp: Date
  files: string[]
  rootId?: string
  replyCount?: number
  replies?: ProcessedMessage[]
}

export interface ProcessedChannel {
  id: string
  type: 'dm' | 'public' | 'private' | 'group'
  name: string // "@username" for DMs, "channel-name" for channels
  displayName?: string // Channel display name (channels only)
}

export interface MessageOutput {
  channel: ProcessedChannel
  messages: ProcessedMessage[]
  redactions: Redaction[]
}

/** @deprecated Use MessageOutput */
export type DMOutput = MessageOutput

export interface Redaction {
  type: string
  masked: string
  position: number
}

export interface PreprocessResult {
  text: string
  redactions: Redaction[]
}

export interface WSPostEvent {
  event: 'posted'
  data: {
    post: string
    channel_type: string
    channel_name: string
    channel_display_name: string
    sender_name: string
    mentions?: string
  }
  broadcast: {
    channel_id: string
    team_id: string
  }
}
