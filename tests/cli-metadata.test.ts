import { afterEach, describe, expect, test, vi } from 'vitest'
import { clearUserCache } from '../src/api/users'
import { fetchChannel, fetchMentions, searchMessages, showUnread } from '../src/cli'
import type { Channel, MessageOutput, Post, PostsResponse, SearchResponse } from '../src/types'
import { installRouteFetch } from './helpers/fake-fetch'

const serverUrl = 'https://mattermost.test'
const me = { id: 'me', username: 'me' }
const team = { id: 'team', name: 'team', display_name: 'Team', type: 'O' }

function channel(id: string): Channel {
  return {
    id,
    type: 'O',
    name: id,
    display_name: id.toUpperCase(),
    header: '',
    purpose: '',
    last_post_at: 0,
    total_msg_count: 0,
    creator_id: 'me',
  }
}

function post(id: string, channelId: string, createAt: number, message = id): Post {
  return {
    id,
    channel_id: channelId,
    user_id: 'me',
    create_at: createAt,
    update_at: createAt,
    delete_at: 0,
    edit_at: 0,
    message,
    type: '',
    props: {},
    hashtags: '',
    file_ids: [],
    root_id: '',
    reply_count: 0,
    pending_post_id: '',
  }
}

function page(posts: Post[]): PostsResponse {
  return {
    order: posts.map(({ id }) => id),
    posts: Object.fromEntries(posts.map((item) => [item.id, item])),
  } as PostsResponse
}

function searchPage(posts: Post[]): SearchResponse {
  return { ...page(posts), matches: {} }
}

function commonRoutes() {
  return [
    { method: 'GET', path: '/api/v4/users/me', handle: () => me },
    { method: 'GET', path: '/api/v4/users/me/teams', handle: () => [team] },
  ]
}

function outputLog() {
  const log = vi.spyOn(console, 'log').mockImplementation(() => undefined)
  return () => JSON.parse(String(log.mock.calls.at(-1)?.[0]))
}

function expectMetadata(
  output: MessageOutput,
  expected: {
    source: MessageOutput['retrieval']['selection']['source']
    count: number
    limit: number | null
    since: string | null
    truncated: boolean | null
  },
) {
  expect(output.retrieval.selection).toEqual({
    source: expected.source,
    selectedCount: expected.count,
    requestedLimit: expected.limit,
    since: expected.since,
    queryTruncated: expected.truncated,
  })
  expect(output.retrieval.visiblePostCount).toBe(expected.count)
  expect(output.retrieval.visibleThreads).toEqual({
    status: 'complete',
    hydratedRootCount: 0,
    failedRootIds: [],
  })
  expect(
    output.messages.every((message) => message.permalink.startsWith(`${serverUrl}/_redirect/pl/`)),
  ).toBe(true)
}

afterEach(() => {
  clearUserCache()
  vi.useRealTimers()
  vi.restoreAllMocks()
  vi.unstubAllGlobals()
})

