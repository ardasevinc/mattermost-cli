import { afterEach, describe, expect, test, vi } from 'vitest'
import { initClient } from '../src/api/client'
import { clearUserCache } from '../src/api/users'
import {
  BoundedPostIdSet,
  createWatchPostHandler,
  fetchDMs,
  fetchThread,
  hasLiteralMention,
  hydrateVisibleThreads,
  isExactMentionPost,
  mentionSearchAfterDate,
  mergeTruncation,
} from '../src/cli'
import { setActiveMattermostCredential } from '../src/preprocessing'
import type { Channel, Post, PostsResponse, Redaction, User } from '../src/types'
import { installRouteFetch } from './helpers/fake-fetch'

afterEach(() => {
  setActiveMattermostCredential(undefined)
  clearUserCache()
  vi.restoreAllMocks()
  vi.unstubAllGlobals()
})

describe('literal mention filtering', () => {
  test.each([
    [[false, false], 2, 2, false],
    [[false, false], 3, 2, true],
    [[true, false], 2, 2, true],
    [[null, false], 2, 2, null],
  ] as const)('merges global truncation states %j with %i candidates / %i limit', (states, candidates, limit, expected) => {
    expect(mergeTruncation([...states], candidates, limit)).toBe(expected)
  })

  test('ignores empty configured literals', () => {
    expect(hasLiteralMention('anything', [''])).toBe(false)
  })

  test('matches configured literals case-insensitively', () => {
    expect(hasLiteralMention('Hello ARDA SEVINC', ['Arda Sevinc'])).toBe(true)
  })

  test('does not treat search-engine token matches as literal mentions', () => {
    expect(hasLiteralMention('Arda discussed this separately', ['Arda Sevinc'])).toBe(false)
  })

  test.each([
    ['Arda', true],
    ['(Arda)', true],
    ['hello\nArda\nthere', true],
    ['Ardahan', false],
    ['xArda', false],
    ['Arda.com', true],
    ['Arda_more', true],
    ['Arda-name', true],
    ['Arda2', false],
    ['Ardağ', false],
    ['İArda', false],
    ['Arda東京', false],
    ['🙂Arda🙂', true],
  ])('applies literal alias boundaries to %j', (message, expected) => {
    expect(hasLiteralMention(message, ['Arda'])).toBe(expected)
  })

  test.each([
    ['@arda', true],
    ['(@arda)', true],
    ['hey\n@arda\nthere', true],
    ['foo@arda', false],
    ['foo@arda.com', false],
    ['@arda.com', false],
    ['@arda_more', false],
    ['@arda-more', false],
    ['@arda2', false],
    ['x@arda!', false],
  ])('applies username boundaries to %j', (message, expected) => {
    expect(hasLiteralMention(message, ['@arda'])).toBe(expected)
  })

  test('applies the exact millisecond boundary after search retrieval', () => {
    const candidate = { message: '@arda', delete_at: 0 } as Post
    expect(isExactMentionPost({ ...candidate, create_at: 999 }, '@arda', 1000)).toBe(false)
    expect(isExactMentionPost({ ...candidate, create_at: 1000 }, '@arda', 1000)).toBe(true)
  })

  test('widens the coarse after query by one UTC calendar day', () => {
    const since = Date.UTC(2026, 6, 14, 15, 30)
    expect(`after:${mentionSearchAfterDate(since)}`).toBe('after:2026-07-13')
  })

  test('dedupes repeated DM targets before fetching and enforces one global limit', async () => {
    const now = Date.now()
    const users = {
      me: { id: 'me', username: 'me' } as User,
      alice: { id: 'alice', username: 'alice' } as User,
      bob: { id: 'bob', username: 'bob' } as User,
    }
    const channels = [
      { id: 'dm-alice', team_id: '', type: 'D', name: 'alice__me', display_name: '' } as Channel,
      { id: 'dm-bob', team_id: '', type: 'D', name: 'bob__me', display_name: '' } as Channel,
    ]
    const makePost = (id: string, channelId: string, userId: string, createAt: number) =>
      ({
        id,
        channel_id: channelId,
        user_id: userId,
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
      }) satisfies Post
    const pages: Record<string, Post[]> = {
      'dm-alice': [makePost('alice-new', 'dm-alice', 'alice', now - 1)],
      'dm-bob': [makePost('bob-new', 'dm-bob', 'bob', now - 2)],
    }
    const { requests } = installRouteFetch([
      { method: 'GET', path: '/api/v4/users/me', handle: () => users.me },
      {
        method: 'GET',
        path: '/api/v4/users/username/alice',
        handle: () => users.alice,
      },
      { method: 'GET', path: '/api/v4/users/username/bob', handle: () => users.bob },
      { method: 'GET', path: '/api/v4/users/me/channels', handle: () => channels },
      {
        method: 'GET',
        path: '/api/v4/channels/dm-alice/posts',
        handle: () => responsePage(pages['dm-alice'] ?? []),
      },
      {
        method: 'GET',
        path: '/api/v4/channels/dm-bob/posts',
        handle: () => responsePage(pages['dm-bob'] ?? []),
      },
    ])
    const log = vi.spyOn(console, 'log').mockImplementation(() => undefined)

    await fetchDMs({
      url: 'https://mattermost.test',
      token: 'token',
      json: true,
      color: false,
      relative: false,
      redact: true,
      threads: false,
      user: ['alice', 'alice', 'bob'],
      limit: 2,
      since: '1m',
    })

    const output = JSON.parse(String(log.mock.calls.at(-1)?.[0])) as Array<{
      messages: Array<{ id: string }>
      retrieval: { selection: { queryTruncated: boolean | null } }
    }>
    expect(output.flatMap(({ messages }) => messages.map(({ id }) => id))).toEqual([
      'alice-new',
      'bob-new',
    ])
    expect(output.every(({ retrieval }) => retrieval.selection.queryTruncated === false)).toBe(true)
    expect(requests.filter(({ url }) => url.pathname.endsWith('/dm-alice/posts'))).toHaveLength(1)
    expect(requests.filter(({ url }) => url.pathname.endsWith('/dm-bob/posts'))).toHaveLength(1)
  })

  test.each([
    ['blank name', { id: 'dm', team_id: '', type: 'D', name: '', display_name: '' }],
    [
      'malformed direct name',
      { id: 'dm', team_id: '', type: 'D', name: 'only-one-user', display_name: '' },
    ],
    [
      'nonempty direct team',
      { id: 'dm', team_id: 'team', type: 'D', name: 'me__other', display_name: '' },
    ],
  ] as const)('fails closed for a discovered direct channel with %s', async (_label, malformed) => {
    const { requests } = installRouteFetch([
      { method: 'GET', path: '/api/v4/users/me', handle: () => ({ id: 'me', username: 'me' }) },
      { method: 'GET', path: '/api/v4/users/me/channels', handle: () => [malformed] },
    ])

    await expect(
      fetchDMs({
        url: 'https://mattermost.test',
        token: 'token',
        json: true,
        color: false,
        relative: false,
        redact: true,
        threads: false,
        user: [],
        limit: 1,
        since: '',
      }),
    ).rejects.toThrow('Mattermost returned an invalid channel response.')
    expect(requests.some(({ url }) => url.pathname.endsWith('/posts'))).toBe(false)
  })

  test('validates every discovered direct channel before filtered-user matching', async () => {
    const malformed = {
      id: 'dm-alice',
      team_id: '',
      type: 'D',
      name: 'only-one-user',
      display_name: '',
    }
    const validDuplicate = {
      ...malformed,
      name: 'alice__me',
    }
    const { requests } = installRouteFetch([
      { method: 'GET', path: '/api/v4/users/me', handle: () => ({ id: 'me', username: 'me' }) },
      {
        method: 'GET',
        path: '/api/v4/users/me/channels',
        handle: () => [malformed, validDuplicate],
      },
    ])
    const error = vi.spyOn(console, 'error').mockImplementation(() => undefined)

    await expect(
      fetchDMs({
        url: 'https://mattermost.test',
        token: 'token',
        json: true,
        color: false,
        relative: false,
        redact: true,
        threads: false,
        user: ['alice'],
        limit: 1,
        since: '',
      }),
    ).rejects.toThrow('Mattermost returned an invalid channel response.')
    expect(requests.some(({ url }) => url.pathname.includes('/users/username/'))).toBe(false)
    expect(requests.some(({ url }) => url.pathname.endsWith('/posts'))).toBe(false)
    expect(error).not.toHaveBeenCalled()
  })

  test('fails closed when a discovered direct channel excludes the current user', async () => {
    const wrongParticipants = {
      id: 'dm-unrelated',
      team_id: '',
      type: 'D',
      name: 'alice__bob',
      display_name: '',
    }
    const { requests } = installRouteFetch([
      { method: 'GET', path: '/api/v4/users/me', handle: () => ({ id: 'me', username: 'me' }) },
      { method: 'GET', path: '/api/v4/users/me/channels', handle: () => [wrongParticipants] },
    ])

    await expect(
      fetchDMs({
        url: 'https://mattermost.test',
        token: 'token',
        json: true,
        color: false,
        relative: false,
        redact: true,
        threads: false,
        user: [],
        limit: 1,
        since: '',
      }),
    ).rejects.toThrow('Mattermost returned an invalid channel response.')
    expect(requests.some(({ url }) => url.pathname.endsWith('/posts'))).toBe(false)
  })

  test('hydrates only threads surviving the final global DM selection', async () => {
    const now = Date.now()
    const channels = [
      { id: 'dm-alice', team_id: '', type: 'D', name: 'alice__me', display_name: '' } as Channel,
      { id: 'dm-bob', team_id: '', type: 'D', name: 'bob__me', display_name: '' } as Channel,
    ]
    const aliceSeed = { ...makeThreadPost('alice-seed', 'alice-root', now), channel_id: 'dm-alice' }
    const bobSeed = {
      ...makeThreadPost('bob-seed', 'bob-root', now - 1),
      channel_id: 'dm-bob',
      user_id: 'bob',
    }
    const aliceRoot = {
      ...makeThreadPost('alice-root', '', now - 2, 1),
      channel_id: 'dm-alice',
    }
    const { requests } = installRouteFetch([
      { method: 'GET', path: '/api/v4/users/me', handle: () => ({ id: 'me', username: 'me' }) },
      {
        method: 'GET',
        path: '/api/v4/users/alice',
        handle: () => ({ id: 'alice', username: 'alice' }),
      },
      { method: 'GET', path: '/api/v4/users/me/channels', handle: () => channels },
      {
        method: 'GET',
        path: '/api/v4/channels/dm-alice/posts',
        handle: () => responsePage([aliceSeed]),
      },
      {
        method: 'GET',
        path: '/api/v4/channels/dm-bob/posts',
        handle: () => responsePage([bobSeed]),
      },
      {
        method: 'GET',
        path: '/api/v4/posts/alice-root/thread',
        handle: () => ({ ...responsePage([aliceRoot, aliceSeed]), has_next: false }),
      },
    ])
    const log = vi.spyOn(console, 'log').mockImplementation(() => undefined)

    await fetchDMs({
      url: 'https://mattermost.test',
      token: 'token',
      json: true,
      color: false,
      relative: false,
      redact: true,
      threads: true,
      user: [],
      limit: 1,
      since: '1m',
    })

    const [output] = JSON.parse(String(log.mock.calls.at(-1)?.[0]))
    expect(output.retrieval.selection.selectedCount).toBe(1)
    expect(output.retrieval.visiblePostCount).toBe(2)
    expect(
      requests.filter(({ url }) => url.pathname.includes('/posts/alice-root/thread')),
    ).toHaveLength(1)
    expect(requests.some(({ url }) => url.pathname.includes('/posts/bob-root/thread'))).toBe(false)
  })
})

