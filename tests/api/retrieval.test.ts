import { afterEach, describe, expect, test, vi } from 'vitest'
import { getMyDMChannels } from '../../src/api/channels'
import { initClient } from '../../src/api/client'
import {
  getAllChannelPosts,
  getChannelPosts,
  getPostThread,
  searchPosts,
  takeMostRecentPosts,
} from '../../src/api/posts'
import type { Channel, Post, PostsResponse, SearchResponse } from '../../src/types'
import { installRouteFetch } from '../helpers/fake-fetch'

function post(id: string, createAt: number): Post {
  return { id, create_at: createAt, delete_at: 0, channel_id: 'channel', message: id } as Post
}

function page(items: Post[]): PostsResponse {
  return {
    order: items.map(({ id }) => id),
    posts: Object.fromEntries(items.map((item) => [item.id, item])),
  } as PostsResponse
}

afterEach(() => vi.unstubAllGlobals())

describe('route-aware retrieval integration', () => {
  test('uses limit plus one and proves channel truncation locally', async () => {
    const { requests } = installRouteFetch([
      {
        method: 'GET',
        path: '/api/v4/channels/channel/posts',
        handle: () => page([post('new', 3), post('selected', 2), post('extra', 1)]),
      },
    ])
    initClient('https://mattermost.test', 'token')

    const result = await getAllChannelPosts('channel', { limit: 2 })

    expect(result.posts.map(({ id }) => id)).toEqual(['new', 'selected'])
    expect(result.truncated).toBe(true)
    expect(requests).toHaveLength(1)
    expect(requests[0]?.url.searchParams.get('per_page')).toBe('3')
  })

  test('resumes locally across equal-millisecond peers without admitting newer posts', async () => {
    installRouteFetch([
      {
        method: 'GET',
        path: '/api/v4/channels/channel/posts',
        handle: ({ url }) =>
          url.searchParams.get('page') === '0'
            ? page([
                post('arrived-later', 300),
                post('anchor', 200),
                post('peer-after-anchor', 200),
              ])
            : page([post('older', 100)]),
      },
    ])
    initClient('https://mattermost.test', 'token')

    const result = await getAllChannelPosts('channel', {
      limit: 2,
      boundary: { createAt: 200, id: 'anchor' },
    })

    expect(result.posts.map(({ id }) => id)).toEqual(['peer-after-anchor', 'older'])
    expect(result.truncated).toBe(false)
  })

  test('uses a safe before anchor while retaining the local boundary filter', async () => {
    const { requests } = installRouteFetch([
      {
        method: 'GET',
        path: '/api/v4/channels/channel/posts',
        handle: () => page([post('peer', 200), post('older', 100)]),
      },
    ])
    initClient('https://mattermost.test', 'token')

    const result = await getAllChannelPosts('channel', {
      limit: 2,
      boundary: { createAt: 200, id: 'anchor' },
      safeBeforePostId: 'safe-newer',
    })

    expect(requests[0]?.url.searchParams.get('before')).toBe('safe-newer')
    expect(result.posts.map(({ id }) => id)).toEqual(['peer', 'older'])
  })

  test('carries the safe anchor across bounded deep mixed-timestamp pages', async () => {
    const { requests } = installRouteFetch([
      {
        method: 'GET',
        path: '/api/v4/channels/channel/posts',
        handle: ({ url }) => {
          const pageNumber = Number(url.searchParams.get('page'))
          if (pageNumber === 0) {
            return page([
              post('newest-than-boundary', 400),
              post('newer-than-boundary', 300),
              post('anchor', 200),
            ])
          }
          if (pageNumber === 1) return page([post('peer', 200), post('older', 100)])
          return page([])
        },
      },
    ])
    initClient('https://mattermost.test', 'token')

    const result = await getAllChannelPosts('channel', {
      limit: 2,
      boundary: { createAt: 200, id: 'anchor' },
      safeBeforePostId: 'safe-newer',
    })

    expect(result.posts.map(({ id }) => id)).toEqual(['peer', 'older'])
    expect(requests).toHaveLength(2)
    expect(requests.every(({ url }) => url.searchParams.get('before') === 'safe-newer')).toBe(true)
  })

  test('retries page zero once without a deleted safe anchor and recovers remaining history', async () => {
    const { requests } = installRouteFetch([
      {
        method: 'GET',
        path: '/api/v4/channels/channel/posts',
        handle: ({ url }) =>
          url.searchParams.has('before') ? page([]) : page([post('peer', 200), post('older', 100)]),
      },
    ])
    initClient('https://mattermost.test', 'token')

    const result = await getAllChannelPosts('channel', {
      limit: 2,
      boundary: { createAt: 200, id: 'anchor' },
      safeBeforePostId: 'deleted-safe-anchor',
    })

    expect(result.posts.map(({ id }) => id)).toEqual(['peer', 'older'])
    expect(result.truncated).toBe(false)
    expect(result.safeBeforeValid).toBe(false)
    expect(requests).toHaveLength(2)
    expect(requests.map(({ url }) => url.searchParams.get('before'))).toEqual([
      'deleted-safe-anchor',
      null,
    ])
    expect(requests.every(({ url }) => url.searchParams.get('page') === '0')).toBe(true)
  })

  test('reports unknown after two full stagnant channel pages', async () => {
    const { requests } = installRouteFetch([
      {
        method: 'GET',
        path: '/api/v4/channels/channel/posts',
        handle: () => page([post('a', 2), post('b', 1), post('a', 2), post('b', 1)]),
      },
    ])
    initClient('https://mattermost.test', 'token')

    const result = await getAllChannelPosts('channel', { limit: 3 })

    expect(result.truncated).toBeNull()
    expect(requests).toHaveLength(3)
  })

  test('requests channel pages without server-side thread expansion', async () => {
    const { requests } = installRouteFetch([
      {
        method: 'GET',
        path: '/api/v4/channels/channel/posts',
        handle: () => page([]),
      },
    ])
    initClient('https://mattermost.test', 'token')

    await getChannelPosts('channel')

    expect(requests[0]?.url.searchParams.get('skipFetchThreads')).toBe('true')
  })

  test('continues after a raw full channel page whose posts are missing or deleted', async () => {
    const { requests } = installRouteFetch([
      {
        method: 'GET',
        path: '/api/v4/channels/channel/posts',
        handle: ({ url }) =>
          url.searchParams.get('page') === '0'
            ? {
                order: ['missing-a', 'deleted', 'missing-b'],
                posts: { deleted: { ...post('deleted', 3), delete_at: 1 } },
              }
            : page([post('live', 2)]),
      },
    ])
    initClient('https://mattermost.test', 'token')

    const result = await getAllChannelPosts('channel', { limit: 2 })

    expect(result.posts.map(({ id }) => id)).toEqual(['live'])
    expect(result.truncated).toBe(false)
    expect(requests).toHaveLength(2)
  })

  test('does not claim channel exhaustion past an inaccessible post boundary', async () => {
    installRouteFetch([
      {
        method: 'GET',
        path: '/api/v4/channels/channel/posts',
        handle: () => ({ ...page([post('visible', 2)]), first_inaccessible_post_time: 1 }),
      },
    ])
    initClient('https://mattermost.test', 'token')

    expect((await getAllChannelPosts('channel', { limit: 2 })).truncated).toBeNull()
  })

  test('only marks a thread complete when the API explicitly reports no next page', async () => {
    let hasNext: boolean | undefined = false
    let inaccessibleTime: number | undefined
    installRouteFetch([
      {
        method: 'GET',
        path: '/api/v4/posts/root/thread',
        handle: () => ({
          ...page([post('root', 1)]),
          has_next: hasNext,
          first_inaccessible_post_time: inaccessibleTime,
        }),
      },
    ])
    initClient('https://mattermost.test', 'token')

    expect((await getPostThread('root')).truncated).toBe(false)
    hasNext = true
    expect((await getPostThread('root')).truncated).toBe(true)
    hasNext = undefined
    expect((await getPostThread('root')).truncated).toBeNull()
    hasNext = false
    inaccessibleTime = 1
    expect((await getPostThread('root')).truncated).toBeNull()
  })

  test('proves legacy thread completeness only from root reply counts', async () => {
    const completeRoot = { ...post('complete-root', 1), root_id: '', reply_count: 1 }
    const completeReply = { ...post('complete-reply', 2), root_id: 'complete-root', reply_count: 0 }
    const partialRoot = { ...post('partial-root', 1), root_id: '', reply_count: 2 }
    const partialReply = { ...post('partial-reply', 2), root_id: 'partial-root', reply_count: 0 }
    installRouteFetch([
      {
        method: 'GET',
        path: '/api/v4/posts/complete-root/thread',
        handle: () => page([completeRoot, completeReply]),
      },
      {
        method: 'GET',
        path: '/api/v4/posts/partial-root/thread',
        handle: () => page([partialRoot, partialReply]),
      },
    ])
    initClient('https://mattermost.test', 'token')

    expect((await getPostThread('complete-root')).truncated).toBe(false)
    expect((await getPostThread('partial-root')).truncated).toBeNull()
  })

  test('paginates threads with Mattermost cursor casing and dedupes posts', async () => {
    const root = { ...post('root', 1), root_id: '', reply_count: 2 }
    const firstReply = { ...post('reply-1', 2), root_id: 'root', reply_count: 0 }
    const secondReply = { ...post('reply-2', 3), root_id: 'root', reply_count: 0 }
    const { requests } = installRouteFetch([
      {
        method: 'GET',
        path: '/api/v4/posts/root/thread',
        handle: ({ url }) =>
          url.searchParams.has('fromPost')
            ? { ...page([root, firstReply, secondReply]), has_next: false }
            : { ...page([root, firstReply]), has_next: true },
      },
    ])
    initClient('https://mattermost.test', 'token')

    const result = await getPostThread('root')

    expect(result.posts.map(({ id }) => id)).toEqual(['root', 'reply-1', 'reply-2'])
    expect(result.truncated).toBe(false)
    expect(requests).toHaveLength(2)
    expect(requests[0]?.url.searchParams.get('perPage')).toBe('200')
    expect(requests[0]?.url.searchParams.get('direction')).toBe('down')
    expect(requests[1]?.url.searchParams.get('fromPost')).toBe('reply-1')
    expect(requests[1]?.url.searchParams.get('fromCreateAt')).toBe('2')
  })

  test('recovers after an inclusive duplicate-only page advances the cursor', async () => {
    const root = { ...post('root', 1), root_id: '', reply_count: 2 }
    const firstReply = { ...post('reply-1', 2), root_id: 'root', reply_count: 0 }
    const duplicateCursor = { ...root, id: 'cursor', create_at: 3 }
    const secondReply = { ...post('reply-2', 4), root_id: 'root', reply_count: 0 }
    let pageNumber = 0
    const { requests } = installRouteFetch([
      {
        method: 'GET',
        path: '/api/v4/posts/root/thread',
        handle: () => {
          pageNumber += 1
          if (pageNumber === 1)
            return { ...page([root, firstReply, duplicateCursor]), has_next: true }
          if (pageNumber === 2) return { ...page([duplicateCursor, firstReply]), has_next: true }
          return { ...page([duplicateCursor, secondReply]), has_next: false }
        },
      },
    ])
    initClient('https://mattermost.test', 'token')

    const result = await getPostThread('root')

    expect(result.posts.map(({ id }) => id)).toEqual(['root', 'reply-1', 'cursor', 'reply-2'])
    expect(result.truncated).toBe(false)
    expect(requests).toHaveLength(3)
  })

  test('terminates after bounded duplicate-only cursor stagnation', async () => {
    const root = { ...post('root', 1), root_id: '', reply_count: 2 }
    const { requests } = installRouteFetch([
      {
        method: 'GET',
        path: '/api/v4/posts/root/thread',
        handle: () => ({ ...page([root]), has_next: true }),
      },
    ])
    initClient('https://mattermost.test', 'token')

    expect((await getPostThread('root')).truncated).toBe(true)
    expect(requests).toHaveLength(3)
  })

  test('search proves local truncation after accepted filtering and dedupe', async () => {
    installRouteFetch([
      {
        method: 'POST',
        path: '/api/v4/teams/team/posts/search',
        handle: () => ({
          ...page([post('new', 3), post('selected', 2), post('extra', 1)]),
          matches: { new: ['n'], selected: ['s'], extra: ['e'] },
        }),
      },
    ])
    initClient('https://mattermost.test', 'token')

    const result = await searchPosts('team', 'needle', 2)

    expect(result.truncated).toBe(true)
    expect(result.order).toEqual(['new', 'selected'])
    expect(Object.keys(result.matches)).toEqual(['new', 'selected'])
  })

  test('an empty search response proves exhaustion', async () => {
    installRouteFetch([
      {
        method: 'POST',
        path: '/api/v4/teams/team/posts/search',
        handle: () => ({ ...page([]), matches: {} }),
      },
    ])
    initClient('https://mattermost.test', 'token')

    expect((await searchPosts('team', 'needle', 2)).truncated).toBe(false)
  })

  test('does not claim search exhaustion past an inaccessible post boundary', async () => {
    installRouteFetch([
      {
        method: 'POST',
        path: '/api/v4/teams/team/posts/search',
        handle: () => ({
          ...page([]),
          matches: {},
          first_inaccessible_post_time: 1,
          has_next: false,
        }),
      },
    ])
    initClient('https://mattermost.test', 'token')

    expect((await searchPosts('team', 'needle', 2)).truncated).toBeNull()
  })

  test('paginates without server since and enforces exact local time, dedupe, and hard limit', async () => {
    let calls = 0
    const { requests } = installRouteFetch([
      {
        method: 'GET',
        path: '/api/v4/channels/channel/posts',
        handle: () => {
          calls += 1
          if (calls === 1) {
            return page([
              post('p4', 130),
              post('p3', 120),
              post('p2', 110),
              post('p1', 100),
              post('old', 99),
            ])
          }
          return page([])
        },
      },
    ])
    initClient('https://mattermost.test', 'token')

    const result = await getAllChannelPosts('channel', { since: 100, limit: 4 })

    expect(result.posts.map(({ id }) => id)).toEqual(['p4', 'p3', 'p2', 'p1'])
    expect(result.truncated).toBe(false)
    expect(requests).toHaveLength(2)
    expect(requests.every(({ url }) => !url.searchParams.has('since'))).toBe(true)
    expect(requests.map(({ url }) => url.searchParams.get('page'))).toEqual(['0', '1'])
    expect(requests.every(({ url }) => url.searchParams.get('skipFetchThreads') === 'true')).toBe(
      true,
    )
  })

  test('treats a short channel page as exhausted without an extra request', async () => {
    const { requests } = installRouteFetch([
      {
        method: 'GET',
        path: '/api/v4/channels/channel/posts',
        handle: () => page([post('p2', 2), post('p1', 1), post('p1', 1)]),
      },
    ])
    initClient('https://mattermost.test', 'token')

    const result = await getAllChannelPosts('channel', { limit: 3 })
    expect(result.posts.map(({ id }) => id)).toEqual(['p2', 'p1'])
    expect(requests).toHaveLength(1)
    expect(result.truncated).toBe(false)
  })

  test('does not skip posts sharing the timestamp at a page boundary', async () => {
    const { requests } = installRouteFetch([
      {
        method: 'GET',
        path: '/api/v4/channels/channel/posts',
        handle: ({ url }) => {
          const pageNumber = Number(url.searchParams.get('page'))
          if (pageNumber === 0) return page([post('d', 100), post('c', 100), post('d', 100)])
          if (pageNumber === 1) return page([post('d', 100), post('c', 100), post('d', 100)])
          if (pageNumber === 2) return page([post('b', 100), post('a', 100), post('b', 100)])
          return page([post('old', 99)])
        },
      },
    ])
    initClient('https://mattermost.test', 'token')

    const result = await getAllChannelPosts('channel', { limit: 2 })

    expect(result.posts.map(({ id }) => id)).toEqual(['a', 'b'])
    expect(requests.map(({ url }) => url.searchParams.get('page'))).toEqual(['0', '1', '2', '3'])
    expect(requests.every(({ url }) => !url.searchParams.has('before'))).toBe(true)
  })

  test('paginates search with a global limit and ID dedupe', async () => {
    const { requests } = installRouteFetch([
      {
        method: 'POST',
        path: '/api/v4/teams/team/posts/search',
        handle: ({ body }) => {
          const pageNumber = (body as { page: number }).page
          const items =
            pageNumber === 0
              ? [post('a', 3), post('b', 2), post('a', 3)]
              : [post('b', 2), post('c', 1)]
          return { ...page(items), matches: {} } as SearchResponse
        },
      },
    ])
    initClient('https://mattermost.test', 'token')

    const result = await searchPosts('team', 'needle', 3)

    expect(result.order).toEqual(['a', 'b', 'c'])
    expect(result.truncated).toBeNull()
    expect(requests.map(({ body }) => (body as { page: number }).page)).toEqual([0, 1, 2, 3])
  })

  test('search tolerates a duplicate-only full page before later progress', async () => {
    const { requests } = installRouteFetch([
      {
        method: 'POST',
        path: '/api/v4/teams/team/posts/search',
        handle: ({ body }) => {
          const pageNumber = (body as { page: number }).page
          const items =
            pageNumber === 0
              ? [post('d', 4), post('c', 3), post('d', 4), post('c', 3)]
              : pageNumber === 1
                ? [post('d', 4), post('c', 3), post('d', 4), post('c', 3)]
                : [post('b', 2), post('a', 1), post('b', 2), post('a', 1)]
          return { ...page(items), matches: {} }
        },
      },
    ])
    initClient('https://mattermost.test', 'token')

    const result = await searchPosts('team', 'needle', 4)

    expect(result.order).toEqual(['d', 'c', 'b', 'a'])
    expect(result.truncated).toBeNull()
    expect(requests.map(({ body }) => (body as { page: number }).page)).toEqual([0, 1, 2, 3, 4])
  })

  test('mention-mode search completes equal-time cutoff ties', async () => {
    const { requests } = installRouteFetch([
      {
        method: 'POST',
        path: '/api/v4/teams/team/posts/search',
        handle: ({ body }) => {
          const pageNumber = (body as { page: number }).page
          const items =
            pageNumber === 0
              ? [post('d', 100), post('c', 100)]
              : pageNumber === 1
                ? [post('b', 100), post('a', 100)]
                : []
          return { ...page(items), matches: {} }
        },
      },
    ])
    initClient('https://mattermost.test', 'token')

    const response = await searchPosts('team', '@arda', 2, () => true)

    const selected = response.order
      .map((id) => response.posts[id])
      .filter((candidate): candidate is Post => !!candidate)
    expect(takeMostRecentPosts(selected, 2).map(({ id }) => id)).toEqual(['a', 'b'])
    expect(response.truncated).toBe(true)
    expect(requests.map(({ body }) => (body as { page: number }).page)).toEqual([0, 1, 2])
  })

  test('search ignores missing and deleted hits and continues after a short page', async () => {
    const { requests } = installRouteFetch([
      {
        method: 'POST',
        path: '/api/v4/teams/team/posts/search',
        handle: ({ body }) => {
          const pageNumber = (body as { page: number }).page
          if (pageNumber === 0) {
            return {
              order: ['missing', 'deleted'],
              posts: { deleted: { ...post('deleted', 3), delete_at: 1 } },
              matches: {},
            }
          }
          const items =
            pageNumber === 1 ? [post('live-a', 2)] : pageNumber === 2 ? [post('live-b', 1)] : []
          return { ...page(items), matches: {} }
        },
      },
    ])
    initClient('https://mattermost.test', 'token')

    const result = await searchPosts('team', 'needle', 2)

    expect(result.order).toEqual(['live-a', 'live-b'])
    expect(result.truncated).toBe(false)
    expect(requests.map(({ body }) => (body as { page: number }).page)).toEqual([0, 1, 2, 3])
  })

  test('search continues to page two when exact local filtering rejects page one', async () => {
    const { requests } = installRouteFetch([
      {
        method: 'POST',
        path: '/api/v4/teams/team/posts/search',
        handle: ({ body }) => {
          const pageNumber = (body as { page: number }).page
          const item =
            pageNumber === 0
              ? { ...post('too-old', 999), message: '@arda' }
              : { ...post('exact-boundary', 1000), message: '@arda' }
          return { ...page([item]), matches: {} }
        },
      },
    ])
    initClient('https://mattermost.test', 'token')

    const result = await searchPosts(
      'team',
      '@arda after:1970-01-01',
      1,
      (candidate) => candidate.create_at >= 1000 && candidate.message === '@arda',
    )

    expect(result.order).toEqual(['exact-boundary'])
    expect(result.truncated).toBeNull()
    expect(requests.map(({ body }) => (body as { page: number }).page)).toEqual([0, 1, 2, 3])
  })

  test('dedupes direct channels returned by Mattermost', async () => {
    const dm = { id: 'dm', type: 'D', name: 'me__you' } as Channel
    installRouteFetch([
      {
        method: 'GET',
        path: '/api/v4/users/me',
        handle: () => ({ id: 'me', username: 'me' }),
      },
      { method: 'GET', path: '/api/v4/users/me/channels', handle: () => [dm, dm] },
    ])
    initClient('https://mattermost.test', 'token')

    expect(await getMyDMChannels()).toEqual([dm])
  })
})
