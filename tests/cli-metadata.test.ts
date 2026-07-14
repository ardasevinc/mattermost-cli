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
    status: 'not_requested',
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

  test.each([
    ['search', 'needle'],
    ['mentions', '@me'],
  ] as const)('%s keeps per-channel counts with global limit/truncation', async (source, text) => {
    const alpha = channel('alpha')
    const beta = channel('beta')
    installRouteFetch([
      ...commonRoutes(),
      {
        method: 'POST',
        path: '/api/v4/teams/team/posts/search',
        handle: () =>
          searchPage([
            post('alpha-new', 'alpha', 3, text),
            post('beta-new', 'beta', 2, text),
            post('alpha-extra', 'alpha', 1, text),
          ]),
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
