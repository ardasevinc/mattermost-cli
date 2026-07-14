import { afterEach, describe, expect, test, vi } from 'vitest'
import { getMyDMChannels } from '../../src/api/channels'
import { initClient } from '../../src/api/client'
import { getAllChannelPosts, searchPosts, takeMostRecentPosts } from '../../src/api/posts'
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
  test('paginates without server since and enforces exact local time, dedupe, and hard limit', async () => {
    let calls = 0
    const { requests } = installRouteFetch([
      {
        method: 'GET',
        path: '/api/v4/channels/channel/posts',
        handle: () => {
          calls += 1
          if (calls === 1) {
            return page([post('p4', 130), post('p3', 120), post('p3', 120), post('p2', 110)])
          }
          if (calls === 2) {
            return page([post('p2', 110), post('p1', 100), post('old', 99), post('older', 98)])
          }
          return page([post('old-2', 97), post('old-3', 96), post('old-4', 95), post('old-5', 94)])
        },
      },
    ])
    initClient('https://mattermost.test', 'token')

    const result = await getAllChannelPosts('channel', { since: 100, limit: 4 })

    expect(result.map(({ id }) => id)).toEqual(['p4', 'p3', 'p2', 'p1'])
    expect(requests).toHaveLength(3)
    expect(requests.every(({ url }) => !url.searchParams.has('since'))).toBe(true)
    expect(requests.map(({ url }) => url.searchParams.get('page'))).toEqual(['0', '1', '2'])
  })

  test('dedupes overlapping pages and stops when pagination makes no progress', async () => {
    const { requests } = installRouteFetch([
      {
        method: 'GET',
        path: '/api/v4/channels/channel/posts',
        handle: () => page([post('p2', 2), post('p1', 1), post('p1', 1)]),
      },
    ])
    initClient('https://mattermost.test', 'token')

    expect((await getAllChannelPosts('channel', { limit: 3 })).map(({ id }) => id)).toEqual([
      'p2',
      'p1',
    ])
    expect(requests).toHaveLength(3)
    expect(requests[2]?.url.searchParams.get('page')).toBe('2')
  })

  test('does not skip posts sharing the timestamp at a page boundary', async () => {
    const { requests } = installRouteFetch([
      {
        method: 'GET',
        path: '/api/v4/channels/channel/posts',
        handle: ({ url }) => {
          const pageNumber = Number(url.searchParams.get('page'))
          if (pageNumber === 0) return page([post('d', 100), post('c', 100)])
          if (pageNumber === 1) return page([post('d', 100), post('c', 100)])
          if (pageNumber === 2) return page([post('b', 100), post('a', 100)])
          return page([post('old', 99)])
        },
      },
    ])
    initClient('https://mattermost.test', 'token')

    const result = await getAllChannelPosts('channel', { limit: 2 })

    expect(result.map(({ id }) => id)).toEqual(['a', 'b'])
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
    expect(requests.map(({ body }) => (body as { page: number }).page)).toEqual([0, 1])
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
    expect(requests.map(({ body }) => (body as { page: number }).page)).toEqual([0, 1, 2])
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

    const response = await searchPosts('team', '@arda', 2, () => true, {
      completeCutoffTies: true,
    })

    const selected = response.order
      .map((id) => response.posts[id])
      .filter((candidate): candidate is Post => !!candidate)
    expect(takeMostRecentPosts(selected, 2).map(({ id }) => id)).toEqual(['a', 'b'])
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
    expect(requests.map(({ body }) => (body as { page: number }).page)).toEqual([0, 1, 2])
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
    expect(requests.map(({ body }) => (body as { page: number }).page)).toEqual([0, 1])
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
