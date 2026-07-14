import { afterEach, describe, expect, test, vi } from 'vitest'
import {
  createDirectChannel,
  createPost,
  initClient,
  MattermostMutationOutcomeUnknownError,
} from '../../src/api'

describe('message write API', () => {
  afterEach(() => {
    vi.unstubAllGlobals()
    vi.restoreAllMocks()
  })

  test('creates a direct channel for the exact two participants without retry opt-in', async () => {
    const fetchMock = vi.fn().mockResolvedValue(
      Response.json({
        id: 'dm-channel',
        type: 'D',
        name: 'me__recipient',
        display_name: '',
        team_id: '',
      }),
    )
    vi.stubGlobal('fetch', fetchMock)
    initClient('https://mattermost.test', 'token')

    await expect(createDirectChannel('me', 'recipient')).resolves.toMatchObject({
      id: 'dm-channel',
      type: 'D',
    })
    expect(fetchMock).toHaveBeenCalledWith(
      'https://mattermost.test/api/v4/channels/direct',
      expect.objectContaining({ method: 'POST', body: JSON.stringify(['me', 'recipient']) }),
    )
  })

  test.each([
    [{ id: '', type: 'D', name: 'me__recipient' }],
    [{ id: 'channel', type: 'G', name: 'me__recipient' }],
    [{ id: 'channel', type: 'D', name: 'me__someone-else' }],
    [{ id: 'channel', type: 'D', name: 'me__recipient', display_name: '', team_id: 'team' }],
    [null],
  ])('rejects a malformed or mismatched direct-channel response', async (response) => {
    vi.stubGlobal('fetch', vi.fn().mockResolvedValue(Response.json(response)))
    initClient('https://mattermost.test', 'token')

    await expect(createDirectChannel('me', 'recipient')).rejects.toBeInstanceOf(
      MattermostMutationOutcomeUnknownError,
    )
  })

  test('creates a post with a stable pending ID and returns only a narrow receipt', async () => {
    const fetchMock = vi.fn().mockResolvedValue(
      Response.json({
        id: 'post-id',
        channel_id: 'channel-id',
        user_id: 'sender-id',
        create_at: 1_784_023_427_000,
        message: 'server-visible message',
        hostile: 'ignored',
      }),
    )
    vi.stubGlobal('fetch', fetchMock)
    initClient('https://mattermost.test', 'token')

    await expect(createPost('channel-id', 'hello', 'pending-id')).resolves.toEqual({
      id: 'post-id',
      channelId: 'channel-id',
      userId: 'sender-id',
      createAt: 1_784_023_427_000,
      pendingPostId: 'pending-id',
    })
    expect(fetchMock).toHaveBeenCalledWith(
      'https://mattermost.test/api/v4/posts',
      expect.objectContaining({
        method: 'POST',
        body: JSON.stringify({
          channel_id: 'channel-id',
          message: 'hello',
          pending_post_id: 'pending-id',
        }),
      }),
    )
  })

  test.each([
    [{ id: '', channel_id: 'channel-id', user_id: 'sender', create_at: 1 }],
    [{ id: 'post', channel_id: 'wrong', user_id: 'sender', create_at: 1 }],
    [{ id: 'post', channel_id: 'channel-id', user_id: '', create_at: 1 }],
    [{ id: 'post', channel_id: 'channel-id', user_id: 'sender', create_at: Number.NaN }],
    [{ id: 'post', channel_id: 'channel-id', user_id: 'sender', create_at: 1e100 }],
    [null],
  ])('rejects a malformed or mismatched create-post response', async (response) => {
    vi.stubGlobal('fetch', vi.fn().mockResolvedValue(Response.json(response)))
    initClient('https://mattermost.test', 'token')

    await expect(createPost('channel-id', 'hello', 'pending-id')).rejects.toBeInstanceOf(
      MattermostMutationOutcomeUnknownError,
    )
  })

  test.each([
    '',
    '   ',
    '\n\t',
  ])('rejects an empty message before network access', async (message) => {
    const fetchMock = vi.fn()
    vi.stubGlobal('fetch', fetchMock)
    initClient('https://mattermost.test', 'token')

    await expect(createPost('channel-id', message)).rejects.toThrow('Message cannot be empty.')
    expect(fetchMock).not.toHaveBeenCalled()
  })
})
