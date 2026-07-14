import { spawnSync } from 'node:child_process'
import { afterEach, describe, expect, test, vi } from 'vitest'
import { clearUserCache } from '../src/api/users'
import { listChannels } from '../src/cli'
import type { Channel, ChannelTypeFilter } from '../src/types'
import { installRouteFetch } from './helpers/fake-fetch'

const channelIdentities: Pick<Channel, 'id' | 'type' | 'name' | 'display_name'>[] = [
  { id: 'public', type: 'O', name: 'public', display_name: 'Public' },
  { id: 'private', type: 'P', name: 'private', display_name: 'Private' },
  { id: 'dm', type: 'D', name: 'me__other', display_name: '' },
  { id: 'group', type: 'G', name: 'group', display_name: 'Group' },
]
const channels: Channel[] = channelIdentities.map(
  (channel) =>
    ({
      ...channel,
      team_id: channel.type === 'O' || channel.type === 'P' ? 'team' : '',
      header: '',
      purpose: '',
      last_post_at: 0,
      total_msg_count: 0,
      creator_id: 'me',
    }) satisfies Channel,
)

afterEach(() => {
  clearUserCache()
  vi.restoreAllMocks()
  vi.unstubAllGlobals()
})

test('rejects an invalid CLI --type value after credential protection and before network', () => {
  const result = spawnSync('bun', ['src/index.ts', 'channels', '--type', 'bogus'], {
    cwd: process.cwd(),
    encoding: 'utf8',
    env: { ...process.env, MM_URL: 'https://mattermost.test', MM_TOKEN: 'token' },
    stdio: 'pipe',
  })

  expect(result.status).not.toBe(0)
  expect(result.stderr).toContain('Invalid channel type "bogus"')
})

test.each([
  ['CLI', ['--token', 'cli-active-token'], { MM_TOKEN: undefined }, 'cli-active-token'],
  ['env', [], { MM_TOKEN: 'env-active-token' }, 'env-active-token'],
])('protects the %s token in invalid pre-network --type output with --no-redact', (_, args, env, token) => {
  const result = spawnSync(
    'bun',
    ['src/index.ts', '--no-redact', ...args, 'channels', '--type', token],
    {
      cwd: process.cwd(),
      encoding: 'utf8',
      env: { ...process.env, MM_URL: 'https://mattermost.test', ...env },
      stdio: 'pipe',
    },
  )
  expect(result.status).not.toBe(0)
  expect(result.stderr).toContain('[REDACTED:mattermost_credential]')
  expect(result.stderr).not.toContain(token)
})

test.each([
  ['users limit', ['--token', 'bad-limit', 'users', '--limit', 'bad-limit'], {}, 'bad-limit'],
  ['unread peek', ['unread', '--peek', 'bad-peek'], { MM_TOKEN: 'bad-peek' }, 'bad-peek'],
  ['DM since', ['--token', 'bad-since', 'dms', '--since', 'bad-since'], {}, 'bad-since'],
])('does not reflect invalid %s values that equal active credentials', (_, args, env, token) => {
  const result = spawnSync('bun', ['src/index.ts', '--no-redact', ...args], {
    cwd: process.cwd(),
    encoding: 'utf8',
    env: { ...process.env, MM_URL: 'https://mattermost.test', MM_TOKEN: undefined, ...env },
    stdio: 'pipe',
  })
  expect(result.status).not.toBe(0)
  expect(result.stderr).not.toContain(token)
  expect(result.stderr).toMatch(/must be a positive number|must use a duration/)
})

test('rejects an invalid direct call before fetching', async () => {
  const fetch = vi.fn()
  vi.stubGlobal('fetch', fetch)

  await expect(
    listChannels({
      url: 'https://mattermost.test',
      token: 'token',
      json: true,
      color: false,
      relative: false,
      redact: true,
      typeFilter: 'bogus' as ChannelTypeFilter,
    }),
  ).rejects.toThrow('Invalid channel type "bogus"')
  expect(fetch).not.toHaveBeenCalled()
})

