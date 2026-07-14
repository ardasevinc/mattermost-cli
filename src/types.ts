// Mattermost API types

export interface User {
  id: string
  username: string
  nickname: string
  first_name: string
  last_name: string
  email: string
  roles?: string
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
  is_pinned?: boolean
  override_username?: string
  metadata?: PostMetadata
}

export interface PostMetadata {
  files?: FileInfo[]
  reactions?: Reaction[]
  embeds?: PostEmbed[]
}

export interface PostEmbed {
  type?: string
  url?: string
  data?: Record<string, unknown>
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

export interface PostAttachmentField {
  title?: unknown
  value?: unknown
  short?: boolean
}

export interface PostAttachment {
  fallback?: unknown
  pretext?: unknown
  title?: unknown
  title_link?: unknown
  text?: unknown
  fields?: unknown
  footer?: unknown
  footer_icon?: unknown
  author_name?: unknown
  author_link?: unknown
  author_icon?: unknown
  color?: unknown
  image_url?: unknown
  thumb_url?: unknown
  ts?: unknown
}

export interface PostsResponse {
  order: string[]
  posts: Record<string, Post>
  next_post_id: string
  prev_post_id: string
  has_next?: boolean
  first_inaccessible_post_time?: number
}

export interface SearchResponse {
  order: string[]
  posts: Record<string, Post>
  matches: Record<string, string[]>
  first_inaccessible_post_time?: number
  has_next?: boolean
}

export interface PostRetrievalResult {
  posts: Post[]
  truncated: boolean | null
}

export interface ChannelMember {
  channel_id: string
  user_id: string
  msg_count: number
  mention_count: number
  last_viewed_at: number
}

// CLI types

export type ChannelTypeFilter = 'all' | 'dm' | 'public' | 'private' | 'group'

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

export interface GroupDMsOptions extends CLIOptions {
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

export interface IdentityOptions {
  url: string
  token: string
  json: boolean
  redact: boolean
}

// Processed message for output

export interface ProcessedMessage {
  id: string
  permalink: string
  user: string
  userId: string
  text: string
  timestamp: Date
  updatedAt: Date
  editedAt?: Date
  deletedAt?: Date
  isDeleted: boolean
  postType: string
  isSystem: boolean
  isPinned: boolean
  files: string[]
  fileDetails: ProcessedFile[]
  attachments: ProcessedAttachment[]
  reactions: ReactionSummary[]
  rootId?: string
  replyCount?: number
  replies?: ProcessedMessage[]
}

export interface ProcessedFile {
  id: string
  name?: string
  mime?: string
  size?: number
  extension?: string
}

export interface ProcessedAttachmentField {
  title?: string
  value?: string
  short?: boolean
}

export interface ProcessedAttachment {
  fallback?: string
  pretext?: string
  title?: string
  titleLink?: string
  text?: string
  fields?: ProcessedAttachmentField[]
  footer?: string
  footerIcon?: string
  authorName?: string
  authorLink?: string
  authorIcon?: string
  color?: string
  imageUrl?: string
  thumbUrl?: string
  timestamp?: string
}

export interface ReactionActor {
  id: string
  username?: string
}

export interface ReactionSummary {
  emoji: string
  count: number
  actors: ReactionActor[]
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
  retrieval: RetrievalMetadata
}

export interface RetrievalMetadata {
  selection: {
    source: 'recent' | 'search' | 'mentions' | 'unread' | 'thread'
    selectedCount: number
    requestedLimit: number | null
    since: string | null
    queryTruncated: boolean | null
  }
  visibleThreads: {
    status: 'not_requested' | 'complete' | 'partial'
    hydratedRootCount: number
    failedRootIds: string[]
  }
  visiblePostCount: number
  deletedPostsIncluded: false
}

export interface Redaction {
  type: string
  masked: string
  position: number
  field?: string
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

export interface WatchEvent {
  type: 'posted'
  postId: string
  channelId: string
  channelName: string
  sender: string
  senderId: string
  message: string
  timestamp: string
  rootId?: string
  fileIds: string[]
  redactions: Redaction[]
}
