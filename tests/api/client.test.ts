import { afterEach, describe, expect, test, vi } from 'vitest'
import {
  getClient,
  initClient,
  MattermostClient,
  REQUEST_TIMEOUT_MS,
  rateLimitDelay,
} from '../../src/api/client'
import { preprocess } from '../../src/preprocessing'

describe('MattermostClient', () => {
  afterEach(() => {
    vi.useRealTimers()
    vi.unstubAllGlobals()
    vi.restoreAllMocks()
  })

  test('singleton replacement releases its prior owned credential', () => {
    initClient('https://mattermost.example.com', 'old-client-token')
    initClient('https://mattermost.example.com', 'new-client-token')
    expect(preprocess('old-client-token new-client-token', { redact: false }).text).toBe(
      'old-client-token [REDACTED:mattermost_credential]',
    )
  })

  test('failed singleton replacement preserves the prior client and credential only', () => {
    const previous = initClient('https://mattermost.example.com', 'old-client-token')
    expect(() => initClient('not a URL', 'attempted-token')).toThrow('Invalid Mattermost URL')
    expect(getClient()).toBe(previous)
    expect(preprocess('old-client-token attempted-token', { redact: false }).text).toBe(
      '[REDACTED:mattermost_credential] attempted-token',
    )
  })

  test('does not expose remote reason phrases or the configured token', async () => {
    vi.stubGlobal(
      'fetch',
      vi.fn().mockResolvedValue({
        headers: new Headers(),
        ok: false,
        status: 500,
        statusText: 'Server Error fake-token\u001b[2Jspoofed',
        text: async () => 'remote body',
      }),
    )

    const client = new MattermostClient('https://mattermost.example.com', 'fake-token')

    await expect(client.get('/users/me')).rejects.toThrow('API request failed: 500.')
  })

  test('honors Mattermost relative reset seconds and bounds remote delays', () => {
    expect(rateLimitDelay({ headers: new Headers({ 'X-RateLimit-Reset': '7' }) }, 0)).toBe(7000)
    expect(rateLimitDelay({ headers: new Headers({ 'X-RateLimit-Reset': '3600' }) }, 0)).toBe(
      30 * 1000,
    )
    expect(rateLimitDelay({ headers: new Headers({ 'X-RateLimit-Reset': 'invalid' }) }, 2)).toBe(
      4000,
    )
  })

  test('prefers Retry-After and accepts its HTTP-date form', () => {
    vi.useFakeTimers()
    vi.setSystemTime(new Date('2026-07-14T00:00:00.000Z'))
    expect(
      rateLimitDelay(
        {
          headers: new Headers({
            'Retry-After': 'Tue, 14 Jul 2026 00:00:03 GMT',
            'X-RateLimit-Reset': String(Date.now() / 1000 + 30),
          }),
        },
        0,
      ),
    ).toBe(3000)
    vi.useRealTimers()
  })

  test('waits for X-RateLimit-Reset before retrying a 429', async () => {
    vi.useFakeTimers()
    const fetchMock = vi
      .fn()
      .mockResolvedValueOnce(
        new Response('', { status: 429, headers: { 'X-RateLimit-Reset': '2' } }),
      )
      .mockResolvedValueOnce(Response.json({ id: 'me' }))
    vi.stubGlobal('fetch', fetchMock)
    const request = new MattermostClient('https://mattermost.example.com', 'fake-token').get(
      '/users/me',
    )

    await vi.advanceTimersByTimeAsync(1999)
    expect(fetchMock).toHaveBeenCalledTimes(1)
    await vi.advanceTimersByTimeAsync(1)
    await expect(request).resolves.toEqual({ id: 'me' })
    expect(fetchMock).toHaveBeenCalledTimes(2)
  })

  test('aborts each stalled attempt and bounds read retries', async () => {
    vi.useFakeTimers()
    const fetchMock = vi.fn((_url: string, init?: RequestInit) => {
      return new Promise<Response>((_resolve, reject) => {
        init?.signal?.addEventListener('abort', () =>
          reject(new DOMException('aborted', 'AbortError')),
        )
      })
    })
    vi.stubGlobal('fetch', fetchMock)
    const request = new MattermostClient('https://mattermost.example.com', 'fake-token').get(
      '/users/me',
    )
    const rejection = expect(request).rejects.toThrow(`timed out after ${REQUEST_TIMEOUT_MS}ms`)

    await vi.advanceTimersByTimeAsync(REQUEST_TIMEOUT_MS + 1000)
    await vi.advanceTimersByTimeAsync(REQUEST_TIMEOUT_MS + 2000)
    await vi.advanceTimersByTimeAsync(REQUEST_TIMEOUT_MS)

    await rejection
    expect(fetchMock).toHaveBeenCalledTimes(3)
  })

  test('retries read-only gateway failures but not ordinary posts', async () => {
    vi.useFakeTimers()
    const fetchMock = vi
      .fn()
      .mockResolvedValueOnce(new Response('', { status: 503 }))
      .mockResolvedValueOnce(Response.json({ id: 'me' }))
      .mockResolvedValueOnce(new Response('', { status: 503 }))
    vi.stubGlobal('fetch', fetchMock)
    const client = new MattermostClient('https://mattermost.example.com', 'fake-token')
    const read = client.get('/users/me')
    await vi.advanceTimersByTimeAsync(1000)

    await expect(read).resolves.toEqual({ id: 'me' })
    await expect(client.post('/posts', { message: 'hello' })).rejects.toMatchObject({ status: 503 })
    expect(fetchMock).toHaveBeenCalledTimes(3)
  })

  test('keeps the timeout active while consuming the response body', async () => {
    vi.useFakeTimers()
    const fetchMock = vi
      .fn()
      .mockImplementationOnce((_url: string, init?: RequestInit) =>
        Promise.resolve({
          ok: true,
          status: 200,
          json: () =>
            new Promise((_resolve, reject) => {
              init?.signal?.addEventListener('abort', () => reject(new DOMException('secret body')))
            }),
        }),
      )
      .mockResolvedValueOnce(Response.json({ id: 'me' }))
    vi.stubGlobal('fetch', fetchMock)
    const request = new MattermostClient('https://mattermost.example.com', 'fake-token').get(
      '/users/me',
    )

    await vi.advanceTimersByTimeAsync(REQUEST_TIMEOUT_MS + 1000)
    await expect(request).resolves.toEqual({ id: 'me' })
    expect(fetchMock).toHaveBeenCalledTimes(2)
  })

  test.each([
    ['POST', (client: MattermostClient) => client.post('/posts', { message: 'hello' })],
    ['PUT', (client: MattermostClient) => client.put('/posts/id', { message: 'hello' })],
    ['DELETE', (client: MattermostClient) => client.delete('/posts/id')],
  ] as const)('does not replay a mutating %s request after a 429', async (_method, request) => {
    const fetchMock = vi.fn().mockResolvedValue(
      new Response('', {
        status: 429,
        headers: { 'Retry-After': '0' },
      }),
    )
    vi.stubGlobal('fetch', fetchMock)

    await expect(
      request(new MattermostClient('https://mattermost.example.com', 'fake-token')),
    ).rejects.toMatchObject({ status: 429 })
    expect(fetchMock).toHaveBeenCalledTimes(1)
  })

  test('reports malformed success JSON without reflecting parser or body details', async () => {
    vi.stubGlobal(
      'fetch',
      vi.fn().mockResolvedValue({
        ok: true,
        status: 200,
        json: () => Promise.reject(new SyntaxError('Unexpected token: secret-body-fragment')),
      }),
    )

    await expect(
      new MattermostClient('https://mattermost.example.com', 'fake-token').get('/users/me'),
    ).rejects.toThrow('Mattermost returned an invalid JSON response.')
  })

  test('retries a terminated response body for a read-only request', async () => {
    vi.useFakeTimers()
    const fetchMock = vi
      .fn()
      .mockResolvedValueOnce({
        ok: true,
        status: 200,
        json: () => Promise.reject(new TypeError('terminated secret-body-fragment')),
      })
      .mockResolvedValueOnce(Response.json({ id: 'me' }))
    vi.stubGlobal('fetch', fetchMock)
    const request = new MattermostClient('https://mattermost.example.com', 'fake-token').get(
      '/users/me',
    )

    await vi.advanceTimersByTimeAsync(1000)
    await expect(request).resolves.toEqual({ id: 'me' })
    expect(fetchMock).toHaveBeenCalledTimes(2)
  })

  test('bounds repeated response-body terminations and hides their cause', async () => {
    vi.useFakeTimers()
    const fetchMock = vi.fn().mockResolvedValue({
      ok: true,
      status: 200,
      json: () => Promise.reject(new TypeError('terminated secret-body-fragment')),
    })
    vi.stubGlobal('fetch', fetchMock)
    const request = new MattermostClient('https://mattermost.example.com', 'fake-token').get(
      '/users/me',
    )
    const rejection = expect(request).rejects.toThrow(
      'Unable to connect to Mattermost due to a network error.',
    )

    await vi.advanceTimersByTimeAsync(3000)
    await rejection
    expect(fetchMock).toHaveBeenCalledTimes(3)
  })

  test('reports bounded rate-limit waits on stderr without consuming response bodies', async () => {
    vi.useFakeTimers()
    const text = vi.fn()
    const fetchMock = vi
      .fn()
      .mockResolvedValueOnce({
        headers: new Headers({ 'Retry-After': '3600' }),
        ok: false,
        status: 429,
        statusText: 'Too Many Requests',
        text,
      })
      .mockResolvedValueOnce(Response.json({ id: 'me' }))
    vi.stubGlobal('fetch', fetchMock)
    const stderr = vi.spyOn(console, 'error').mockImplementation(() => {})
    const request = new MattermostClient('https://mattermost.example.com', 'fake-token').get(
      '/users/me',
    )

    await vi.advanceTimersByTimeAsync(30_000)
    await expect(request).resolves.toEqual({ id: 'me' })
    expect(stderr).toHaveBeenCalledWith(
      'Mattermost request was rate limited; retrying in 30000ms (attempt 1).',
    )
    expect(text).not.toHaveBeenCalled()
  })
})
