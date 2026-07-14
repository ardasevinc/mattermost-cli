import { afterEach, describe, expect, test, vi } from 'vitest'
import { clearUserCache } from '../src/api/users'
import { fetchDMs, fetchGroupDMs } from '../src/cli'
import type { Channel, DMsOptions, GroupDMsOptions, Post, User } from '../src/types'
import { installRouteFetch } from './helpers/fake-fetch'

const me = { id: 'me', username: 'me' } as User
const alice = { id: 'alice', username: 'alice' } as User

function group(id: string, displayName = ''): Channel {
  return { id, type: 'G', name: `${id}-internal`, display_name: displayName } as Channel
}

function post(id: string, channelId: string, createAt: number): Post {
  return {
    id,
    channel_id: channelId,
    user_id: 'alice',
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
  }
}

function page(posts: Post[]) {
  return {
    order: posts.map(({ id }) => id),
    posts: Object.fromEntries(posts.map((item) => [item.id, item])),
    has_next: false,
  }
}

function options(overrides: Partial<GroupDMsOptions> = {}): GroupDMsOptions {
  return {
    url: 'https://mattermost.test',
    token: 'token',
    json: true,
    color: false,
    relative: false,
    redact: true,
    threads: false,
    limit: 50,
    since: '7d',
    ...overrides,
  }
}

function userBatchRoute() {
  return { method: 'POST', path: '/api/v4/users/ids', handle: () => [alice] }
}

afterEach(() => {
  clearUserCache()
  vi.restoreAllMocks()
  vi.unstubAllGlobals()
})

