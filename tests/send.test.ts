import { afterEach, describe, expect, test, vi } from 'vitest'
import { clearUserCache } from '../src/api'
import { MattermostDeliveryConfirmedError, sendDirectMessage, sendGroupMessage } from '../src/cli'

const base = {
  url: 'https://mattermost.test',
  token: 'token',
  json: true,
  redact: true,
  dryRun: false,
}

function channel(overrides: Record<string, unknown> = {}) {
  return {
    id: 'channel-id',
    team_id: '',
    type: 'D',
    display_name: '',
    name: 'me__recipient',
    header: '',
    purpose: '',
    last_post_at: 0,
    total_msg_count: 0,
    creator_id: 'me',
    ...overrides,
  }
}

describe('message sending handlers', () => {
  afterEach(() => {
    clearUserCache()
    vi.unstubAllGlobals()
    vi.restoreAllMocks()
  })

  test('dry-runs an existing DM without creating a channel or post', async () => {
    const fetchMock = vi
      .fn()
      .mockResolvedValueOnce(Response.json({ id: 'me', username: 'sender' }))
      .mockResolvedValueOnce(Response.json({ id: 'recipient', username: 'alice' }))
      .mockResolvedValueOnce(Response.json([channel()]))
    vi.stubGlobal('fetch', fetchMock)
    const output = vi.spyOn(console, 'log').mockImplementation(() => {})

    await sendDirectMessage({ ...base, username: '@alice', dryRun: true })

    expect(fetchMock).toHaveBeenCalledTimes(3)
    expect(fetchMock).not.toHaveBeenCalledWith(
      expect.stringMatching(/\/channels\/direct|\/posts$/),
      expect.objectContaining({ method: 'POST' }),
    )
    expect(JSON.parse(String(output.mock.calls[0]?.[0]))).toEqual({
      status: 'dry_run',
      destination: {
        type: 'dm',
        label: '@alice',
        channelId: 'channel-id',
        willCreate: false,
      },
    })
  })

  test('dry-runs a new DM without creating it', async () => {
    const fetchMock = vi
      .fn()
      .mockResolvedValueOnce(Response.json({ id: 'me', username: 'sender' }))
      .mockResolvedValueOnce(Response.json({ id: 'recipient', username: 'alice' }))
      .mockResolvedValueOnce(Response.json([]))
    vi.stubGlobal('fetch', fetchMock)
    const output = vi.spyOn(console, 'log').mockImplementation(() => {})

    await sendDirectMessage({ ...base, username: 'alice', dryRun: true })

    expect(fetchMock).toHaveBeenCalledTimes(3)
    expect(JSON.parse(String(output.mock.calls[0]?.[0]))).toMatchObject({
      status: 'dry_run',
      destination: { channelId: null, willCreate: true },
    })
  })

  test('rejects a mismatched username response before channel discovery or posting', async () => {
    const fetchMock = vi
      .fn()
      .mockResolvedValueOnce(Response.json({ id: 'me', username: 'sender' }))
      .mockResolvedValueOnce(Response.json({ id: 'recipient', username: 'mallory' }))
    vi.stubGlobal('fetch', fetchMock)

    await expect(
      sendDirectMessage({ ...base, username: 'alice', message: 'do not send' }),
    ).rejects.toThrow('invalid user response')
    expect(fetchMock).toHaveBeenCalledTimes(2)
  })

  test.each([
    true,
    false,
  ])('blocks the active credential before any request when redact=%s', async (redact) => {
    const fetchMock = vi.fn()
    vi.stubGlobal('fetch', fetchMock)

    await expect(
      sendDirectMessage({
        ...base,
        redact,
        username: 'alice',
        message: `never send ${base.token} anywhere`,
      }),
    ).rejects.toThrow('Refusing to send the active Mattermost credential.')
    expect(fetchMock).not.toHaveBeenCalled()
  })

  test('creates a missing DM, sends once, and omits message content from the receipt', async () => {
    const direct = channel()
    const fetchMock = vi
      .fn()
      .mockResolvedValueOnce(Response.json({ id: 'me', username: 'sender' }))
      .mockResolvedValueOnce(Response.json({ id: 'recipient', username: 'alice' }))
      .mockResolvedValueOnce(Response.json([]))
      .mockResolvedValueOnce(Response.json(direct))
      .mockResolvedValueOnce(
        Response.json({
          id: 'post-id',
          channel_id: 'channel-id',
          user_id: 'me',
          create_at: 1_784_023_427_000,
          message: 'very private',
        }),
      )
    vi.stubGlobal('fetch', fetchMock)
    vi.spyOn(crypto, 'randomUUID').mockReturnValue('00000000-0000-4000-8000-000000000000')
    const output = vi.spyOn(console, 'log').mockImplementation(() => {})

    await sendDirectMessage({ ...base, username: 'alice', message: 'very private' })

    expect(fetchMock).toHaveBeenCalledTimes(5)
    expect(fetchMock.mock.calls[3]?.[0]).toBe('https://mattermost.test/api/v4/channels/direct')
    expect(fetchMock.mock.calls[4]?.[0]).toBe('https://mattermost.test/api/v4/posts')
    const receiptText = String(output.mock.calls[0]?.[0])
    expect(receiptText).not.toContain('very private')
    expect(JSON.parse(receiptText)).toMatchObject({
      status: 'sent',
      destination: { type: 'dm', label: '@alice', channelId: 'channel-id' },
      post: { id: 'post-id', pendingPostId: '00000000-0000-4000-8000-000000000000' },
    })
  })

  test('reports uncertain DM setup while proving the message was never attempted', async () => {
    const fetchMock = vi
      .fn()
      .mockResolvedValueOnce(Response.json({ id: 'me', username: 'sender' }))
      .mockResolvedValueOnce(Response.json({ id: 'recipient', username: 'alice' }))
      .mockResolvedValueOnce(Response.json([]))
      .mockResolvedValueOnce(Response.json({ id: 'wrong', type: 'G', name: 'bad' }))
    vi.stubGlobal('fetch', fetchMock)

    await expect(
      sendDirectMessage({ ...base, username: 'alice', message: 'must not be attempted' }),
    ).rejects.toThrow('The message was not attempted; run a dry-run before retrying.')
    expect(fetchMock).toHaveBeenCalledTimes(4)
    expect(fetchMock.mock.calls.map(([request]) => String(request))).not.toContain(
      'https://mattermost.test/api/v4/posts',
    )
  })

  test('sends once to an existing group DM after validating its type', async () => {
    const group = channel({ type: 'G', name: 'group-name', display_name: 'Test Group' })
    const fetchMock = vi
      .fn()
      .mockResolvedValueOnce(Response.json(group))
      .mockResolvedValueOnce(Response.json({ id: 'me', username: 'sender' }))
      .mockResolvedValueOnce(
        Response.json({
          id: 'post-id',
          channel_id: 'channel-id',
          user_id: 'me',
          create_at: 1_784_023_427_000,
        }),
      )
    vi.stubGlobal('fetch', fetchMock)
    const output = vi.spyOn(console, 'log').mockImplementation(() => {})

    await sendGroupMessage({ ...base, channelId: 'channel-id', message: 'hello group' })

    expect(fetchMock).toHaveBeenCalledTimes(3)
    expect(fetchMock.mock.calls[2]?.[0]).toBe('https://mattermost.test/api/v4/posts')
    expect(JSON.parse(String(output.mock.calls[0]?.[0]))).toMatchObject({
      status: 'sent',
      destination: { type: 'group', label: 'Test Group', channelId: 'channel-id' },
      post: { id: 'post-id' },
    })
  })

  test('reports confirmed delivery distinctly when receipt output fails', async () => {
    const fetchMock = vi
      .fn()
      .mockResolvedValueOnce(Response.json({ id: 'me', username: 'sender' }))
      .mockResolvedValueOnce(Response.json({ id: 'recipient', username: 'alice' }))
      .mockResolvedValueOnce(Response.json([channel()]))
      .mockResolvedValueOnce(
        Response.json({
          id: 'post-id',
          channel_id: 'channel-id',
          user_id: 'me',
          create_at: 1_784_023_427_000,
        }),
      )
    vi.stubGlobal('fetch', fetchMock)
    vi.spyOn(console, 'log').mockImplementation(() => {
      throw new Error('broken pipe')
    })

    await expect(
      sendDirectMessage({ ...base, username: 'alice', message: 'one message' }),
    ).rejects.toBeInstanceOf(MattermostDeliveryConfirmedError)
    expect(fetchMock).toHaveBeenCalledTimes(4)
  })

  test('rejects a non-group channel before identity lookup or posting', async () => {
    const fetchMock = vi.fn().mockResolvedValueOnce(Response.json(channel()))
    vi.stubGlobal('fetch', fetchMock)

    await expect(
      sendGroupMessage({ ...base, channelId: 'channel-id', message: 'do not send' }),
    ).rejects.toThrow('is not a group DM')
    expect(fetchMock).toHaveBeenCalledTimes(1)
  })

  test('dry-runs an existing group without reading identity or posting', async () => {
    const fetchMock = vi
      .fn()
      .mockResolvedValueOnce(
        Response.json(channel({ type: 'G', name: 'group-name', display_name: '' })),
      )
    vi.stubGlobal('fetch', fetchMock)
    const output = vi.spyOn(console, 'log').mockImplementation(() => {})

    await sendGroupMessage({ ...base, channelId: 'channel-id', dryRun: true })

    expect(fetchMock).toHaveBeenCalledTimes(1)
    expect(JSON.parse(String(output.mock.calls[0]?.[0]))).toMatchObject({
      status: 'dry_run',
      destination: { type: 'group', label: 'group-name', willCreate: false },
    })
  })

  test('protects active credentials and terminal controls in group receipts with redaction off', async () => {
    const fetchMock = vi.fn().mockResolvedValueOnce(
      Response.json(
        channel({
          type: 'G',
          name: 'group-name',
          display_name: `ops ${base.token}\u001b[2J`,
        }),
      ),
    )
    vi.stubGlobal('fetch', fetchMock)
    const output = vi.spyOn(console, 'log').mockImplementation(() => {})

    await sendGroupMessage({ ...base, redact: false, channelId: 'channel-id', dryRun: true })

    const receipt = String(output.mock.calls[0]?.[0])
    expect(receipt).not.toContain(base.token)
    expect(receipt).not.toContain('\u001b')
    expect(receipt).toContain('[REDACTED:mattermost_credential]')
  })
})