describe('watch post deduplication', () => {
  test('suppresses repeats while bounding retained post ids', () => {
    const ids = new BoundedPostIdSet(2)
    expect(ids.add('one')).toBe(true)
    expect(ids.add('one')).toBe(false)
    expect(ids.add('two')).toBe(true)
    expect(ids.add('three')).toBe(true)
    expect(ids.add('one')).toBe(true)
  })

  test('writes synchronous redacted JSONL in event order and suppresses duplicate ids', () => {
    const lines: string[] = []
    const handlePost = createWatchPostHandler({ json: true, color: false, redact: true }, (line) =>
      lines.push(line),
    )
    const makePost = (id: string, message: string, createAt: number) =>
      ({
        id,
        channel_id: 'channel-1',
        user_id: 'user-1',
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
      }) satisfies Post

    handlePost(makePost('one', 'first sk-abcdefghijklmnopqrstuvwxyz123456', 1), 'town', 'arda')
    handlePost(makePost('two', 'second', 2), 'town', 'arda')
    handlePost(makePost('one', 'duplicate', 3), 'town', 'arda')

    expect(lines).toHaveLength(2)
    expect(lines.every((line) => !line.includes('\n'))).toBe(true)
    expect(lines.map((line) => JSON.parse(line).postId)).toEqual(['one', 'two'])
    expect(JSON.parse(lines[0] as string).message).not.toContain(
      'sk-abcdefghijklmnopqrstuvwxyz123456',
    )
  })

  test('sanitizes every remotely controlled watch presentation field', () => {
    const token = `ghp_${'a'.repeat(36)}`
    const lines: string[] = []
    const handlePost = createWatchPostHandler({ json: true, color: false, redact: true }, (line) =>
      lines.push(line),
    )
    handlePost(
      {
        id: `post\u001b${token}`,
        channel_id: `channel\u001b${token}`,
        user_id: `user\u001b${token}`,
        create_at: 1,
        update_at: 1,
        delete_at: 0,
        edit_at: 0,
        message: `message\u001b${token}`,
        type: '',
        props: {},
        hashtags: '',
        file_ids: [`file\u001b${token}`],
        root_id: `root\u001b${token}`,
        reply_count: 0,
        pending_post_id: '',
      },
      `town\u001b${token}`,
      `sender\u001b${token}`,
    )

    expect(lines).toHaveLength(1)
    expect(lines[0]).not.toContain(token)
    expect(lines[0]).not.toContain('\u001b')
    expect(JSON.parse(lines[0] as string).redactions.map(({ field }: Redaction) => field)).toEqual(
      expect.arrayContaining([
        'watch.sender',
        'watch.message',
        'watch.postId',
        'watch.channelId',
        'watch.channelName',
        'watch.senderId',
        'watch.rootId',
        'watch.fileId',
      ]),
    )
  })

  test('protects the active credential in JSON watch output with --no-redact', () => {
    const credential = '9xuqwrwgstrb3mzrxb83nb357a'
    initClient('https://mattermost.test', credential)
    const lines: string[] = []
    const handlePost = createWatchPostHandler({ json: true, color: false, redact: false }, (line) =>
      lines.push(line),
    )
    handlePost(
      {
        id: 'post',
        channel_id: 'channel',
        user_id: 'user',
        create_at: 1,
        update_at: 1,
        delete_at: 0,
        edit_at: 0,
        message: `active=${credential} unrelated=aaaaaaaaaaaaaaaaaaaaaaaaaa`,
        type: '',
        props: {},
        hashtags: '',
        file_ids: [],
        root_id: '',
        reply_count: 0,
        pending_post_id: '',
      },
      'town',
      'sender',
    )
    expect(lines[0]).not.toContain(credential)
    expect(lines[0]).toContain('aaaaaaaaaaaaaaaaaaaaaaaaaa')
    expect(JSON.parse(lines[0] as string).redactions).toEqual([
      expect.objectContaining({ type: 'mattermost_credential', field: 'watch.message' }),
    ])
  })
})

