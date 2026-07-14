// Post/message fetching with pagination

import { comparePostIds } from '../cursor'
import type { Post, PostRetrievalResult, SearchResponse } from '../types'
import { getClient } from './client'

interface GetPostsOptions {
  limit?: number
  page?: number
  skipFetchThreads?: boolean
  before?: string
}

const MAX_SEARCH_SCAN_PAGES = 100

export interface CreatedPostReceipt {
  id: string
  channelId: string
  userId: string
  createAt: number
  pendingPostId: string
}

export async function createPost(
  channelId: string,
  message: string,
  pendingPostId: string = crypto.randomUUID(),
): Promise<CreatedPostReceipt> {
  if (!channelId) throw new Error('A destination channel is required.')
  if (!message.trim()) throw new Error('Message cannot be empty.')
  if (!pendingPostId) throw new Error('A pending post ID is required.')

  const post = await getClient().post<unknown>('/posts', {
    channel_id: channelId,
    message,
    pending_post_id: pendingPostId,
  })
  if (typeof post !== 'object' || post === null || Array.isArray(post)) {
    throw new Error('Invalid create-post response.')
  }
  const raw = post as Record<string, unknown>
  if (
    typeof raw.id !== 'string' ||
    raw.id.length === 0 ||
    raw.channel_id !== channelId ||
    typeof raw.user_id !== 'string' ||
    raw.user_id.length === 0 ||
    typeof raw.create_at !== 'number' ||
    !Number.isFinite(raw.create_at) ||
    raw.create_at <= 0
  ) {
    throw new Error('Invalid create-post response.')
  }

  return {
    id: raw.id,
    channelId,
    userId: raw.user_id,
    createAt: raw.create_at,
    pendingPostId,
  }
}

export async function getChannelPosts(
  channelId: string,
  options: GetPostsOptions = {},
): Promise<{
  posts: Post[]
  rawCount: number
  firstInaccessiblePostTime: number | null
  hasNext: boolean | null
  incompletePayload: boolean
}> {
  const { limit = 50, page = 0, skipFetchThreads = true, before } = options
  const client = getClient()

  const params = new URLSearchParams()
  params.set('per_page', String(Math.min(limit, 200))) // API max is 200
  params.set('page', String(page))
  params.set('skipFetchThreads', String(skipFetchThreads))
  if (before !== undefined) params.set('before', before)

  const rawResponse = await client.get<unknown>(
    `/channels/${encodeURIComponent(channelId)}/posts?${params}`,
  )

  const response = normalizeOrderedPostsPage(rawResponse)

  // Convert posts object to array, sorted by order
  const posts = response.order
    .map((id) => response.posts[id])
    .filter((post): post is Post => !!post && post.delete_at === 0) // Exclude deleted

  return {
    posts,
    rawCount: response.order.length,
    firstInaccessiblePostTime: response.firstInaccessiblePostTime,
    hasNext: response.hasNext,
    incompletePayload: response.incompletePayload,
  }
}

function byMostRecentPost<T extends Pick<Post, 'create_at' | 'id'>>(a: T, b: T): number {
  const diff = b.create_at - a.create_at
  if (diff !== 0) return diff
  return comparePostIds(a.id, b.id)
}

function objectRecord(value: unknown): Record<string, unknown> | null {
  return typeof value === 'object' && value !== null && !Array.isArray(value)
    ? (value as Record<string, unknown>)
    : null
}

