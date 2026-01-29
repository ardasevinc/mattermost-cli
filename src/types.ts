// Mattermost API types

export interface User {
  id: string
  username: string
  nickname: string
  first_name: string
  last_name: string
  email: string
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

// Config file types

export interface FileConfig {
  url?: string
  token?: string
}

// CLI types

export interface CLIOptions {
  url: string
  token: string
  json: boolean
  color: boolean
  relative: boolean
}

export interface DMsOptions extends CLIOptions {
  user: string[]
  limit: number
  since: string
  channel?: string
}

// Processed message for output

export interface ProcessedMessage {
  id: string
  user: string
  userId: string
  text: string
  timestamp: Date
  files: string[]
}

export interface ProcessedChannel {
  id: string
  otherUser: string
  otherUserId: string
}

export interface DMOutput {
  channel: ProcessedChannel
  messages: ProcessedMessage[]
  redactions: Redaction[]
}

export interface Redaction {
  type: string
  masked: string
  position: number
}

export interface PreprocessResult {
  text: string
  redactions: Redaction[]
}
