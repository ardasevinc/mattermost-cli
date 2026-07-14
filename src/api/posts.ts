// Post/message fetching with pagination

import type { Post, PostRetrievalResult, PostsResponse, SearchResponse } from '../types'
import { getClient } from './client'

interface GetPostsOptions {
  limit?: number
  page?: number
  skipFetchThreads?: boolean
}

export async function getChannelPosts(
  channelId: string,
  options: GetPostsOptions = {},
): Promise<{
  posts: Post[]
  rawCount: number
  firstInaccessiblePostTime: number | null
  hasNext: boolean | null
}> {
  const { limit = 50, page = 0, skipFetchThreads = true } = options
  const client = getClient()

  const params = new URLSearchParams()
  params.set('per_page', String(Math.min(limit, 200))) // API max is 200
  params.set('page', String(page))
  params.set('skipFetchThreads', String(skipFetchThreads))

  const response = await client.get<PostsResponse>(`/channels/${channelId}/posts?${params}`)

  // Convert posts object to array, sorted by order
  const posts = response.order
    .map((id) => response.posts[id])
    .filter((post): post is Post => !!post && post.delete_at === 0) // Exclude deleted

  return {
    posts,
    rawCount: response.order.length,
    firstInaccessiblePostTime: response.first_inaccessible_post_time || null,
    hasNext: response.has_next ?? null,
  }
}

function byMostRecentPost<T extends Pick<Post, 'create_at' | 'id'>>(a: T, b: T): number {
  const diff = b.create_at - a.create_at
  if (diff !== 0) return diff
  return a.id.localeCompare(b.id)
}

// Fetch all posts with pagination, respecting limit and since
export async function getAllChannelPosts(
  channelId: string,
  options: { limit?: number; since?: number } = {},
): Promise<PostRetrievalResult> {
  const { limit = 50, since } = options
  const postsById = new Map<string, Post>()
  const seenIds = new Set<string>()
  const target = limit + 1
  const pageSize = Math.min(target, 200)
  let page = 0
  let stagnantPages = 0
  let exhausted = false
  let uncertain = false

  while (true) {
    const pageResult = await getChannelPosts(channelId, {
      limit: pageSize,
      page,
    })
    const posts = pageResult.posts
    if (pageResult.firstInaccessiblePostTime !== null) uncertain = true

    if (pageResult.rawCount === 0) {
      exhausted = !uncertain
      break
    }

    let madeProgress = false
    for (const post of posts) {
      if (seenIds.has(post.id)) continue
      seenIds.add(post.id)
      madeProgress = true
      if (since === undefined || post.create_at >= since) postsById.set(post.id, post)
    }

    page += 1

    if (madeProgress) stagnantPages = 0
    else stagnantPages += 1

    if (since !== undefined && posts.length > 0 && posts.every((post) => post.create_at < since)) {
      exhausted = !uncertain
      break
    }
    if (postsById.size >= target) {
      const selected = takeMostRecentPosts([...postsById.values()], limit)
      const cutoff = selected[selected.length - 1]?.create_at
      if (cutoff !== undefined && posts.some((post) => post.create_at < cutoff)) break
    }
    if (pageResult.rawCount < pageSize && pageResult.hasNext !== true) {
      exhausted = !uncertain
      break
    }
    if (stagnantPages >= 2) {
      uncertain = true
      break
    }
  }

  const selected = takeMostRecentPosts([...postsById.values()], limit)
  return {
    posts: selected,
    truncated: postsById.size > limit ? true : uncertain ? null : exhausted ? false : null,
  }
}

export function takeMostRecentPosts<T extends Pick<Post, 'create_at' | 'id'>>(
  posts: T[],
  limit: number,
): T[] {
  return [...new Map(posts.map((post) => [post.id, post])).values()]
    .sort(byMostRecentPost)
    .slice(0, limit)
}

// Fetch a full thread (root + all replies)
export async function getPostThread(postId: string): Promise<PostRetrievalResult> {
  const client = getClient()
  const response = await client.get<PostsResponse>(`/posts/${postId}/thread`)

  const posts = response.order
    .map((id) => response.posts[id])
    .filter((post): post is Post => !!post && post.delete_at === 0)

  return {
    posts,
    truncated:
      response.first_inaccessible_post_time !== undefined &&
      response.first_inaccessible_post_time > 0
        ? null
        : response.has_next === undefined
          ? null
          : response.has_next,
  }
}

export async function searchPosts(
  teamId: string,
  terms: string,
  limit = 50,
  accept: (post: Post) => boolean = () => true,
): Promise<SearchResponse & { truncated: boolean | null }> {
  const client = getClient()
  const posts = new Map<string, Post>()
  const seenIds = new Set<string>()
  const order: string[] = []
  const matches: Record<string, string[]> = {}
  const target = limit + 1
  const perPage = Math.min(target, 100)
  let page = 0
  let stagnantPages = 0
  let exhausted = false
  let uncertain = false

  while (true) {
    const response = await client.post<SearchResponse>(`/teams/${teamId}/posts/search`, {
      terms,
      is_or_search: false,
      page,
      per_page: perPage,
    })
    if (response.first_inaccessible_post_time) uncertain = true
    if (response.has_next === true && response.order.length === 0) uncertain = true
    let madeProgress = false
    const acceptedThisPage: Post[] = []
    for (const id of response.order) {
      if (seenIds.has(id)) continue
      seenIds.add(id)
      madeProgress = true
      const post = response.posts[id]
      if (!post || post.delete_at !== 0 || !accept(post)) continue
      posts.set(id, post)
      acceptedThisPage.push(post)
      order.push(id)
      if (response.matches[id]) matches[id] = response.matches[id]
    }
    page += 1
    if (madeProgress) stagnantPages = 0
    else stagnantPages += 1

    // Search paging is only fully honored by Elasticsearch-backed servers. A short page is not
    // proof of exhaustion, so rely on an empty response or bounded stagnation instead.
    if (response.order.length === 0) {
      exhausted = !uncertain
      break
    }
    if (response.has_next === false) {
      exhausted = !uncertain
      break
    }
    if (stagnantPages >= 2) {
      uncertain = true
      break
    }
    if (posts.size < target) continue

    const selected = takeMostRecentPosts([...posts.values()], limit)
    const cutoff = selected[selected.length - 1]?.create_at
    if (cutoff !== undefined && acceptedThisPage.some((post) => post.create_at < cutoff)) break
  }

  const selected = takeMostRecentPosts([...posts.values()], limit)
  const selectedIds = new Set(selected.map(({ id }) => id))
  return {
    order: selected.map(({ id }) => id),
    posts: Object.fromEntries([...posts].filter(([id]) => selectedIds.has(id))),
    matches: Object.fromEntries(Object.entries(matches).filter(([id]) => selectedIds.has(id))),
    truncated: posts.size > limit ? true : uncertain ? null : exhausted ? false : null,
  }
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
