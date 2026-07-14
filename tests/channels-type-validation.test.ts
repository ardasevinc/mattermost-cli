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
  expect(output).not.toContain('#Group')
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
  ).rejects.toThrow('Mattermost returned an unknown channel type.')
})