describe('group DM retrieval', () => {
  test('discovers only group channels and labels display names without a channel prefix', async () => {
    const channel = group('group-one', 'Launch <script> AKIA1234567890ABCDEF')
    const message = post('message', channel.id, Date.now())
    const { requests } = installRouteFetch([
      { method: 'GET', path: '/api/v4/users/me', handle: () => me },
      { method: 'GET', path: '/api/v4/users/me/channels', handle: () => [channel] },
      {
        method: 'GET',
        path: `/api/v4/channels/${channel.id}/posts`,
        handle: () => page([message]),
      },
      userBatchRoute(),
    ])
    const log = vi.spyOn(console, 'log').mockImplementation(() => undefined)

    await fetchGroupDMs(options())

    const [output] = JSON.parse(String(log.mock.calls.at(-1)?.[0]))
    expect(output.channel).toMatchObject({ type: 'group', name: 'Launch <script> AK...EF' })
    expect(output.channel.displayName).toBeUndefined()
    expect(
      requests.some(({ method, url }) => method === 'POST' && url.pathname.includes('/channels')),
    ).toBe(false)
  })

  test('fetches an explicit group channel without channel discovery', async () => {
    const channel = group('chosen', '')
    const message = post('message', channel.id, Date.now())
    const { requests } = installRouteFetch([
      { method: 'GET', path: `/api/v4/channels/${channel.id}`, handle: () => channel },
      { method: 'GET', path: '/api/v4/users/me', handle: () => me },
      {
        method: 'GET',
        path: `/api/v4/channels/${channel.id}/posts`,
        handle: () => page([message]),
      },
      userBatchRoute(),
    ])
    vi.spyOn(console, 'log').mockImplementation(() => undefined)

    await fetchGroupDMs(options({ channel: channel.id }))

    expect(requests.some(({ url }) => url.pathname === '/api/v4/users/me/channels')).toBe(false)
  })

  test('enforces one global limit and reports truncation across group channels', async () => {
    const first = group('first', 'First')
    const second = group('second', 'Second')
    const now = Date.now()
    const { requests } = installRouteFetch([
      { method: 'GET', path: '/api/v4/users/me', handle: () => me },
      { method: 'GET', path: '/api/v4/users/me/channels', handle: () => [first, second] },
      {
        method: 'GET',
        path: `/api/v4/channels/${first.id}/posts`,
        handle: () => page([post('older', first.id, now - 1)]),
      },
      {
        method: 'GET',
        path: `/api/v4/channels/${second.id}/posts`,
        handle: () => page([post('newer', second.id, now)]),
      },
      userBatchRoute(),
    ])
    const log = vi.spyOn(console, 'log').mockImplementation(() => undefined)

    await fetchGroupDMs(options({ limit: 1 }))

    const output = JSON.parse(String(log.mock.calls.at(-1)?.[0]))
    expect(
      output.flatMap(({ messages }: { messages: Array<{ id: string }> }) =>
        messages.map(({ id }) => id),
      ),
    ).toEqual(['newer'])
    expect(output[0].retrieval.selection).toMatchObject({ selectedCount: 1, queryTruncated: true })
    expect(requests.filter(({ url }) => url.pathname.endsWith('/posts'))).toHaveLength(2)
  })

  test.each([
    ['JSON', true, '[]'],
    ['human', false, 'No messages found.'],
  ])('returns a successful empty %s result', async (_label, json, expected) => {
    const { requests } = installRouteFetch([
      { method: 'GET', path: '/api/v4/users/me', handle: () => me },
      { method: 'GET', path: '/api/v4/users/me/channels', handle: () => [] },
    ])
    const log = vi.spyOn(console, 'log').mockImplementation(() => undefined)

    await fetchGroupDMs(options({ json }))

    expect(log.mock.calls.map(([value]) => String(value)).join('\n')).toBe(expected)
    expect(requests.filter(({ url }) => url.pathname === '/api/v4/users/me')).toHaveLength(1)
  })

  test('rejects an explicit non-group channel before reading posts', async () => {
    const channel = { ...group('public'), type: 'O' as const }
    const { requests } = installRouteFetch([
      { method: 'GET', path: `/api/v4/channels/${channel.id}`, handle: () => channel },
    ])

    await expect(fetchGroupDMs(options({ channel: channel.id }))).rejects.toThrow(
      'Channel "public" is not a group DM.',
    )
    expect(requests.some(({ url }) => url.pathname.endsWith('/posts'))).toBe(false)
  })

  test.each([
    [true, 'Channel "AK...EF" is not a group DM.'],
    [false, 'Channel "AKIA1234567890ABCDEF" is not a group DM.'],
  ])('presents wrong-type IDs according to redact=%s', async (redact, message) => {
    const channel = { ...group('AKIA1234567890ABCDEF'), type: 'P' as const }
    installRouteFetch([
      { method: 'GET', path: `/api/v4/channels/${channel.id}`, handle: () => channel },
    ])

    await expect(fetchGroupDMs(options({ channel: channel.id, redact }))).rejects.toThrow(message)
  })

  test.each([
    'G',
    'O',
    'P',
  ] as const)('rejects an explicit %s channel through dms before reading posts', async (type) => {
    const channel = { ...group(`wrong-${type}`), type }
    const { requests } = installRouteFetch([
      { method: 'GET', path: `/api/v4/channels/${channel.id}`, handle: () => channel },
    ])

    await expect(
      fetchDMs({
        ...options(),
        user: [],
        channel: channel.id,
      } satisfies DMsOptions),
    ).rejects.toThrow(`Channel "wrong-${type}" is not a direct-message channel.`)
    expect(requests.some(({ url }) => url.pathname.endsWith('/posts'))).toBe(false)
    expect(requests.some(({ url }) => url.pathname === '/api/v4/users/me')).toBe(false)
  })

  test.each([
    [true, 'Channel "AK...EF" is not a direct-message channel.'],
    [false, 'Channel "AKIA1234567890ABCDEF" is not a direct-message channel.'],
  ])('presents wrong DM channel IDs according to redact=%s', async (redact, message) => {
    const channel = { ...group('AKIA1234567890ABCDEF'), type: 'G' as const }
    installRouteFetch([
      { method: 'GET', path: `/api/v4/channels/${channel.id}`, handle: () => channel },
    ])

    await expect(
      fetchDMs({
        ...options({ redact }),
        user: [],
        channel: channel.id,
      } satisfies DMsOptions),
    ).rejects.toThrow(message)
  })

  test('discovers empty DMs with one identity request', async () => {
    const { requests } = installRouteFetch([
      { method: 'GET', path: '/api/v4/users/me', handle: () => me },
      { method: 'GET', path: '/api/v4/users/me/channels', handle: () => [] },
    ])
    vi.spyOn(console, 'log').mockImplementation(() => undefined)

    await fetchDMs({ ...options(), user: [] } satisfies DMsOptions)

    expect(requests.filter(({ url }) => url.pathname === '/api/v4/users/me')).toHaveLength(1)
  })

  test('uses a Markdown group label without a hash', async () => {
    const channel = group('group', 'Design Crew')
    const message = post('message', channel.id, Date.now())
    const routes = () => [
      { method: 'GET', path: `/api/v4/channels/${channel.id}`, handle: () => channel },
      { method: 'GET', path: '/api/v4/users/me', handle: () => me },
      {
        method: 'GET',
        path: `/api/v4/channels/${channel.id}/posts`,
        handle: () => page([message]),
      },
      userBatchRoute(),
    ]
    installRouteFetch(routes())
    const log = vi.spyOn(console, 'log').mockImplementation(() => undefined)
    await fetchGroupDMs(options({ channel: channel.id, json: false }))
    expect(String(log.mock.calls.at(-1)?.[0])).toContain('## Group DM: Design Crew')
    expect(String(log.mock.calls.at(-1)?.[0])).not.toContain('#Design Crew')
  })
})