function normalizeOrderedPostsPage(value: unknown): {
  order: string[]
  posts: Record<string, Post>
  hasNext: boolean | null
  firstInaccessiblePostTime: number | null
  incompletePayload: boolean
} {
  const response = objectRecord(value)
  if (!response || !Array.isArray(response.order)) {
    throw new Error('Mattermost returned an invalid posts response.')
  }

  const order = response.order.filter((id): id is string => typeof id === 'string')
  const rawPosts = objectRecord(response.posts)
  const posts: Record<string, Post> = {}
  let incompletePayload = order.length !== response.order.length || (!rawPosts && order.length > 0)
  for (const id of order) {
    const candidate = rawPosts?.[id]
    if (!candidate || typeof candidate !== 'object' || Array.isArray(candidate)) {
      incompletePayload = true
      continue
    }
    posts[id] = candidate as Post
  }

  const hasNext = typeof response.has_next === 'boolean' ? response.has_next : null
  if (Object.hasOwn(response, 'has_next') && response.has_next !== undefined && hasNext === null) {
    incompletePayload = true
  }
  const firstInaccessiblePostTime =
    typeof response.first_inaccessible_post_time === 'number' &&
    response.first_inaccessible_post_time > 0
      ? response.first_inaccessible_post_time
      : null

  return { order, posts, hasNext, firstInaccessiblePostTime, incompletePayload }
}