describe.each([
  ['all', ['public', 'private', 'dm', 'group']],
  ['dm', ['dm']],
  ['public', ['public']],
  ['private', ['private']],
  ['group', ['group']],
] satisfies [ChannelTypeFilter, string[]][])('channels --type %s', (typeFilter, expectedIds) => {
  test('returns channels of the requested type', async () => {
    installRouteFetch([
      { method: 'GET', path: '/api/v4/users/me', handle: () => ({ id: 'me', username: 'me' }) },
      { method: 'GET', path: '/api/v4/users/me/channels', handle: () => channels },
      {
        method: 'GET',
        path: '/api/v4/users/me/teams',
        handle: () => [{ id: 'team', name: 'core', display_name: 'Core', type: 'O' }],
      },
      {
        method: 'POST',
        path: '/api/v4/users/ids',
        handle: () => [{ id: 'other', username: 'other' }],
      },
    ])
    const log = vi.spyOn(console, 'log').mockImplementation(() => undefined)

    await listChannels({
      url: 'https://mattermost.test',
      token: 'token',
      json: true,
      color: false,
      relative: false,
      redact: true,
      typeFilter,
    })

    const output = JSON.parse(String(log.mock.calls[0]?.[0])) as { id: string }[]
    expect(output.map(({ id }) => id).sort()).toEqual(expectedIds.sort())
  })
})

test('dedupes channel IDs and emits narrow team identity only for O/P channels', async () => {
  const { requests } = installRouteFetch([
    { method: 'GET', path: '/api/v4/users/me', handle: () => ({ id: 'me', username: 'me' }) },
    {
      method: 'GET',
      path: '/api/v4/users/me/channels',
      handle: () => [channels[0], channels[0], channels[3]],
    },
    {
      method: 'GET',
      path: '/api/v4/users/me/teams',
      handle: () => [
        { id: 'team', name: 'co\nre', display_name: 'AKIA1234567890ABCDEF', type: 'O' },
      ],
    },
  ])
  const log = vi.spyOn(console, 'log').mockImplementation(() => undefined)

  await listChannels({
    url: 'https://mattermost.test',
    token: 'token',
    json: true,
    color: false,
    relative: false,
    redact: true,
    typeFilter: 'all',
  })

  const output = JSON.parse(String(log.mock.calls[0]?.[0]))
  expect(output).toHaveLength(2)
  expect(output.find(({ id }: { id: string }) => id === 'public')?.team).toEqual({
    id: 'team',
    name: 'co\\nre',
    displayName: 'AK...EF',
  })
  expect(output.find(({ id }: { id: string }) => id === 'group')?.team).toBeNull()
  expect(requests.filter(({ url }) => url.pathname.endsWith('/teams'))).toHaveLength(1)
})

test('fails closed when a discovered direct channel excludes the current user', async () => {
  installRouteFetch([
    { method: 'GET', path: '/api/v4/users/me', handle: () => ({ id: 'me', username: 'me' }) },
    {
      method: 'GET',
      path: '/api/v4/users/me/channels',
      handle: () => [
        {
          ...channels[2],
          name: 'alice__bob',
        },
      ],
    },
  ])
  const log = vi.spyOn(console, 'log').mockImplementation(() => undefined)

  await expect(
    listChannels({
      url: 'https://mattermost.test',
      token: 'token',
      json: true,
      color: false,
      relative: false,
      redact: true,
      typeFilter: 'all',
    }),
  ).rejects.toThrow('Mattermost returned an invalid channel response.')
  expect(log).not.toHaveBeenCalled()
})

test('does not fetch teams for D/G-only discovery', async () => {
  const { requests } = installRouteFetch([
    { method: 'GET', path: '/api/v4/users/me', handle: () => ({ id: 'me', username: 'me' }) },
    {
      method: 'GET',
      path: '/api/v4/users/me/channels',
      handle: () => [channels[3]],
    },
  ])
  vi.spyOn(console, 'log').mockImplementation(() => undefined)
  await listChannels({
    url: 'https://mattermost.test',
    token: 'token',
    json: true,
    color: false,
    relative: false,
    redact: true,
    typeFilter: 'group',
  })
  expect(requests.some(({ url }) => url.pathname.endsWith('/teams'))).toBe(false)
})

