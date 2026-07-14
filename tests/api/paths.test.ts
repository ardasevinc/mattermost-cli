import { afterEach, describe, expect, test, vi } from 'vitest'
import { getChannel, getChannelByName, getMyChannels } from '../../src/api/channels'
import { initClient } from '../../src/api/client'
import { getChannelPosts, getPostThread, searchPosts } from '../../src/api/posts'
import { clearUserCache, getUser, getUserByUsername } from '../../src/api/users'

describe('Mattermost API paths', () => {
  afterEach(() => {
    clearUserCache()
    vi.unstubAllGlobals()
  })

  test('encodes every dynamic path segment while leaving query parameters intact', async () => {
    const urls: URL[] = []
    const responses: unknown[] = [
      { id: 'user/id', username: 'first' },
      { id: 'second', username: 'name/with space' },
      [],
      { id: 'channel/id' },
      { id: 'named-channel' },
      { order: [], posts: {}, has_next: false },
      { order: [], posts: {}, has_next: false },
      { order: [], posts: {}, matches: {}, has_next: false },
    ]
    vi.stubGlobal(
      'fetch',
      vi.fn(async (input: string | URL | Request) => {
        urls.push(new URL(String(input)))
        return Response.json(responses.shift())
      }),
    )
    initClient('https://mattermost.example.com', 'fake-token')

    await getUser('user/id')
    await getUserByUsername('name/with space')
    await getMyChannels('user/id')
    await getChannel('channel/id')
    await getChannelByName('team/id', '#release/name')
    await getChannelPosts('channel/id', { before: 'post/id' })
    await getPostThread('post/id')
    await searchPosts('team/id', 'term', 1)

    expect(urls.map(({ pathname }) => pathname)).toEqual([
      '/api/v4/users/user%2Fid',
      '/api/v4/users/username/name%2Fwith%20space',
      '/api/v4/users/user%2Fid/channels',
      '/api/v4/channels/channel%2Fid',
      '/api/v4/teams/team%2Fid/channels/name/release%2Fname',
      '/api/v4/channels/channel%2Fid/posts',
      '/api/v4/posts/post%2Fid/thread',
      '/api/v4/teams/team%2Fid/posts/search',
    ])
    expect(urls[5]?.searchParams.get('before')).toBe('post/id')
    expect(urls[6]?.searchParams.get('direction')).toBe('down')
  })
})
