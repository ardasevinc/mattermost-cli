import { afterEach, describe, expect, test, vi } from 'vitest'
import { clearUserCache } from '../src/api/users'
import {
  fetchChannel,
  fetchDMs,
  fetchGroupDMs,
  fetchMentions,
  searchMessages,
  showUnread,
} from '../src/cli'
import type {
  Channel,
  ChannelOptions,
  CLIOptions,
  DMsOptions,
  GroupDMsOptions,
  MentionOptions,
  SearchOptions,
  UnreadOptions,
} from '../src/types'
import { installRouteFetch } from './helpers/fake-fetch'

const serverUrl = 'https://mattermost.test'
const me = { id: 'me', username: 'me' }
const team = { id: 'team', name: 'team', display_name: 'Team', type: 'O' }
const general = {
  id: 'general',
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

const baseOptions: CLIOptions = {
  url: serverUrl,
  token: 'token',
  json: true,
  color: false,
  relative: false,
  redact: true,
  threads: true,
}

function commonRoutes() {
  return [
    { method: 'GET', path: '/api/v4/users/me', handle: () => me },
    { method: 'GET', path: '/api/v4/users/me/teams', handle: () => [team] },
  ]
}

function emptyPage() {
  return { order: [], posts: {}, has_next: false }
}

function uncertainEmptyPage() {
  return { ...emptyPage(), first_inaccessible_post_time: 1 }
}

function captureOutput() {
  const log = vi.spyOn(console, 'log').mockImplementation(() => undefined)
  const exit = vi.spyOn(process, 'exit').mockImplementation(() => undefined as never)
  return {
    output: () => log.mock.calls.map(([value]) => String(value)).join('\n'),
    exit,
  }
}

type EmptyCommand = {
  name: string
  install: () => void
  run: (json: boolean) => Promise<void>
}

const emptyCommands: EmptyCommand[] = [
  {
    name: 'channel',
    install: () => {
      installRouteFetch([
        ...commonRoutes(),
        { method: 'GET', path: '/api/v4/teams/team/channels/name/general', handle: () => general },
        { method: 'GET', path: '/api/v4/channels/general/posts', handle: emptyPage },
      ])
    },
    run: (json) =>
      fetchChannel({
        ...baseOptions,
        json,
        channel: 'general',
        limit: 50,
        since: '',
      } satisfies ChannelOptions),
  },
  {
    name: 'search',
    install: () => {
      installRouteFetch([
        ...commonRoutes(),
        { method: 'POST', path: '/api/v4/teams/team/posts/search', handle: emptyPage },
      ])
    },
    run: (json) =>
      searchMessages({ ...baseOptions, json, query: 'needle', limit: 50 } satisfies SearchOptions),
  },
  {
    name: 'mentions',
    install: () => {
      installRouteFetch([
        ...commonRoutes(),
        { method: 'POST', path: '/api/v4/teams/team/posts/search', handle: emptyPage },
      ])
    },
    run: (json) =>
      fetchMentions({
        ...baseOptions,
        json,
        limit: 50,
        mentionNames: [],
      } satisfies MentionOptions),
  },
]

afterEach(() => {
  clearUserCache()
  vi.restoreAllMocks()
  vi.unstubAllGlobals()
})

describe.each(emptyCommands)('$name empty reads', ({ install, run }) => {
  test('emits exact JSON and does not exit', async () => {
    install()
    const capture = captureOutput()

    await run(true)

    expect(capture.output()).toBe('[]')
    expect(capture.exit).not.toHaveBeenCalled()
  })

  test('emits neutral human output and does not exit', async () => {
    install()
    const capture = captureOutput()

    await run(false)

    expect(capture.output()).toBe('No messages found.')
    expect(capture.exit).not.toHaveBeenCalled()
  })
})

describe('unknown empty reads', () => {
  test.each([
    ['channel', () => fetchChannel({ ...baseOptions, channel: 'general', limit: 50, since: '' })],
    ['search', () => searchMessages({ ...baseOptions, query: 'needle', limit: 50 })],
    ['mentions', () => fetchMentions({ ...baseOptions, limit: 50, mentionNames: [] })],
  ] as const)('%s rejects instead of claiming an empty success', async (name, run) => {
    installRouteFetch([
      ...commonRoutes(),
      ...(name === 'channel'
        ? [
            {
              method: 'GET',
              path: '/api/v4/teams/team/channels/name/general',
              handle: () => general,
            },
            { method: 'GET', path: '/api/v4/channels/general/posts', handle: uncertainEmptyPage },
          ]
        : [
            {
              method: 'POST',
              path: '/api/v4/teams/team/posts/search',
              handle: uncertainEmptyPage,
            },
          ]),
    ])

    await expect(run()).rejects.toThrow(
      'Message retrieval was incomplete, so an empty result cannot be confirmed.',
    )
  })

  test('merged DM history rejects when its only empty channel has unknown completeness', async () => {
    installRouteFetch([
      { method: 'GET', path: '/api/v4/users/me', handle: () => me },
      { method: 'GET', path: '/api/v4/users/me/channels', handle: () => [direct] },
      { method: 'GET', path: '/api/v4/channels/dm/posts', handle: uncertainEmptyPage },
    ])

    await expect(fetchDMs({ ...baseOptions, user: [], limit: 50, since: '' })).rejects.toThrow(
      'Message retrieval was incomplete',
    )
  })

  test.each([
    ['direct', { ...direct }, fetchDMs],
    ['group', { ...direct, id: 'group', type: 'G' as const }, fetchGroupDMs],
  ] as const)('explicit %s history rejects an unknown empty result', async (_name, channel, run) => {
    installRouteFetch([
      { method: 'GET', path: `/api/v4/channels/${channel.id}`, handle: () => channel },
      { method: 'GET', path: '/api/v4/users/me', handle: () => me },
      {
        method: 'GET',
        path: `/api/v4/channels/${channel.id}/posts`,
        handle: uncertainEmptyPage,
      },
    ])

    await expect(
      run({
        ...baseOptions,
        channel: channel.id,
        limit: 50,
        since: '',
        ...(run === fetchDMs ? { user: [] } : {}),
      } as DMsOptions & GroupDMsOptions),
    ).rejects.toThrow('Message retrieval was incomplete')
  })

  test('unread peek rejects when unread posts cannot be confirmed empty', async () => {
    installRouteFetch([
      ...commonRoutes(),
      {
        method: 'GET',
        path: '/api/v4/users/me/channels',
        handle: () => [{ ...general, total_msg_count: 1 }],
      },
      {
        method: 'GET',
        path: '/api/v4/users/me/teams/team/channels/members',
        handle: () => [
          { channel_id: 'general', msg_count: 0, mention_count: 0, last_viewed_at: 1 },
        ],
      },
      { method: 'GET', path: '/api/v4/channels/general/posts', handle: uncertainEmptyPage },
    ])

    await expect(showUnread({ ...baseOptions, peek: 5 } satisfies UnreadOptions)).rejects.toThrow(
      'Message retrieval was incomplete',
    )
  })
})

describe('DM empty reads', () => {
  test.each([
    ['no matched channels', []],
    ['matched channel with no posts', [direct]],
  ] as const)('%s emits exact JSON and does not exit', async (_case, channels) => {
    installRouteFetch([
      { method: 'GET', path: '/api/v4/users/me', handle: () => me },
      { method: 'GET', path: '/api/v4/users/me/channels', handle: () => channels },
      ...(channels.length > 0
        ? [{ method: 'GET', path: '/api/v4/channels/dm/posts', handle: emptyPage }]
        : []),
    ])
    const capture = captureOutput()

    await fetchDMs({
      ...baseOptions,
      user: [],
      limit: 50,
      since: '',
    } satisfies DMsOptions)

    expect(capture.output()).toBe('[]')
    expect(capture.exit).not.toHaveBeenCalled()
  })

  test('emits neutral human output and does not exit', async () => {
    installRouteFetch([
      { method: 'GET', path: '/api/v4/users/me', handle: () => me },
      { method: 'GET', path: '/api/v4/users/me/channels', handle: () => [] },
    ])
    const capture = captureOutput()

    await fetchDMs({
      ...baseOptions,
      json: false,
      user: [],
      limit: 50,
      since: '',
    } satisfies DMsOptions)

    expect(capture.output()).toBe('No messages found.')
    expect(capture.exit).not.toHaveBeenCalled()
  })
})

test('empty unread JSON keeps its command-specific structured shape', async () => {
  installRouteFetch([
    ...commonRoutes(),
    { method: 'GET', path: '/api/v4/users/me/channels', handle: () => [] },
    { method: 'GET', path: '/api/v4/users/me/teams/team/channels/members', handle: () => [] },
  ])
  const capture = captureOutput()

  await showUnread({ ...baseOptions } satisfies CLIOptions)

  expect(JSON.parse(capture.output())).toEqual({ unread: [] })
  expect(capture.exit).not.toHaveBeenCalled()
})
