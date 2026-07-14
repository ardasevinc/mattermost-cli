import { afterEach, describe, expect, test, vi } from 'vitest'
import { clearUserCache } from '../src/api/users'
import { fetchChannel, fetchDMs, fetchGroupDMs } from '../src/cli'
import { decodeChannelHistoryCursor, encodeChannelHistoryCursor } from '../src/cursor'
import type { Channel, ChannelOptions, Post, PostsResponse } from '../src/types'
import { installRouteFetch } from './helpers/fake-fetch'

const serverUrl = 'https://mattermost.test'
const me = { id: 'me', username: 'me' }
const team = { id: 'team', name: 'team', display_name: 'Team', type: 'O' }
const general = {
  id: 'general',
  team_id: 'team',
  type: 'O',
  name: 'general',
  display_name: 'General',
  header: '',
  purpose: '',
  last_post_at: 0,
  total_msg_count: 0,
  creator_id: 'me',
} satisfies Channel
const direct = { ...general, id: 'dm', type: 'D', name: 'me__other' } satisfies Channel
const group = {
  ...general,
  id: 'group',
  type: 'G',
  name: 'group',
  display_name: 'Crew',
} satisfies Channel

function post(id: string, createAt: number, overrides: Partial<Post> = {}): Post {
  return {
    id,
    channel_id: 'general',
    user_id: 'me',
    create_at: createAt,
    update_at: createAt,
    delete_at: 0,
    edit_at: 0,
    message: id,
    type: '',
    props: {},
    hashtags: '',
    file_ids: [],
    root_id: '',
    reply_count: 0,
    pending_post_id: '',
    ...overrides,
  }
}

function page(items: Post[], extra: Partial<PostsResponse> = {}): PostsResponse {
  return {
    order: items.map(({ id }) => id),
    posts: Object.fromEntries(items.map((item) => [item.id, item])),
    ...extra,
  } as PostsResponse
}

const options = (overrides: Partial<ChannelOptions> = {}): ChannelOptions => ({
  url: serverUrl,
  token: 'token',
  json: true,
  color: false,
  relative: false,
  redact: true,
  threads: false,
  channel: 'general',
  limit: 2,
  since: '',
  ...overrides,
})

function routes(handlePosts: (url: URL) => PostsResponse, resolved = general) {
  return [
    { method: 'GET', path: '/api/v4/users/me', handle: () => me },
    { method: 'GET', path: '/api/v4/users/me/teams', handle: () => [team] },
    {
      method: 'GET',
      path: '/api/v4/teams/team/channels/name/general',
      handle: () => resolved,
    },
    {
      method: 'GET',
      path: '/api/v4/channels/general/posts',
      handle: ({ url }: { url: URL }) => handlePosts(url),
    },
  ]
}

function captureJSON() {
  const log = vi.spyOn(console, 'log').mockImplementation(() => undefined)
  return () => JSON.parse(String(log.mock.calls.at(-1)?.[0]))
}

afterEach(() => {
  clearUserCache()
  vi.useRealTimers()
  vi.restoreAllMocks()
  vi.unstubAllGlobals()
})

