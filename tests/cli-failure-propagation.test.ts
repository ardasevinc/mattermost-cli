import { afterEach, describe, expect, test, vi } from 'vitest'
import { clearUserCache } from '../src/api/users'
import { fetchDMs, showUnread } from '../src/cli'
import type { Channel, DMsOptions, UnreadOptions } from '../src/types'

const base = {
  url: 'https://mattermost.test',
  token: 'token',
  json: true,
  color: false,
  relative: false,
  redact: true,
  threads: false,
}
const me = { id: 'me', username: 'me' }
const dm = {
  id: 'dm',
  team_id: '',
  type: 'D',
  name: 'carol__me',
  display_name: '',
  total_msg_count: 0,
} as Channel

function response(body: unknown, status = 200): Response {
  return Response.json(body, { status, statusText: status === 404 ? 'Not Found' : 'Server Error' })
}

afterEach(() => {
  clearUserCache()
  vi.restoreAllMocks()
  vi.unstubAllGlobals()
})

describe('dms --user failures', () => {
  test('warns separately for a missing user and an existing user without a DM in a mixed request', async () => {
    vi.stubGlobal(
      'fetch',
      vi.fn(async (input: string | URL | Request) => {
        const path = new URL(typeof input === 'string' || input instanceof URL ? input : input.url)
          .pathname
        if (path.endsWith('/users/me')) return response(me)
        if (path.endsWith('/users/me/channels')) return response([dm])
        if (path.endsWith('/users/username/alice')) {
          return response({ message: 'REMOTE_SECRET_BODY' }, 404)
        }
        if (path.endsWith('/users/username/bob')) return response({ id: 'bob', username: 'bob' })
        if (path.endsWith('/users/username/carol')) {
          return response({ id: 'carol', username: 'carol' })
        }
        if (path.endsWith('/channels/dm/posts')) {
          return response({ order: [], posts: {}, has_next: false })
        }
        throw new Error(`unexpected route ${path}`)
      }),
    )
    const warn = vi.spyOn(console, 'error').mockImplementation(() => undefined)
    vi.spyOn(console, 'log').mockImplementation(() => undefined)

    await fetchDMs({ ...base, user: ['alice', 'bob', 'carol'], limit: 50, since: '' })

    const warnings = warn.mock.calls.flat().join('\n')
    expect(warnings).toContain('Warning: User @alice was not found.')
    expect(warnings).toContain('Warning: No direct-message channel exists with @bob.')
    expect(warnings).not.toContain('REMOTE_SECRET_BODY')
  })

  test.each([
    500, 401,
  ])('propagates %i lookup errors without leaking the remote body', async (status) => {
    vi.stubGlobal(
      'fetch',
      vi.fn(async (input: string | URL | Request) => {
        const path = new URL(typeof input === 'string' || input instanceof URL ? input : input.url)
          .pathname
        if (path.endsWith('/users/me')) return response(me)
        if (path.endsWith('/users/me/channels')) return response([])
        return response({ message: 'REMOTE_SECRET_BODY' }, status)
      }),
    )

    await expect(
      fetchDMs({ ...base, user: ['alice'], limit: 50, since: '' } satisfies DMsOptions),
    ).rejects.toThrow(`API request failed: ${status}`)
    await expect(
      fetchDMs({ ...base, user: ['alice'], limit: 50, since: '' } satisfies DMsOptions),
    ).rejects.not.toThrow('REMOTE_SECRET_BODY')
  })

  test('propagates a malformed successful user lookup', async () => {
    vi.stubGlobal(
      'fetch',
      vi.fn(async (input: string | URL | Request) => {
        const path = new URL(typeof input === 'string' || input instanceof URL ? input : input.url)
          .pathname
        if (path.endsWith('/users/me')) return response(me)
        if (path.endsWith('/users/me/channels')) return response([])
        return response({ username: 'alice' })
      }),
    )

    await expect(
      fetchDMs({ ...base, user: ['alice'], limit: 50, since: '' } satisfies DMsOptions),
    ).rejects.toThrow('Mattermost returned an invalid user response.')
  })
})

test('unread propagates a direct-channel membership request failure', async () => {
  vi.stubGlobal(
    'fetch',
    vi.fn(async (input: string | URL | Request) => {
      const path = new URL(typeof input === 'string' || input instanceof URL ? input : input.url)
        .pathname
      if (path.endsWith('/users/me')) return response(me)
      if (path.endsWith('/users/me/teams')) {
        return response([{ id: 'team', name: 'team', display_name: 'Team', type: 'O' }])
      }
      if (path.endsWith('/users/me/channels')) return response([{ ...dm, total_msg_count: 1 }])
      if (path.endsWith('/users/me/teams/team/channels/members')) return response([])
      if (path.endsWith('/channels/dm/members/me')) {
        return response({ message: 'REMOTE_SECRET_BODY' }, 500)
      }
      throw new Error(`unexpected route ${path}`)
    }),
  )
  const log = vi.spyOn(console, 'log').mockImplementation(() => undefined)

  await expect(showUnread({ ...base } satisfies UnreadOptions)).rejects.toThrow(
    'API request failed: 500',
  )
  expect(log).not.toHaveBeenCalledWith('All caught up!')
})
