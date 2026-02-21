// Post/message fetching with pagination

import type { Post, PostsResponse, SearchResponse } from '../types'
import { getClient } from './client'

interface GetPostsOptions {
  limit?: number
  since?: number // epoch milliseconds
  before?: string // post ID for pagination
  after?: string // post ID for pagination
}

export async function getChannelPosts(
  channelId: string,
  options: GetPostsOptions = {},
): Promise<Post[]> {
  const { limit = 50, since, before, after } = options
  const client = getClient()

  const params = new URLSearchParams()
  params.set('per_page', String(Math.min(limit, 200))) // API max is 200

  if (since) params.set('since', String(since))
  if (before) params.set('before', before)
  if (after) params.set('after', after)

  const response = await client.get<PostsResponse>(`/channels/${channelId}/posts?${params}`)

  // Convert posts object to array, sorted by order
  const posts = response.order
    .map((id) => response.posts[id])
    .filter((post): post is Post => !!post && post.delete_at === 0) // Exclude deleted

  return posts
}

// Fetch all posts with pagination, respecting limit and since
export async function getAllChannelPosts(
  channelId: string,
  options: { limit?: number; since?: number } = {},
): Promise<Post[]> {
  const { limit = 50, since } = options
  const allPosts: Post[] = []
  let before: string | undefined

  while (allPosts.length < limit) {
    const remaining = limit - allPosts.length
    const posts = await getChannelPosts(channelId, {
      limit: Math.min(remaining, 200),
      since,
      before,
    })

    if (posts.length === 0) break

    allPosts.push(...posts)

    // Get the oldest post ID for next page
    const oldestPost = posts[posts.length - 1]
    if (!oldestPost) break
    before = oldestPost.id

    // If we got fewer than requested, we've hit the end
    if (posts.length < Math.min(remaining, 200)) break
  }

  // Filter by since timestamp if provided
  if (since) {
    return allPosts.filter((post) => post.create_at >= since)
  }

  return allPosts.slice(0, limit)
}

// Fetch a full thread (root + all replies)
export async function getPostThread(postId: string): Promise<Post[]> {
  const client = getClient()
  const response = await client.get<PostsResponse>(`/posts/${postId}/thread`)

  return response.order
    .map((id) => response.posts[id])
    .filter((post): post is Post => !!post && post.delete_at === 0)
}

export async function searchPosts(teamId: string, terms: string): Promise<SearchResponse> {
  const client = getClient()
  return client.post<SearchResponse>(`/teams/${teamId}/posts/search`, {
    terms,
    is_or_search: false,
  })
}

// Parse duration string to milliseconds
// Supports: "24h", "7d", "30d", "1w", "2m" (months)
export function parseDuration(duration: string): number {
  const match = duration.match(/^(\d+)([hdwm])$/i)
  if (!match) {
    throw new Error(
      `Invalid duration format: ${duration}. Use formats like "24h", "7d", "1w", "2m"`,
    )
  }

  const valueText = match[1]
  const unitText = match[2]
  if (!valueText || !unitText) {
    throw new Error(`Invalid duration format: ${duration}`)
  }

  const value = parseInt(valueText, 10)
  const unit = unitText.toLowerCase()

  const now = Date.now()
  const msPerHour = 60 * 60 * 1000
  const msPerDay = 24 * msPerHour

  switch (unit) {
    case 'h':
      return now - value * msPerHour
    case 'd':
      return now - value * msPerDay
    case 'w':
      return now - value * 7 * msPerDay
    case 'm':
      return now - value * 30 * msPerDay
    default:
      throw new Error(`Unknown duration unit: ${unit}`)
  }
}