test.each(['', 'other'])('fails closed for O/P with invalid team membership %j', async (teamId) => {
  installRouteFetch([
    { method: 'GET', path: '/api/v4/users/me', handle: () => ({ id: 'me', username: 'me' }) },
    {
      method: 'GET',
      path: '/api/v4/users/me/channels',
      handle: () => [{ ...channels[0], team_id: teamId }],
    },
    {
      method: 'GET',
      path: '/api/v4/users/me/teams',
      handle: () => [{ id: 'team', name: 'core', display_name: 'Core', type: 'O' }],
    },
  ])
  await expect(
    listChannels({
      url: 'https://mattermost.test',
      token: 'token',
      json: true,
      color: false,
      relative: false,
      redact: false,
      typeFilter: 'all',
    }),
  ).rejects.toThrow('Invalid channels response.')
})

test('human channel listing labels group DMs without a hash', async () => {
  installRouteFetch([
    { method: 'GET', path: '/api/v4/users/me', handle: () => ({ id: 'me', username: 'me' }) },
    {
      method: 'GET',
      path: '/api/v4/users/me/channels',
      handle: () => channels.filter((channel) => channel.type === 'G'),
    },
  ])
  const log = vi.spyOn(console, 'log').mockImplementation(() => undefined)

  await listChannels({
    url: 'https://mattermost.test',
    token: 'token',
    json: false,
    color: false,
    relative: false,
    redact: true,
    typeFilter: 'group',
  })

  const output = log.mock.calls.map(([value]) => String(value)).join('\n')
  expect(output).toContain('Group')
  expect(output).toContain('[group]')
  expect(output).not.toContain('#Group')
})

test('human O/P labels include team slug and channel ID', async () => {
  installRouteFetch([
    { method: 'GET', path: '/api/v4/users/me', handle: () => ({ id: 'me', username: 'me' }) },
    { method: 'GET', path: '/api/v4/users/me/channels', handle: () => [channels[0]] },
    {
      method: 'GET',
      path: '/api/v4/users/me/teams',
      handle: () => [{ id: 'team', name: 'core', display_name: 'Core', type: 'O' }],
    },
  ])
  const log = vi.spyOn(console, 'log').mockImplementation(() => undefined)

  await listChannels({
    url: 'https://mattermost.test',
    token: 'token',
    json: false,
    color: false,
    relative: false,
    redact: true,
    typeFilter: 'public',
  })

  expect(log.mock.calls.map(([value]) => String(value)).join('\n')).toContain('core/#public')
  expect(log.mock.calls.map(([value]) => String(value)).join('\n')).toContain('[public]')
})

test('normalizes malformed channel count and timestamp scalars in JSON output', async () => {
  installRouteFetch([
    { method: 'GET', path: '/api/v4/users/me', handle: () => ({ id: 'me', username: 'me' }) },
    {
      method: 'GET',
      path: '/api/v4/users/me/channels',
      handle: () => [
        {
          ...channels[0],
          total_msg_count: '9000',
          last_post_at: 'tomorrow',
        },
      ],
    },
    {
      method: 'GET',
      path: '/api/v4/users/me/teams',
      handle: () => [{ id: 'team', name: 'core', display_name: 'Core', type: 'O' }],
    },
  ])
  const log = vi.spyOn(console, 'log').mockImplementation(() => undefined)
  await listChannels({
    url: 'https://mattermost.test',
    token: 'token',
    json: true,
    color: false,
    relative: false,
    redact: false,
    typeFilter: 'all',
  })
  expect(JSON.parse(String(log.mock.calls[0]?.[0]))[0]).toMatchObject({
    messageCount: 0,
    lastPost: null,
  })
})

test('fails generically for a hostile unknown remote channel type', async () => {
  const hostile = 'X\u202eactive-token'
  installRouteFetch([
    { method: 'GET', path: '/api/v4/users/me', handle: () => ({ id: 'me', username: 'me' }) },
    {
      method: 'GET',
      path: '/api/v4/users/me/channels',
      handle: () => [{ ...channels[0], type: hostile }],
    },
  ])
  await expect(
    listChannels({
      url: 'https://mattermost.test',
      token: 'active-token',
      json: true,
      color: false,
      relative: false,
      redact: false,
      typeFilter: 'all',
    }),
  ).rejects.toThrow('Mattermost returned an invalid channel response.')
})