describe('command-level JSON retrieval metadata', () => {
  test('channel reports the exact local since boundary', async () => {
    vi.useFakeTimers()
    vi.setSystemTime(new Date('2026-07-14T12:00:00.000Z'))
    const general = channel('general')
    installRouteFetch([
      ...commonRoutes(),
      { method: 'GET', path: '/api/v4/teams/team/channels/name/general', handle: () => general },
      {
        method: 'GET',
        path: '/api/v4/channels/general/posts',
        handle: () => page([post('channel-post', 'general', Date.now())]),
      },
    ])
    const readOutput = outputLog()

    await fetchChannel({
      url: serverUrl,
      token: 'token',
      json: true,
      color: false,
      relative: false,
      redact: true,
      threads: true,
      channel: 'general',
      limit: 2,
      since: '24h',
    })

    expectMetadata(readOutput()[0], {
      source: 'recent',
      count: 1,
      limit: 2,
      since: '2026-07-13T12:00:00.000Z',
      truncated: false,
    })
  })

  test('--no-threads returns only thread-shaped seeds without hydration calls', async () => {
    const general = channel('general')
    const seed = { ...post('root', 'general', Date.now()), reply_count: 2 }
    const { requests } = installRouteFetch([
      ...commonRoutes(),
      { method: 'GET', path: '/api/v4/teams/team/channels/name/general', handle: () => general },
      {
        method: 'GET',
        path: '/api/v4/channels/general/posts',
        handle: () => page([seed]),
      },
    ])
    const readOutput = outputLog()

    await fetchChannel({
      url: serverUrl,
      token: 'token',
      json: true,
      color: false,
      relative: false,
      redact: true,
      threads: false,
      channel: 'general',
      limit: 1,
      since: '24h',
    })

    const [output] = readOutput() as MessageOutput[]
    expect(output?.messages.map(({ id }) => id)).toEqual(['root'])
    expect(output?.retrieval.visibleThreads).toEqual({
      status: 'not_requested',
      hydratedRootCount: 0,
      failedRootIds: [],
    })
    expect(output?.retrieval.visiblePostCount).toBe(1)
    expect(requests.some(({ url }) => url.pathname.includes('/thread'))).toBe(false)
  })

  test.each([
    ['search', 'needle'],
    ['mentions', '@me'],
  ] as const)('%s keeps per-channel counts with global limit/truncation', async (source, text) => {
    const alpha = channel('alpha')
    const beta = channel('beta')
    const alphaPost = {
      ...post('alpha-new', 'alpha', 3, text),
      user_id: 'alpha-user',
      metadata: {
        reactions: [{ user_id: 'reactor', post_id: 'alpha-new', emoji_name: 'eyes', create_at: 1 }],
      },
    }
    const betaPost = { ...post('beta-new', 'beta', 2, text), user_id: 'beta-user' }
    const { requests } = installRouteFetch([
      ...commonRoutes(),
      {
        method: 'POST',
        path: '/api/v4/teams/team/posts/search',
        handle: () => searchPage([alphaPost, betaPost, post('alpha-extra', 'alpha', 1, text)]),
      },
      {
        method: 'POST',
        path: '/api/v4/users/ids',
        handle: () => [
          { id: 'alpha-user', username: 'alpha-user' },
          { id: 'beta-user', username: 'beta-user' },
          { id: 'reactor', username: 'reactor' },
        ],
      },
      { method: 'GET', path: '/api/v4/channels/alpha', handle: () => alpha },
      { method: 'GET', path: '/api/v4/channels/beta', handle: () => beta },
    ])
    const readOutput = outputLog()

    if (source === 'search') {
      await searchMessages({
        url: serverUrl,
        token: 'token',
        json: true,
        color: false,
        relative: false,
        redact: true,
        threads: true,
        query: 'needle',
        limit: 2,
      })
    } else {
      await fetchMentions({
        url: serverUrl,
        token: 'token',
        json: true,
        color: false,
        relative: false,
        redact: true,
        threads: true,
        limit: 2,
        mentionNames: [],
      })
    }

    const outputs = readOutput() as MessageOutput[]
    expect(outputs).toHaveLength(2)
    for (const output of outputs) {
      expectMetadata(output, {
        source,
        count: 1,
        limit: 2,
        since: null,
        truncated: true,
      })
    }
    expect(requests.filter(({ url }) => url.pathname.endsWith('/users/ids'))).toHaveLength(1)
    expect(requests.find(({ url }) => url.pathname.endsWith('/users/ids'))?.body).toEqual([
      'alpha-user',
      'reactor',
      'beta-user',
    ])
    expect(requests.some(({ url }) => /\/(files|reactions)(\/|$)/.test(url.pathname))).toBe(false)
  })

  test.each([
    ['search', 'needle'],
    ['mentions', '@me'],
  ] as const)('%s keeps matched seed counts separate from hydrated context', async (source, text) => {
    const general = channel('general')
    const root = { ...post('root', 'general', 1, 'older context'), reply_count: 2 }
    const seed = { ...post('seed', 'general', 3, text), root_id: 'root' }
    const sibling = {
      ...post('sibling', 'general', 2, 'context sk-abcdefghijklmnopqrstuvwxyz123456'),
      root_id: 'root',
      user_id: 'other',
    }
    installRouteFetch([
      ...commonRoutes(),
      {
        method: 'POST',
        path: '/api/v4/teams/team/posts/search',
        handle: () => ({ ...searchPage([seed]), has_next: false }),
      },
      {
        method: 'GET',
        path: '/api/v4/posts/root/thread',
        handle: () => ({ ...page([root, sibling, seed]), has_next: false }),
      },
      { method: 'GET', path: '/api/v4/channels/general', handle: () => general },
      {
        method: 'POST',
        path: '/api/v4/users/ids',
        handle: () => [{ id: 'other', username: 'other' }],
      },
    ])
    const readOutput = outputLog()

    if (source === 'search') {
      await searchMessages({
        url: serverUrl,
        token: 'token',
        json: true,
        color: false,
        relative: false,
        redact: true,
        threads: true,
        query: text,
        limit: 1,
      })
    } else {
      await fetchMentions({
        url: serverUrl,
        token: 'token',
        json: true,
        color: false,
        relative: false,
        redact: true,
        threads: true,
        limit: 1,
        mentionNames: [],
      })
    }

    const [output] = readOutput() as MessageOutput[]
    expect(output?.retrieval.selection.selectedCount).toBe(1)
    expect(output?.retrieval.visiblePostCount).toBe(3)
    expect(output?.retrieval.visibleThreads.status).toBe('complete')
    const visible =
      output?.messages.flatMap((message) => [message, ...(message.replies ?? [])]) ?? []
    expect(visible.map(({ id }) => id)).toEqual(['root', 'sibling', 'seed'])
    const hydratedSibling = visible.find(({ id }) => id === 'sibling')
    expect(hydratedSibling?.user).toBe('other')
    expect(hydratedSibling?.text).not.toContain('sk-abcdefghijklmnopqrstuvwxyz123456')
    expect(hydratedSibling?.permalink).toBe(`${serverUrl}/_redirect/pl/sibling`)
  })

  test('unread peek reports each channel boundary and selected count', async () => {
    const alpha = { ...channel('alpha'), last_post_at: 300, total_msg_count: 2 }
    const beta = { ...channel('beta'), last_post_at: 200, total_msg_count: 2 }
    installRouteFetch([
      ...commonRoutes(),
      { method: 'GET', path: '/api/v4/users/me/channels', handle: () => [alpha, beta] },
      {
        method: 'GET',
        path: '/api/v4/users/me/teams/team/channels/members',
        handle: () => [
          {
            channel_id: 'alpha',
            user_id: 'me',
            msg_count: 1,
            mention_count: 0,
            last_viewed_at: 100,
          },
          { channel_id: 'beta', user_id: 'me', msg_count: 1, mention_count: 0, last_viewed_at: 50 },
        ],
      },
      {
        method: 'GET',
        path: '/api/v4/channels/alpha/posts',
        handle: () => page([post('alpha-post', 'alpha', 300)]),
      },
      {
        method: 'GET',
        path: '/api/v4/channels/beta/posts',
        handle: () => page([post('beta-post', 'beta', 200)]),
      },
    ])
    const readOutput = outputLog()

    await showUnread({
      url: serverUrl,
      token: 'token',
      json: true,
      color: false,
      relative: false,
      redact: true,
      threads: true,
      peek: 2,
    })

    const outputs = readOutput().peek as MessageOutput[]
    const alphaOutput = outputs.find(({ channel }) => channel.id === 'alpha')
    const betaOutput = outputs.find(({ channel }) => channel.id === 'beta')
    if (!alphaOutput || !betaOutput) throw new Error('missing unread output fixture')
    expectMetadata(alphaOutput, {
      source: 'unread',
      count: 1,
      limit: 2,
      since: new Date(100).toISOString(),
      truncated: false,
    })
    expectMetadata(betaOutput, {
      source: 'unread',
      count: 1,
      limit: 2,
      since: new Date(50).toISOString(),
      truncated: false,
    })
  })
})
