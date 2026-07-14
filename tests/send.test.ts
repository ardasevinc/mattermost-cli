import { afterEach, describe, expect, test, vi } from 'vitest'
import { clearUserCache, MattermostMutationOutcomeUnknownError } from '../src/api'
import { MattermostDeliveryConfirmedError, sendDirectMessage, sendGroupMessage } from '../src/cli'
import { LONG_MARKDOWN, SHORT_MARKDOWN } from './fixtures/markdown'

const base = {
  url: 'https://mattermost.test',
  token: 't'.repeat(26),
  json: true,
  redact: true,
  dryRun: false,
}
const postId = 'p'.repeat(26)

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

function captureStdout(): () => string {
  let output = ''
  vi.spyOn(process.stdout, 'write').mockImplementation((chunk, ...args) => {
    output += String(chunk)
    const callback = args.find((arg) => typeof arg === 'function')
    if (typeof callback === 'function') callback()
    return true
  })
  return () => output
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

  test('rejects a blank sender identity before resolving a DM recipient or posting', async () => {
    const fetchMock = vi
      .fn()
      .mockResolvedValueOnce(Response.json({ id: '   ', username: 'sender' }))
    vi.stubGlobal('fetch', fetchMock)

    await expect(
      sendDirectMessage({ ...base, username: 'alice', message: 'do not send' }),
    ).rejects.toThrow('invalid identity response')
    expect(fetchMock).toHaveBeenCalledTimes(1)
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

  test('creates a missing DM and sends short Markdown verbatim without reflecting it', async () => {
    const direct = channel()
    const fetchMock = vi
      .fn()
      .mockResolvedValueOnce(Response.json({ id: 'me', username: 'sender' }))
      .mockResolvedValueOnce(Response.json({ id: 'recipient', username: 'alice' }))
      .mockResolvedValueOnce(Response.json([]))
      .mockResolvedValueOnce(Response.json(direct))
      .mockResolvedValueOnce(
        Response.json({
          id: postId,
          channel_id: 'channel-id',
          user_id: 'me',
          create_at: 1_784_023_427_000,
          message: SHORT_MARKDOWN,
        }),
      )
    vi.stubGlobal('fetch', fetchMock)
    vi.spyOn(crypto, 'randomUUID').mockReturnValue('00000000-0000-4000-8000-000000000000')
    const output = captureStdout()

    await sendDirectMessage({ ...base, username: 'alice', message: SHORT_MARKDOWN })

    expect(fetchMock).toHaveBeenCalledTimes(5)
    expect(fetchMock.mock.calls[3]?.[0]).toBe('https://mattermost.test/api/v4/channels/direct')
    expect(fetchMock.mock.calls[4]?.[0]).toBe('https://mattermost.test/api/v4/posts')
    expect(JSON.parse(String(fetchMock.mock.calls[4]?.[1]?.body))).toMatchObject({
      channel_id: 'channel-id',
      message: SHORT_MARKDOWN,
    })
    const receiptText = output()
    expect(receiptText).not.toContain('Release ready')
    expect(JSON.parse(receiptText)).toMatchObject({
      status: 'sent',
      destination: { type: 'dm', label: '@alice', channelId: 'channel-id' },
      post: { id: postId, pendingPostId: '00000000-0000-4000-8000-000000000000' },
    })
  })

  test('reports uncertain DM setup while proving the message was never attempted', async () => {
    const fetchMock = vi
      .fn()
      .mockResolvedValueOnce(Response.json({ id: 'me', username: 'sender' }))
      .mockResolvedValueOnce(Response.json({ id: 'recipient', username: 'alice' }))
      .mockResolvedValueOnce(Response.json([]))
      .mockResolvedValueOnce(
        Response.json({
          id: '   ',
          type: 'D',
          name: 'me__recipient',
          display_name: '',
          team_id: '',
        }),
      )
    vi.stubGlobal('fetch', fetchMock)

    await expect(
      sendDirectMessage({ ...base, username: 'alice', message: 'must not be attempted' }),
    ).rejects.toThrow('The message was not attempted; run a dry-run before retrying.')
    expect(fetchMock).toHaveBeenCalledTimes(4)
    expect(fetchMock.mock.calls.map(([request]) => String(request))).not.toContain(
      'https://mattermost.test/api/v4/posts',
    )
  })

  test('sends long Markdown verbatim to an existing group without reflecting it', async () => {
    const group = channel({ type: 'G', name: 'group-name', display_name: 'Test Group' })
    const fetchMock = vi
      .fn()
      .mockResolvedValueOnce(Response.json(group))
      .mockResolvedValueOnce(Response.json({ id: 'me', username: 'sender' }))
      .mockResolvedValueOnce(
        Response.json({
          id: postId,
          channel_id: 'channel-id',
          user_id: 'me',
          create_at: 1_784_023_427_000,
        }),
      )
    vi.stubGlobal('fetch', fetchMock)
    const output = captureStdout()

    await sendGroupMessage({ ...base, channelId: 'channel-id', message: LONG_MARKDOWN })

    expect(fetchMock).toHaveBeenCalledTimes(3)
    expect(fetchMock.mock.calls[2]?.[0]).toBe('https://mattermost.test/api/v4/posts')
    expect(JSON.parse(String(fetchMock.mock.calls[2]?.[1]?.body))).toMatchObject({
      channel_id: 'channel-id',
      message: LONG_MARKDOWN,
    })
    const receipt = output()
    expect(receipt).not.toContain('Extended deployment report')
    expect(JSON.parse(receipt)).toMatchObject({
      status: 'sent',
      destination: { type: 'group', label: 'Test Group', channelId: 'channel-id' },
      post: { id: postId },
    })
  })

  test.each([
    {},
    { id: '   ', username: 'sender' },
  ])('rejects a malformed sender identity before posting to a group', async (identity) => {
    const group = channel({ type: 'G', name: 'group-name', display_name: 'Test Group' })
    const fetchMock = vi
      .fn()
      .mockResolvedValueOnce(Response.json(group))
      .mockResolvedValueOnce(Response.json(identity))
    vi.stubGlobal('fetch', fetchMock)

    await expect(
      sendGroupMessage({ ...base, channelId: 'channel-id', message: 'must not send' }),
    ).rejects.toThrow('invalid identity response')
    expect(fetchMock).toHaveBeenCalledTimes(2)
    expect(fetchMock.mock.calls.map(([request]) => String(request))).not.toContain(
      'https://mattermost.test/api/v4/posts',
    )
  })

  test('reports confirmed delivery distinctly when receipt output fails', async () => {
    const fetchMock = vi
      .fn()
      .mockResolvedValueOnce(Response.json({ id: 'me', username: 'sender' }))
      .mockResolvedValueOnce(Response.json({ id: 'recipient', username: 'alice' }))
      .mockResolvedValueOnce(Response.json([channel()]))
      .mockResolvedValueOnce(
        Response.json({
          id: postId,
          channel_id: 'channel-id',
          user_id: 'me',
          create_at: 1_784_023_427_000,
        }),
      )
    vi.stubGlobal('fetch', fetchMock)
    vi.spyOn(process.stdout, 'write').mockImplementation((_chunk, ...args) => {
      const callback = args.find((arg) => typeof arg === 'function')
      if (typeof callback === 'function') callback(new Error('broken pipe'))
      return false
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

  test.each([
    true,
    false,
  ])('protects the active credential in both post ID and permalink when redact=%s', async (redact) => {
    const group = channel({ type: 'G', name: 'group-name', display_name: 'Test Group' })
    const fetchMock = vi
      .fn()
      .mockResolvedValueOnce(Response.json(group))
      .mockResolvedValueOnce(Response.json({ id: 'me', username: 'sender' }))
      .mockResolvedValueOnce(
        Response.json({
          id: base.token,
          channel_id: 'channel-id',
          user_id: 'me',
          create_at: 1_784_023_427_000,
        }),
      )
    vi.stubGlobal('fetch', fetchMock)
    const output = captureStdout()

    await sendGroupMessage({
      ...base,
      redact,
      channelId: 'channel-id',
      message: 'safe message',
    })

    const receipt = output()
    expect(receipt).not.toContain(base.token)
    expect(receipt).toContain('[REDACTED:mattermost_credential]')
    expect(JSON.parse(receipt).post.permalink).not.toContain(base.token)
  })

  test('never reflects outbound message content from a hostile post ID receipt', async () => {
    const message = 'private launch detail'
    const group = channel({ type: 'G', name: 'group-name', display_name: 'Test Group' })
    const fetchMock = vi
      .fn()
      .mockResolvedValueOnce(Response.json(group))
      .mockResolvedValueOnce(Response.json({ id: 'me', username: 'sender' }))
      .mockResolvedValueOnce(
        Response.json({
          id: message,
          channel_id: 'channel-id',
          user_id: 'me',
          create_at: 1_784_023_427_000,
        }),
      )
    vi.stubGlobal('fetch', fetchMock)
    const output = captureStdout()

    await expect(
      sendGroupMessage({ ...base, channelId: 'channel-id', message }),
    ).rejects.toBeInstanceOf(MattermostMutationOutcomeUnknownError)
    expect(output()).not.toContain(message)
    expect(fetchMock).toHaveBeenCalledTimes(3)
  })
})
