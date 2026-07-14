import { afterEach, describe, expect, test, vi } from 'vitest'
import { clearUserCache, fetchUsers, initClient } from '../src/api'
import { listUsers, normalizeUsers, resolveUsersTeamId } from '../src/cli'
import { installRouteFetch } from './helpers/fake-fetch'

const options = {
  url: 'https://mattermost.test',
  token: 'token',
  json: true,
  redact: true,
  limit: 2,
}

afterEach(() => {
  clearUserCache()
  vi.restoreAllMocks()
  vi.unstubAllGlobals()
})

describe('users API', () => {
  test('lists active users with a limit-plus-one probe', async () => {
    const { requests } = installRouteFetch([
      { method: 'GET', path: '/api/v4/users', handle: () => [] },
    ])
    initClient(options.url, options.token)
    await fetchUsers({ limit: 20 })
    expect(requests[0]?.url.search).toBe('?page=0&per_page=21&active=true')
  })

  test('search is the only semantic read POST and carries team and limit-plus-one', async () => {
    const { requests } = installRouteFetch([
      { method: 'POST', path: '/api/v4/users/search', handle: () => [] },
    ])
    initClient(options.url, options.token)
    await fetchUsers({ query: 'arda', teamId: 'team', limit: 20 })
    expect(requests).toHaveLength(1)
    expect(requests[0]).toMatchObject({
      method: 'POST',
      body: { term: 'arda', team_id: 'team', limit: 21, allow_inactive: false },
    })
  })

  test('reports unknown truncation when the 200-user API cap prevents probing', async () => {
    installRouteFetch([
      {
        method: 'GET',
        path: '/api/v4/users',
        handle: () => Array.from({ length: 200 }, () => ({})),
      },
    ])
    initClient(options.url, options.token)
    await expect(fetchUsers({ limit: 300 })).resolves.toMatchObject({ truncated: null })
  })

  test('search probes above the list ceiling up to its 1000-user endpoint limit', async () => {
    const { requests } = installRouteFetch([
      { method: 'POST', path: '/api/v4/users/search', handle: () => [] },
    ])
    initClient(options.url, options.token)
    await fetchUsers({ query: 'dev', limit: 300 })
    expect(requests[0]?.body).toMatchObject({ limit: 301 })
  })

  test('reports unknown truncation at the full 1000-user search ceiling', async () => {
    installRouteFetch([
      {
        method: 'POST',
        path: '/api/v4/users/search',
        handle: () => Array.from({ length: 1000 }, () => ({})),
      },
    ])
    initClient(options.url, options.token)
    await expect(fetchUsers({ query: 'dev', limit: 1000 })).resolves.toMatchObject({
      truncated: null,
    })
  })
})

describe('users command', () => {
  test('whitelists, redacts, sorts, slices, and reports truncation', async () => {
    const { requests } = installRouteFetch([
      {
        method: 'POST',
        path: '/api/v4/users/search',
        handle: () => [
          { id: 'z', username: 'zed', email: 'private@example.com', roles: 'system_admin' },
          {
            id: 'a\u001b[2J',
            username: 'alice',
            first_name: 'AKIAIOSFODNN7EXAMPLE',
            nickname: 'ally',
            auth_service: 'saml',
            props: { hidden: true },
            notify_props: { email: 'true' },
          },
          { id: 'b', username: 'bob' },
        ],
      },
    ])
    const log = vi.spyOn(console, 'log').mockImplementation(() => undefined)
    await listUsers({ ...options, query: '  dev  ' })
    const output = JSON.parse(String(log.mock.calls[0]?.[0]))
    expect(output).toEqual({
      users: [
        { id: 'a\\u001b[2J', username: 'alice', displayName: 'AK...LE', nickname: 'ally' },
        { id: 'b', username: 'bob' },
      ],
      retrieval: {
        selectedCount: 2,
        requestedLimit: 2,
        query: 'dev',
        teamId: null,
        truncated: true,
      },
    })
    expect(JSON.stringify(output)).not.toMatch(/private@example|roles|auth_service|props|notify/)
    expect(requests.map(({ method }) => method)).toEqual(['POST'])
  })

  test('resolves a team with one identity fetch and filters a list request', async () => {
    const { requests } = installRouteFetch([
      { method: 'GET', path: '/api/v4/users/me', handle: () => ({ id: 'me', username: 'me' }) },
      {
        method: 'GET',
        path: '/api/v4/users/me/teams',
        handle: () => [{ id: 'team', name: 'core', display_name: 'Core', type: 'O' }],
      },
      { method: 'GET', path: '/api/v4/users', handle: () => [] },
    ])
    const log = vi.spyOn(console, 'log').mockImplementation(() => undefined)
    await listUsers({ ...options, team: 'core' })
    expect(JSON.parse(String(log.mock.calls[0]?.[0]))).toEqual({
      users: [],
      retrieval: {
        selectedCount: 0,
        requestedLimit: 2,
        query: null,
        teamId: 'team',
        truncated: false,
      },
    })
    expect(requests.filter(({ url }) => url.pathname.endsWith('/users/me'))).toHaveLength(1)
    expect(requests[2]?.url.searchParams.get('in_team')).toBe('team')
  })

  test.each([
    null,
    {},
    [null],
    [{ id: 'team', name: 'core', display_name: 'Core' }],
    [{ id: 'team', name: 'core', display_name: 7, type: 'O' }],
    [{ id: '', name: 'core', display_name: 'Core', type: 'O' }],
  ])('rejects malformed team payloads before resolution: %j', (teams) => {
    expect(() => resolveUsersTeamId(teams, 'core')).toThrow('Invalid teams response.')
  })

  test('matches canonical team fields but redacts remote names in resolution errors', () => {
    const secret = 'AKIAIOSFODNN7EXAMPLE'
    const teams = [{ id: 'team', name: secret, display_name: 'Core', type: 'O' }]
    expect(resolveUsersTeamId(teams, 'Core')).toBe('team')
    expect(() => resolveUsersTeamId(teams, 'missing')).toThrow('Your teams: AK...LE')
    expect(() => resolveUsersTeamId(teams, 'missing')).not.toThrow(secret)
  })

  test('prints compact human rows, coverage, and the exact empty result', async () => {
    installRouteFetch([{ method: 'GET', path: '/api/v4/users', handle: () => [] }])
    const log = vi.spyOn(console, 'log').mockImplementation(() => undefined)
    await listUsers({ ...options, json: false })
    expect(log).toHaveBeenCalledWith('No users found.')
  })

  test.each([
    null,
    {},
    [null],
    [{ id: 'secret-user', username: 7 }],
    [{ id: '', username: 'x' }],
  ])('fails closed generically for malformed required data: %j', (value) => {
    expect(() => normalizeUsers(value)).toThrow('Invalid users response.')
  })

  test('ignores malformed optional fields', () => {
    expect(
      normalizeUsers([{ id: 'u', username: 'user', first_name: 7, last_name: {}, nickname: [] }]),
    ).toEqual([{ id: 'u', username: 'user' }])
  })

  test('sanitizes and redacts the query echoed in retrieval metadata', async () => {
    installRouteFetch([{ method: 'POST', path: '/api/v4/users/search', handle: () => [] }])
    const log = vi.spyOn(console, 'log').mockImplementation(() => undefined)
    await listUsers({ ...options, query: 'AKIAIOSFODNN7EXAMPLE\u001b[2J' })
    expect(JSON.parse(String(log.mock.calls[0]?.[0])).retrieval.query).toBe('AK...LE\\u001b[2J')
  })
})
