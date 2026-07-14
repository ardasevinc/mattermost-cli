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

test('rejects an invalid CLI --type value before running the command', () => {
  const result = spawnSync('bun', ['src/index.ts', 'channels', '--type', 'bogus'], {
    cwd: process.cwd(),
    encoding: 'utf8',
    env: { ...process.env, MM_URL: 'https://mattermost.test', MM_TOKEN: 'token' },
    stdio: 'pipe',
  })

  expect(result.status).not.toBe(0)
  expect(result.stderr).toContain('Allowed choices are all, dm, public, private, group')
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