describe('thread retrieval metadata', () => {
  test('hydrates a reply seed with its root and siblings once', async () => {
    const root = makeThreadPost('root', '', 1, 2)
    const reply = makeThreadPost('reply', 'root', 2)
    const sibling = makeThreadPost('sibling', 'root', 3)
    const { requests } = installRouteFetch([
      {
        method: 'GET',
        path: '/api/v4/posts/root/thread',
        handle: () => ({ ...responsePage([root, reply, sibling]), has_next: false }),
      },
    ])
    initClient('https://mattermost.test', 'token')

    const result = await hydrateVisibleThreads([reply, sibling], true)

    expect(result.posts.map(({ id }) => id)).toEqual(['reply', 'sibling', 'root'])
    expect(result.visibleThreads).toEqual({
      status: 'complete',
      hydratedRootCount: 1,
      failedRootIds: [],
    })
    expect(requests).toHaveLength(1)
  })

  test('hydrates a selected root with its replies', async () => {
    const root = makeThreadPost('root', '', 1, 1)
    const reply = makeThreadPost('reply', 'root', 2)
    const { requests } = installRouteFetch([
      {
        method: 'GET',
        path: '/api/v4/posts/root/thread',
        handle: () => ({ ...responsePage([root, reply]), has_next: false }),
      },
    ])
    initClient('https://mattermost.test', 'token')

    expect((await hydrateVisibleThreads([root], true)).posts.map(({ id }) => id)).toEqual([
      'root',
      'reply',
    ])
    expect(requests).toHaveLength(1)
  })

  test('skips complete seed threads and makes no requests when threads are disabled', async () => {
    const root = makeThreadPost('root', '', 1, 1)
    const reply = makeThreadPost('reply', 'root', 2)
    const { requests } = installRouteFetch([])
    initClient('https://mattermost.test', 'token')

    expect((await hydrateVisibleThreads([root, reply], true)).visibleThreads.status).toBe(
      'complete',
    )
    expect((await hydrateVisibleThreads([reply], false)).visibleThreads.status).toBe(
      'not_requested',
    )
    expect(requests).toHaveLength(0)
  })

  test('isolates failures, sanitizes warnings, and preserves other hydrated roots', async () => {
    const badRootId = 'bad\u001b[31m-root'
    const goodSeed = makeThreadPost('good-seed', 'good-root', 2)
    const badSeed = makeThreadPost('bad-seed', badRootId, 3)
    const goodRoot = makeThreadPost('good-root', '', 1, 1)
    installRouteFetch([
      {
        method: 'GET',
        path: '/api/v4/posts/good-root/thread',
        handle: () => ({ ...responsePage([goodRoot, goodSeed]), has_next: false }),
      },
      {
        method: 'GET',
        path: `/api/v4/posts/${badRootId}/thread`,
        handle: () => {
          throw new Error('server included secret')
        },
      },
    ])
    initClient('https://mattermost.test', 'token')
    const error = vi.spyOn(console, 'error').mockImplementation(() => undefined)

    const result = await hydrateVisibleThreads([goodSeed, badSeed], true)

    expect(result.posts.map(({ id }) => id)).toEqual(['good-seed', 'bad-seed', 'good-root'])
    expect(result.visibleThreads).toEqual({
      status: 'partial',
      hydratedRootCount: 1,
      failedRootIds: [badRootId],
    })
    const warning = String(error.mock.calls.at(-1)?.[0])
    expect(warning).not.toContain('\u001b')
    expect(warning).not.toContain('server included secret')
  })

  test('preserves accumulated thread context when a later page fails', async () => {
    const root = makeThreadPost('root', '', 1, 3)
    const seed = makeThreadPost('seed', 'root', 3)
    const context = makeThreadPost('context', 'root', 2)
    installRouteFetch([
      {
        method: 'GET',
        path: '/api/v4/posts/root/thread',
        handle: ({ url }) => {
          if (url.searchParams.has('fromPost')) throw new Error('page two failed')
          return { ...responsePage([root, context, seed]), has_next: true }
        },
      },
    ])
    initClient('https://mattermost.test', 'token')
    vi.spyOn(console, 'error').mockImplementation(() => undefined)

    const result = await hydrateVisibleThreads([seed], true)

    expect(result.posts.map(({ id }) => id)).toEqual(['seed', 'root', 'context'])
    expect(result.visibleThreads).toEqual({
      status: 'partial',
      hydratedRootCount: 0,
      failedRootIds: ['root'],
    })
  })

  test('never exceeds four concurrent thread requests', async () => {
    const seeds = Array.from({ length: 9 }, (_, index) =>
      makeThreadPost(`seed-${index}`, `root-${index}`, index + 10),
    )
    let active = 0
    let maxActive = 0
    vi.stubGlobal(
      'fetch',
      vi.fn(async (input: string | URL | Request) => {
        active += 1
        maxActive = Math.max(maxActive, active)
        await new Promise((resolve) => setTimeout(resolve, 1))
        const url = new URL(typeof input === 'string' || input instanceof URL ? input : input.url)
        const rootId = url.pathname.split('/').at(-2) as string
        const index = Number(rootId.slice('root-'.length))
        const root = makeThreadPost(rootId, '', index, 1)
        active -= 1
        return Response.json({ ...responsePage([root, seeds[index] as Post]), has_next: false })
      }),
    )
    initClient('https://mattermost.test', 'token')

    expect((await hydrateVisibleThreads(seeds, true)).visibleThreads.status).toBe('complete')
    expect(maxActive).toBe(4)
  })

  test('reports concurrent failed roots in seed selection order', async () => {
    const seeds = [
      makeThreadPost('seed-first', 'root-first', 1),
      makeThreadPost('seed-second', 'root-second', 2),
    ]
    vi.stubGlobal(
      'fetch',
      vi.fn(async (input: string | URL | Request) => {
        const url = new URL(typeof input === 'string' || input instanceof URL ? input : input.url)
        if (url.pathname.includes('root-first')) {
          await new Promise((resolve) => setTimeout(resolve, 2))
          return Response.json({ ...responsePage([seeds[0] as Post]), has_next: false })
        }
        return Response.json({ ...responsePage([seeds[1] as Post]), has_next: false })
      }),
    )
    initClient('https://mattermost.test', 'token')
    vi.spyOn(console, 'error').mockImplementation(() => undefined)

    expect((await hydrateVisibleThreads(seeds, true)).visibleThreads.failedRootIds).toEqual([
      'root-first',
      'root-second',
    ])
  })

  test('uses a reply channel and reports a missing root as partial', async () => {
    const reply = {
      id: 'reply',
      channel_id: 'channel',
      user_id: 'me',
      create_at: 1,
      update_at: 1,
      delete_at: 0,
      edit_at: 0,
      message: 'reply',
      type: '',
      props: {},
      hashtags: '',
      file_ids: [],
      root_id: 'missing-root',
      reply_count: 0,
      pending_post_id: '',
    } satisfies Post
    installRouteFetch([
      { method: 'GET', path: '/api/v4/users/me', handle: () => ({ id: 'me', username: 'me' }) },
      {
        method: 'GET',
        path: '/api/v4/posts/missing-root/thread',
        handle: () => ({ ...responsePage([reply]), has_next: false }),
      },
      {
        method: 'GET',
        path: '/api/v4/channels/channel',
        handle: () => ({
          id: 'channel',
          team_id: 'team',
          type: 'O',
          name: 'general',
          display_name: 'General',
        }),
      },
    ])
    const log = vi.spyOn(console, 'log').mockImplementation(() => undefined)

    await fetchThread({
      url: 'https://mattermost.test',
      token: 'token',
      json: true,
      color: false,
      relative: false,
      redact: true,
      threads: true,
      postId: 'missing-root',
    })

    const [output] = JSON.parse(String(log.mock.calls.at(-1)?.[0]))
    expect(output.channel.id).toBe('channel')
    expect(output.retrieval.selection).toMatchObject({
      source: 'thread',
      selectedCount: 1,
      requestedLimit: null,
      since: null,
      queryTruncated: false,
    })
    expect(output.retrieval.visibleThreads).toEqual({
      status: 'partial',
      hydratedRootCount: 0,
      failedRootIds: ['missing-root'],
    })
  })

  test('classifies malformed channel metadata as unavailable and sanitizes its ID', async () => {
    const token = `ghp_${'a'.repeat(36)}`
    const unsafeRoot = `root\u001b${token}`
    const unsafeChannel = `channel\u001b${token}`
    const reply = {
      ...makeThreadPost('reply', unsafeRoot, 1),
      channel_id: unsafeChannel,
      user_id: 'me',
    }
    installRouteFetch([
      { method: 'GET', path: '/api/v4/users/me', handle: () => ({ id: 'me', username: 'me' }) },
      {
        method: 'GET',
        path: `/api/v4/posts/${encodeURIComponent(unsafeRoot)}/thread`,
        handle: () => ({ ...responsePage([reply]), has_next: false }),
      },
      {
        method: 'GET',
        path: `/api/v4/channels/${encodeURIComponent(unsafeChannel)}`,
        handle: () => null,
      },
    ])
    const log = vi.spyOn(console, 'log').mockImplementation(() => undefined)
    const error = vi.spyOn(console, 'error').mockImplementation(() => undefined)

    await fetchThread({
      url: 'https://mattermost.test',
      token: 'token',
      json: true,
      color: false,
      relative: false,
      redact: true,
      threads: true,
      postId: unsafeRoot,
    })

    const serialized = String(log.mock.calls.at(-1)?.[0])
    const [output] = JSON.parse(serialized)
    expect(output.channel).toEqual({
      id: expect.not.stringContaining(token),
      type: 'unknown',
      name: 'unknown',
      metadataStatus: 'unavailable',
    })
    expect(
      error.mock.calls
        .flat()
        .map(String)
        .filter((message) => message.startsWith('Warning: Channel metadata is unavailable')),
    ).toHaveLength(1)
    expect(serialized).not.toContain(token)
    expect(String(error.mock.calls.at(-1)?.[0])).not.toContain(token)
    expect(serialized).not.toContain('\u001b')
  })

  test.each([
    ['null body', null],
    ['non-object body', 'channel'],
    ['blank id', { id: '', team_id: 'team', type: 'O', name: 'general', display_name: 'General' }],
    [
      'mismatched id',
      { id: 'other', team_id: 'team', type: 'O', name: 'general', display_name: 'General' },
    ],
    [
      'unknown type',
      { id: 'channel', team_id: 'team', type: 'X', name: 'general', display_name: 'General' },
    ],
    [
      'blank public name',
      { id: 'channel', team_id: 'team', type: 'O', name: ' ', display_name: 'General' },
    ],
    [
      'non-string display name',
      { id: 'channel', team_id: 'team', type: 'O', name: 'general', display_name: null },
    ],
    ['missing team id', { id: 'channel', type: 'O', name: 'general', display_name: 'General' }],
    [
      'blank public team id',
      { id: 'channel', team_id: '', type: 'O', name: 'general', display_name: 'General' },
    ],
    [
      'malformed direct name',
      { id: 'channel', team_id: '', type: 'D', name: 'one-user', display_name: '' },
    ],
    [
      'direct channel excluding the current user',
      { id: 'channel', team_id: '', type: 'D', name: 'alice__bob', display_name: '' },
    ],
    [
      'nonempty direct team id',
      { id: 'channel', team_id: 'team', type: 'D', name: 'me__me', display_name: '' },
    ],
    [
      'blank group name',
      { id: 'channel', team_id: '', type: 'G', name: '', display_name: 'Group' },
    ],
    [
      'nonempty group team id',
      { id: 'channel', team_id: 'team', type: 'G', name: 'group', display_name: 'Group' },
    ],
  ] as const)('classifies malformed 200 channel metadata with %s as unavailable', async (_label, payload) => {
    const root = makeThreadPost('root', '', 1)
    installRouteFetch([
      { method: 'GET', path: '/api/v4/users/me', handle: () => ({ id: 'me', username: 'me' }) },
      {
        method: 'GET',
        path: '/api/v4/posts/root/thread',
        handle: () => ({ ...responsePage([root]), has_next: false }),
      },
      { method: 'GET', path: '/api/v4/channels/channel', handle: () => payload },
    ])
    const log = vi.spyOn(console, 'log').mockImplementation(() => undefined)
    const error = vi.spyOn(console, 'error').mockImplementation(() => undefined)

    await fetchThread({
      url: 'https://mattermost.test',
      token: 'token',
      json: true,
      color: false,
      relative: false,
      redact: true,
      threads: true,
      postId: 'root',
    })

    const [output] = JSON.parse(String(log.mock.calls.at(-1)?.[0]))
    expect(output.channel).toEqual({
      id: 'channel',
      type: 'unknown',
      name: 'unknown',
      metadataStatus: 'unavailable',
    })
    expect(output.messages.map(({ id }: { id: string }) => id)).toEqual(['root'])
    expect(
      error.mock.calls
        .flat()
        .map(String)
        .filter((message) => message.startsWith('Warning: Channel metadata is unavailable')),
    ).toEqual(['Warning: Channel metadata is unavailable for channel.'])
  })

  test.each([
    ['direct', { id: 'channel', team_id: '', type: 'D', name: 'me__me', display_name: '' }],
    ['group', { id: 'channel', team_id: '', type: 'G', name: 'group', display_name: 'Group' }],
  ] as const)('accepts a valid %s channel metadata shape', async (_label, payload) => {
    const root = makeThreadPost('root', '', 1)
    installRouteFetch([
      { method: 'GET', path: '/api/v4/users/me', handle: () => ({ id: 'me', username: 'me' }) },
      {
        method: 'GET',
        path: '/api/v4/posts/root/thread',
        handle: () => ({ ...responsePage([root]), has_next: false }),
      },
      { method: 'GET', path: '/api/v4/channels/channel', handle: () => payload },
    ])
    const log = vi.spyOn(console, 'log').mockImplementation(() => undefined)

    await fetchThread({
      url: 'https://mattermost.test',
      token: 'token',
      json: true,
      color: false,
      relative: false,
      redact: true,
      threads: true,
      postId: 'root',
    })

    const [output] = JSON.parse(String(log.mock.calls.at(-1)?.[0]))
    expect(output.channel.metadataStatus).toBe('resolved')
    expect(output.channel.type).toBe(_label === 'direct' ? 'dm' : 'group')
  })

  test.each([
    ['403', 403],
    ['404', 404],
    ['500', 500],
    ['network', null],
  ] as const)(
    'preserves a fetched thread when channel metadata fails with %s',
    async (_name, status) => {
      const remoteSecret = `sk-${'z'.repeat(40)}`
      const root = makeThreadPost('root', '', 1)
      vi.stubGlobal(
        'fetch',
        vi.fn(async (input: string | URL | Request) => {
          const path = new URL(
            typeof input === 'string' || input instanceof URL ? input : input.url,
          ).pathname
          if (path.endsWith('/users/me')) {
            return Response.json({ id: 'me', username: 'me' })
          }
          if (path.endsWith('/posts/root/thread')) {
            return Response.json({ ...responsePage([root]), has_next: false })
          }
          if (path.endsWith('/channels/channel')) {
            if (status === null) throw new Error(`transport ${remoteSecret}`)
            return Response.json({ message: remoteSecret }, { status })
          }
          throw new Error(`unexpected route ${path}`)
        }),
      )
      const log = vi.spyOn(console, 'log').mockImplementation(() => undefined)
      const error = vi.spyOn(console, 'error').mockImplementation(() => undefined)

      await fetchThread({
        url: 'https://mattermost.test',
        token: 'token',
        json: true,
        color: false,
        relative: false,
        redact: true,
        threads: true,
        postId: 'root',
      })

      const serialized = String(log.mock.calls.at(-1)?.[0])
      const [output] = JSON.parse(serialized)
      expect(output.messages.map(({ id }: { id: string }) => id)).toEqual(['root'])
      expect(output.channel).toEqual({
        id: 'channel',
        type: 'unknown',
        name: 'unknown',
        metadataStatus: 'unavailable',
      })
      expect(
        error.mock.calls
          .flat()
          .map(String)
          .filter((message) => message.startsWith('Warning: Channel metadata is unavailable')),
      ).toEqual(['Warning: Channel metadata is unavailable for channel.'])
      expect(`${serialized}\n${error.mock.calls.flat().join('\n')}`).not.toContain(remoteSecret)
    },
    10_000,
  )

  test('keeps a channel authentication failure fail-closed', async () => {
    const root = makeThreadPost('root', '', 1)
    vi.stubGlobal(
      'fetch',
      vi.fn(async (input: string | URL | Request) => {
        const path = new URL(typeof input === 'string' || input instanceof URL ? input : input.url)
          .pathname
        if (path.endsWith('/users/me')) return Response.json({ id: 'me', username: 'me' })
        if (path.endsWith('/posts/root/thread')) {
          return Response.json({ ...responsePage([root]), has_next: false })
        }
        if (path.endsWith('/channels/channel')) {
          return Response.json({ message: 'REMOTE_SECRET_BODY' }, { status: 401 })
        }
        throw new Error(`unexpected route ${path}`)
      }),
    )
    const log = vi.spyOn(console, 'log').mockImplementation(() => undefined)
    const error = vi.spyOn(console, 'error').mockImplementation(() => undefined)

    await expect(
      fetchThread({
        url: 'https://mattermost.test',
        token: 'token',
        json: true,
        color: false,
        relative: false,
        redact: true,
        threads: true,
        postId: 'root',
      }),
    ).rejects.toThrow('API request failed: 401')
    expect(log).not.toHaveBeenCalled()
    expect(error.mock.calls.flat().join('\n')).not.toContain('REMOTE_SECRET_BODY')
    expect(error.mock.calls.flat().join('\n')).not.toContain('Channel metadata is unavailable')
  })

  test('does not count an unproven root as hydrated in mm thread metadata', async () => {
    const root = makeThreadPost('root', '', 1, 2)
    installRouteFetch([
      { method: 'GET', path: '/api/v4/users/me', handle: () => ({ id: 'me', username: 'me' }) },
      {
        method: 'GET',
        path: '/api/v4/posts/root/thread',
        handle: () => responsePage([root]),
      },
      {
        method: 'GET',
        path: '/api/v4/channels/channel',
        handle: () => ({
          id: 'channel',
          team_id: 'team',
          type: 'O',
          name: 'general',
          display_name: 'General',
        }),
      },
    ])
    const log = vi.spyOn(console, 'log').mockImplementation(() => undefined)
    vi.spyOn(console, 'error').mockImplementation(() => undefined)

    await fetchThread({
      url: 'https://mattermost.test',
      token: 'token',
      json: true,
      color: false,
      relative: false,
      redact: true,
      threads: true,
      postId: 'root',
    })

    const [output] = JSON.parse(String(log.mock.calls.at(-1)?.[0]))
    expect(output.retrieval.visibleThreads).toEqual({
      status: 'partial',
      hydratedRootCount: 0,
      failedRootIds: ['root'],
    })
  })
})

function makeThreadPost(id: string, rootId: string, createAt: number, replyCount = 0): Post {
  return {
    id,
    channel_id: 'channel',
    user_id: 'me',
    create_at: createAt,
    update_at: createAt,
    delete_at: 0,
    edit_at: 0,
    message: id,
    type: '',
    props: {},
    hashtags: '',
    file_ids: [],
    root_id: rootId,
    reply_count: replyCount,
    pending_post_id: '',
  }
}

function responsePage(posts: Post[]): PostsResponse {
  return {
    order: posts.map(({ id }) => id),
    posts: Object.fromEntries(posts.map((post) => [post.id, post])),
  } as PostsResponse
}
