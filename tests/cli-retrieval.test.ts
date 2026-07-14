import { afterEach, describe, expect, test, vi } from 'vitest'
import { clearUserCache } from '../src/api/users'
import {
  BoundedPostIdSet,
  createWatchPostHandler,
  fetchDMs,
  hasLiteralMention,
  isExactMentionPost,
  mentionSearchAfterDate,
} from '../src/cli'
import type { Channel, Post, PostsResponse, User } from '../src/types'
import { installRouteFetch } from './helpers/fake-fetch'

afterEach(() => {
  clearUserCache()
  vi.restoreAllMocks()
  vi.unstubAllGlobals()
})

describe('literal mention filtering', () => {
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
      { id: 'dm-alice', type: 'D', name: 'alice__me' } as Channel,
      { id: 'dm-bob', type: 'D', name: 'bob__me' } as Channel,
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
    }>
    expect(output.flatMap(({ messages }) => messages.map(({ id }) => id))).toEqual([
      'alice-new',
      'bob-new',
    ])
    expect(requests.filter(({ url }) => url.pathname.endsWith('/dm-alice/posts'))).toHaveLength(1)
    expect(requests.filter(({ url }) => url.pathname.endsWith('/dm-bob/posts'))).toHaveLength(1)
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
})

function responsePage(posts: Post[]): PostsResponse {
  return {
    order: posts.map(({ id }) => id),
    posts: Object.fromEntries(posts.map((post) => [post.id, post])),
  } as PostsResponse
}