describe('channel history cursor integration', () => {
  test('pages across equal timestamps without gaps, duplicates, or newly arrived posts', async () => {
    vi.useFakeTimers()
    vi.setSystemTime(new Date('2026-07-14T12:00:00Z'))
    installRouteFetch(
      routes((url) =>
        url.searchParams.get('page') === '0'
          ? page([
              post('newer', Date.now()),
              post('peer-a', Date.now() - 1),
              post('peer-b', Date.now() - 1),
            ])
          : page([post('older', Date.now() - 2)]),
      ),
    )
    const firstOutput = captureJSON()
    await fetchChannel(options({ since: '24h' }))
    const first = firstOutput()[0]
    const cursor = first.retrieval.selection.nextCursor as string
    const decoded = decodeChannelHistoryCursor(cursor)
    expect(first.retrieval.selection.selectedCount).toBe(2)
    expect(decoded.since).toBe(Date.now() - 86_400_000)
    expect(decoded.safeBeforePostId).toBe('newer')

    vi.restoreAllMocks()
    const { requests } = installRouteFetch(
      routes((url) =>
        url.searchParams.get('page') === '0'
          ? page([
              post('arrived-after-page-one', Date.now() + 10),
              post('peer-b', Date.now() - 1),
              post('older', Date.now() - 2),
            ])
          : page([]),
      ),
    )
    const secondOutput = captureJSON()
    await fetchChannel(options({ cursor }))
    const second = secondOutput()[0]
    expect(second.messages.map(({ id }: { id: string }) => id).sort()).toEqual(['older', 'peer-b'])
    expect(second.retrieval.selection.inputCursor).toBe(cursor)
    expect(second.retrieval.selection.nextCursor).toBeNull()
    expect(second.retrieval.selection.since).toBe('2026-07-13T12:00:00.000Z')
    expect(
      requests.find(({ url }) => url.pathname.endsWith('/posts'))?.url.searchParams.get('before'),
    ).toBe('newer')
  })

  test('preserves an unknown empty resume as an unchanged retry cursor', async () => {
    const cursor = encodeChannelHistoryCursor({
      v: 1,
      scope: 'channel',
      channelId: 'general',
      boundary: { createAt: 100, id: 'anchor' },
      since: null,
    })
    installRouteFetch(routes(() => page([], { first_inaccessible_post_time: 1 })))
    const output = captureJSON()

    await fetchChannel(options({ cursor }))

    expect(output()).toEqual([
      expect.objectContaining({
        messages: [],
        retrieval: expect.objectContaining({
          visiblePostCount: 0,
          selection: expect.objectContaining({
            selectedCount: 0,
            queryTruncated: null,
            inputCursor: cursor,
            nextCursor: cursor,
          }),
        }),
      }),
    ])
  })

  test('keeps a next cursor for a nonempty result with unknown completeness', async () => {
    installRouteFetch(
      routes(() => page([post('visible', 100)], { first_inaccessible_post_time: 1 })),
    )
    const output = captureJSON()

    await fetchChannel(options())

    const selection = output()[0].retrieval.selection
    expect(selection).toMatchObject({ selectedCount: 1, queryTruncated: null, inputCursor: null })
    expect(selection.nextCursor).toEqual(expect.any(String))
  })

  test('drops a dead safe anchor from the regenerated cursor and the following resume', async () => {
    const inputCursor = encodeChannelHistoryCursor({
      v: 1,
      scope: 'channel',
      channelId: 'general',
      boundary: { createAt: 200, id: 'anchor' },
      since: null,
      safeBeforePostId: 'deleted-safe-anchor',
    })
    const firstRequests = installRouteFetch(
      routes((url) =>
        url.searchParams.has('before')
          ? page([])
          : page([post('peer-b', 200), post('peer-c', 200), post('older', 100)]),
      ),
    ).requests
    const firstOutput = captureJSON()

    await fetchChannel(options({ cursor: inputCursor, limit: 1 }))

    const regenerated = firstOutput()[0].retrieval.selection.nextCursor as string
    expect(decodeChannelHistoryCursor(regenerated).safeBeforePostId).toBeUndefined()
    expect(
      firstRequests
        .filter(({ url }) => url.pathname.endsWith('/posts'))
        .map(({ url }) => url.searchParams.get('before')),
    ).toEqual(['deleted-safe-anchor', null])

    vi.restoreAllMocks()
    const secondRequests = installRouteFetch(
      routes(() => page([post('peer-c', 200), post('older', 100)])),
    ).requests
    const secondOutput = captureJSON()

    await fetchChannel(options({ cursor: regenerated, limit: 1 }))

    expect(
      secondRequests
        .filter(({ url }) => url.pathname.endsWith('/posts'))
        .every(({ url }) => !url.searchParams.has('before')),
    ).toBe(true)
    expect(secondOutput()[0].messages[0].id).toBe('peer-c')
  })

  test('selects the cursor page before hydrating threads', async () => {
    const root = post('root', 300, { reply_count: 1 })
    const sibling = post('sibling', 200)
    const reply = post('reply', 400, { root_id: 'root' })
    const { requests } = installRouteFetch([
      ...routes(() => page([root, sibling])),
      {
        method: 'GET',
        path: '/api/v4/posts/root/thread',
        handle: () => page([root, reply], { has_next: false }),
      },
    ])
    const output = captureJSON()

    await fetchChannel(options({ limit: 1, threads: true }))

    const result = output()[0]
    expect(result.retrieval.selection).toMatchObject({ selectedCount: 1, queryTruncated: true })
    expect(decodeChannelHistoryCursor(result.retrieval.selection.nextCursor).boundary.id).toBe(
      'root',
    )
    expect(result.retrieval.visiblePostCount).toBe(2)
    expect(JSON.stringify(result.messages)).toContain('reply')
    expect(JSON.stringify(result.messages)).not.toContain('sibling')
    expect(requests.filter(({ url }) => url.pathname.includes('/thread'))).toHaveLength(1)
  })

  test.each([
    ['direct message', direct, fetchDMs],
    ['group DM', group, fetchGroupDMs],
  ] as const)('resumes an explicit %s channel', async (_label, conversation, run) => {
    const cursor = encodeChannelHistoryCursor({
      v: 1,
      scope: 'channel',
      channelId: conversation.id,
      boundary: { createAt: 200, id: 'anchor' },
      since: null,
    })
    const conversationPosts = [
      post('peer', 200, { channel_id: conversation.id }),
      post('older', 100, { channel_id: conversation.id }),
    ]
    const extraRoutes =
      conversation.type === 'D'
        ? [
            {
              method: 'GET',
              path: '/api/v4/users/other',
              handle: () => ({ id: 'other', username: 'other' }),
            },
          ]
        : []
    const { requests } = installRouteFetch([
      { method: 'GET', path: `/api/v4/channels/${conversation.id}`, handle: () => conversation },
      { method: 'GET', path: '/api/v4/users/me', handle: () => me },
      ...extraRoutes,
      {
        method: 'GET',
        path: `/api/v4/channels/${conversation.id}/posts`,
        handle: () => page(conversationPosts),
      },
    ])
    const output = captureJSON()

    await run({ ...options(), user: [], channel: conversation.id, cursor })

    expect(output()[0].retrieval.selection).toMatchObject({
      selectedCount: 2,
      queryTruncated: false,
      inputCursor: cursor,
      nextCursor: null,
    })
    expect(requests.some(({ url }) => url.pathname.endsWith(`/${conversation.id}/posts`))).toBe(
      true,
    )
  })

  test.each([
    ['direct message', direct, fetchDMs],
    ['group DM', group, fetchGroupDMs],
  ] as const)('rejects an explicit %s cursor mismatch before post history', async (_label, conversation, run) => {
    const cursor = encodeChannelHistoryCursor({
      v: 1,
      scope: 'channel',
      channelId: 'other-channel',
      boundary: { createAt: 200, id: 'anchor' },
      since: null,
    })
    const { requests } = installRouteFetch([
      { method: 'GET', path: `/api/v4/channels/${conversation.id}`, handle: () => conversation },
      { method: 'GET', path: '/api/v4/users/me', handle: () => me },
    ])

    const invocation = run({ ...options(), user: [], channel: conversation.id, cursor })
    await expect(invocation).rejects.toThrow('Cursor does not match the selected channel.')
    expect(requests.some(({ url }) => url.pathname.endsWith('/posts'))).toBe(false)
  })

  test('rejects mismatch before requesting posts', async () => {
    const cursor = encodeChannelHistoryCursor({
      v: 1,
      scope: 'channel',
      channelId: 'other',
      boundary: { createAt: 100, id: 'anchor' },
      since: null,
    })
    const { requests } = installRouteFetch(routes(() => page([])))
    await expect(fetchChannel(options({ cursor }))).rejects.toThrow(
      'Cursor does not match the selected channel.',
    )
    expect(requests.some(({ url }) => url.pathname.endsWith('/posts'))).toBe(false)
  })

  test('rejects an empty cursor before requesting posts', async () => {
    const { requests } = installRouteFetch(routes(() => page([])))
    await expect(fetchChannel(options({ cursor: '' }))).rejects.toThrow('Invalid cursor.')
    expect(requests).toHaveLength(0)
  })

  test('rejects malformed, unsupported, and explicit-since combinations before fetching', async () => {
    const fake = vi.fn()
    vi.stubGlobal('fetch', fake)
    const validCursor = encodeChannelHistoryCursor({
      v: 1,
      scope: 'channel',
      channelId: 'general',
      boundary: { createAt: 100, id: 'anchor' },
      since: null,
    })
    await expect(
      fetchChannel(options({ cursor: validCursor, sinceExplicit: true })),
    ).rejects.toThrow('A cursor cannot be combined with --since.')
    await expect(
      fetchDMs({ ...options(), user: [], channel: undefined, cursor: validCursor }),
    ).rejects.toThrow('A cursor requires --channel')
    await expect(
      fetchDMs({ ...options(), user: ['alice'], channel: 'dm', cursor: validCursor }),
    ).rejects.toThrow('A cursor cannot be combined with --user.')
    await expect(
      fetchGroupDMs({ ...options(), channel: undefined, cursor: validCursor }),
    ).rejects.toThrow('A cursor requires --channel')
    expect(fake).not.toHaveBeenCalled()
  })
})
