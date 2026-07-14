import { afterEach, describe, expect, test, vi } from 'vitest'
import { clearUserCache } from '../src/api/users'
import { listTeams, normalizeTeams, normalizeWhoAmI, showWhoAmI } from '../src/cli'
import type { Team } from '../src/types'
import { installRouteFetch } from './helpers/fake-fetch'

const options = { url: 'https://mattermost.test', token: 'token', json: true, redact: true }

afterEach(() => {
  clearUserCache()
  vi.restoreAllMocks()
  vi.unstubAllGlobals()
})

describe('whoami', () => {
  test('whitelists, redacts, and safely normalizes identity fields', async () => {
    const { requests } = installRouteFetch([
      {
        method: 'GET',
        path: '/api/v4/users/me',
        handle: () => ({
          id: 'user\u001b[31m',
          username: 'arda',
          first_name: 'AKIAIOSFODNN7EXAMPLE',
          last_name: 42,
          nickname: null,
          roles: 'system_user   team_user',
          email: 'private@example.com',
          auth_service: 'saml',
          props: { secret: 'never' },
          notify_props: { email: 'true' },
        }),
      },
    ])
    const log = vi.spyOn(console, 'log').mockImplementation(() => undefined)

    await showWhoAmI(options)

    const output = JSON.parse(String(log.mock.calls[0]?.[0]))
    expect(output).toEqual({
      id: 'user\\u001b[31m',
      username: 'arda',
      displayName: 'AK...LE',
      roles: ['system_user', 'team_user'],
    })
    expect(JSON.stringify(output)).not.toMatch(/private@example|auth_service|props|notify/)
    expect(requests.map(({ method }) => method)).toEqual(['GET'])
  })

  test('tolerates malformed optional profile fields', () => {
    const output = normalizeWhoAmI({
      id: 'me',
      username: 'arda',
      first_name: '' as never,
      last_name: {} as never,
      nickname: [] as never,
      roles: ['system_admin'] as never,
      email: 'hidden@example.com',
    })

    expect(output).toEqual({ id: 'me', username: 'arda', roles: [] })
  })

  test.each([
    null,
    [],
    { id: 'secret-identity', username: 7 },
    { id: '', username: 'secret-identity' },
    { id: 'me', username: '' },
  ])('fails closed for malformed identity data without echoing it: %j', (value) => {
    expect(() => normalizeWhoAmI(value)).toThrow('Invalid identity response.')
    try {
      normalizeWhoAmI(value)
    } catch (error) {
      expect(String(error)).not.toContain('secret-identity')
    }
  })

  test('rejects a malformed identity API response', async () => {
    installRouteFetch([
      {
        method: 'GET',
        path: '/api/v4/users/me',
        handle: () => ({ id: 'remote-secret', username: null }),
      },
    ])

    await expect(showWhoAmI(options)).rejects.toThrow('Invalid identity response.')
  })
})

describe('teams', () => {
  test('uses one identity fetch, whitelists fields, redacts, and sorts by name then id', async () => {
    const { requests } = installRouteFetch([
      { method: 'GET', path: '/api/v4/users/me', handle: () => ({ id: 'me', username: 'arda' }) },
      {
        method: 'GET',
        path: '/api/v4/users/me/teams',
        handle: () => [
          { id: 'z', name: 'beta', display_name: 'Beta', type: 'I', email: 'hidden' },
          { id: 'b', name: 'alpha', display_name: null, type: 'O', allowed_domains: 'hidden' },
          {
            id: 'a',
            name: 'alpha',
            display_name: 'AKIAIOSFODNN7EXAMPLE',
            type: 'O',
          },
        ],
      },
    ])
    const log = vi.spyOn(console, 'log').mockImplementation(() => undefined)

    await listTeams(options)

    const output = JSON.parse(String(log.mock.calls[0]?.[0]))
    expect(output).toEqual([
      { id: 'a', name: 'alpha', displayName: 'AK...LE', type: 'open' },
      { id: 'b', name: 'alpha', type: 'open' },
      { id: 'z', name: 'beta', displayName: 'Beta', type: 'invite_only' },
    ])
    expect(JSON.stringify(output)).not.toMatch(/email|allowed_domains|hidden/)
    expect(requests.map(({ method, url }) => `${method} ${url.pathname}`)).toEqual([
      'GET /api/v4/users/me',
      'GET /api/v4/users/me/teams',
    ])
  })

  test('returns an empty JSON array successfully', async () => {
    installRouteFetch([
      { method: 'GET', path: '/api/v4/users/me', handle: () => ({ id: 'me', username: 'arda' }) },
      { method: 'GET', path: '/api/v4/users/me/teams', handle: () => [] },
    ])
    const log = vi.spyOn(console, 'log').mockImplementation(() => undefined)

    await listTeams(options)

    expect(JSON.parse(String(log.mock.calls[0]?.[0]))).toEqual([])
  })

  test('prints the exact empty human result', async () => {
    installRouteFetch([
      { method: 'GET', path: '/api/v4/users/me', handle: () => ({ id: 'me', username: 'arda' }) },
      { method: 'GET', path: '/api/v4/users/me/teams', handle: () => [] },
    ])
    const log = vi.spyOn(console, 'log').mockImplementation(() => undefined)

    await listTeams({ ...options, json: false })

    expect(log).toHaveBeenCalledTimes(1)
    expect(log).toHaveBeenCalledWith('No teams found.')
  })

  test('sanitizes human-facing names and ids and tolerates malformed optional fields', () => {
    const output = normalizeTeams([
      {
        id: 'id\u001b[2J',
        name: 'team\nname',
        display_name: 7 as never,
        type: 'I',
      },
    ] satisfies Team[])

    expect(output).toEqual([{ id: 'id\\u001b[2J', name: 'team\\nname', type: 'invite_only' }])
  })

  test.each([
    null,
    {},
    [null],
    [['secret-team']],
    [{ id: 'secret-team', name: 'alpha', type: 'X' }],
    [{ id: '', name: 'secret-team', type: 'O' }],
    [{ id: 'team', name: '', type: 'I' }],
    [{ id: 'team', name: 'secret-team' }],
  ])('fails closed for malformed team data without echoing it: %j', (value) => {
    expect(() => normalizeTeams(value)).toThrow('Invalid teams response.')
    try {
      normalizeTeams(value)
    } catch (error) {
      expect(String(error)).not.toContain('secret-team')
    }
  })

  test('rejects a malformed top-level teams API response', async () => {
    installRouteFetch([
      { method: 'GET', path: '/api/v4/users/me', handle: () => ({ id: 'me', username: 'arda' }) },
      {
        method: 'GET',
        path: '/api/v4/users/me/teams',
        handle: () => ({ secret: 'remote-team-payload' }),
      },
    ])

    await expect(listTeams(options)).rejects.toThrow('Invalid teams response.')
  })

  test('rejects an unknown team type from the API', async () => {
    installRouteFetch([
      { method: 'GET', path: '/api/v4/users/me', handle: () => ({ id: 'me', username: 'arda' }) },
      {
        method: 'GET',
        path: '/api/v4/users/me/teams',
        handle: () => [{ id: 'team', name: 'alpha', type: 'remote-secret-type' }],
      },
    ])

    await expect(listTeams(options)).rejects.toThrow('Invalid teams response.')
  })
})