// Fetch all posts with pagination, respecting limit and since
export async function getAllChannelPosts(
  channelId: string,
  options: {
    limit?: number
    since?: number
    boundary?: { createAt: number; id: string }
    safeBeforePostId?: string
  } = {},
): Promise<PostRetrievalResult> {
  const { limit = 50, since, boundary, safeBeforePostId } = options
  const postsById = new Map<string, Post>()
  const seenIds = new Set<string>()
  const target = limit + 1
  const pageSize = Math.min(target, 200)
  let page = 0
  let stagnantPages = 0
  let exhausted = false
  let uncertain = false
  let activeBeforePostId = safeBeforePostId
  let retriedWithoutMissingAnchor = false

  while (true) {
    const pageResult = await getChannelPosts(channelId, {
      limit: pageSize,
      page,
      before: activeBeforePostId,
    })
    const posts = pageResult.posts
    if (pageResult.firstInaccessiblePostTime !== null) uncertain = true
    if (pageResult.incompletePayload) uncertain = true

    if (pageResult.rawCount === 0) {
      if (page === 0 && activeBeforePostId !== undefined && !retriedWithoutMissingAnchor) {
        activeBeforePostId = undefined
        retriedWithoutMissingAnchor = true
        stagnantPages = 0
        continue
      }
      if (pageResult.hasNext === true) uncertain = true
      exhausted = !uncertain
      break
    }

    let madeProgress = false
    for (const post of posts) {
      if (seenIds.has(post.id)) continue
      seenIds.add(post.id)
      madeProgress = true
      const afterBoundary =
        boundary === undefined ||
        post.create_at < boundary.createAt ||
        (post.create_at === boundary.createAt && comparePostIds(post.id, boundary.id) > 0)
      if ((since === undefined || post.create_at >= since) && afterBoundary) {
        postsById.set(post.id, post)
      }
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
    safeBeforeValid: safeBeforePostId === undefined || !retriedWithoutMissingAnchor,
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
  const posts = new Map<string, Post>()
  let fromPost: string | undefined
  let fromCreateAt: number | undefined
  let uncertain = false
  let stagnantPages = 0
  let successfulPages = 0

  while (true) {
    const params = new URLSearchParams({ perPage: '200', direction: 'down' })
    if (fromPost !== undefined) params.set('fromPost', fromPost)
    if (fromCreateAt !== undefined) params.set('fromCreateAt', String(fromCreateAt))
    let response: ReturnType<typeof normalizeOrderedPostsPage>
    try {
      response = normalizeOrderedPostsPage(
        await client.get<unknown>(`/posts/${encodeURIComponent(postId)}/thread?${params}`),
      )
    } catch (error) {
      if (successfulPages === 0) throw error
      return { posts: [...posts.values()], truncated: null }
    }
    successfulPages += 1

    if (response.firstInaccessiblePostTime !== null || response.incompletePayload) uncertain = true
    let added = 0
    for (const id of response.order) {
      const post = response.posts[id]
      if (!post || post.delete_at !== 0 || posts.has(id)) continue
      posts.set(id, post)
      added += 1
    }

    if (response.hasNext !== true) {
      const visible = [...posts.values()]
      const root = visible.find((post) => !post.root_id)
      const visibleReplyCount = root ? visible.filter((post) => post.root_id === root.id).length : 0
      const legacyComplete =
        response.hasNext === null &&
        !response.incompletePayload &&
        root !== undefined &&
        visibleReplyCount >= root.reply_count
      return {
        posts: visible,
        truncated: uncertain ? null : response.hasNext === false || legacyComplete ? false : null,
      }
    }

    const cursorPost = [...response.order]
      .reverse()
      .map((id) => response.posts[id])
      .find((post): post is Post => !!post)
    if (!cursorPost) {
      return { posts: [...posts.values()], truncated: uncertain ? null : true }
    }

    const nextFromPost = cursorPost.id
    const nextFromCreateAt = cursorPost.create_at
    const cursorAdvanced = nextFromPost !== fromPost || nextFromCreateAt !== fromCreateAt
    stagnantPages = added > 0 && cursorAdvanced ? 0 : stagnantPages + 1
    if (stagnantPages >= 2) {
      return { posts: [...posts.values()], truncated: uncertain ? null : true }
    }
    fromPost = nextFromPost
    fromCreateAt = nextFromCreateAt
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
    const rawResponse = await client.post<unknown>(
      `/teams/${encodeURIComponent(teamId)}/posts/search`,
      {
        terms,
        is_or_search: false,
        page,
        per_page: perPage,
      },
      true,
    )
    const response = objectRecord(rawResponse)
    if (!response || !Array.isArray(response.order)) {
      throw new Error('Mattermost returned an invalid search response.')
    }
    const responseOrder = response.order.filter((id): id is string => typeof id === 'string')
    if (responseOrder.length !== response.order.length) uncertain = true
    const responsePosts = objectRecord(response.posts)
    if (!responsePosts && responseOrder.length > 0) uncertain = true
    const responseMatches = objectRecord(response.matches)
    const hasNext = typeof response.has_next === 'boolean' ? response.has_next : undefined
    if (
      typeof response.first_inaccessible_post_time === 'number' &&
      response.first_inaccessible_post_time > 0
    ) {
      uncertain = true
    }
    if (hasNext === true && responseOrder.length === 0) uncertain = true
    let madeProgress = false
    const acceptedThisPage: Post[] = []
    for (const id of responseOrder) {
      if (seenIds.has(id)) continue
      seenIds.add(id)
      madeProgress = true
      const candidate = responsePosts?.[id]
      if (!candidate || typeof candidate !== 'object' || Array.isArray(candidate)) {
        uncertain = true
        continue
      }
      const post = candidate as Post
      if (post.delete_at !== 0 || !accept(post)) continue
      posts.set(id, post)
      acceptedThisPage.push(post)
      order.push(id)
      const rawMatches = responseMatches?.[id]
      if (Array.isArray(rawMatches)) {
        matches[id] = rawMatches.filter((match): match is string => typeof match === 'string')
      }
    }
    page += 1
    if (madeProgress) stagnantPages = 0
    else stagnantPages += 1

    // Search paging is only fully honored by Elasticsearch-backed servers. A short page is not
    // proof of exhaustion, so rely on an empty response or bounded stagnation instead.
    if (responseOrder.length === 0) {
      exhausted = !uncertain
      break
    }
    if (hasNext === false) {
      exhausted = !uncertain
      break
    }
    if (page >= MAX_SEARCH_SCAN_PAGES) {
      uncertain = true
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
    throw new Error('Invalid duration format. Use formats like "24h", "7d", "1w", "2m".')
  }

  const valueText = match[1]
  const unitText = match[2]
  if (!valueText || !unitText) {
    throw new Error('Invalid duration format.')
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
