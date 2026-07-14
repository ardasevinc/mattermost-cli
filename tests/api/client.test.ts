import { afterEach, describe, expect, test, vi } from 'vitest'
import { MattermostClient, rateLimitDelay } from '../../src/api/client'

describe('MattermostClient', () => {
  afterEach(() => {
    vi.unstubAllGlobals()
  })

  test('neutralizes terminal controls in remote HTTP reason phrases', async () => {
    vi.stubGlobal(
      'fetch',
      vi.fn().mockResolvedValue({
        headers: new Headers(),
        ok: false,
        status: 500,
        statusText: 'Server Error\u001b[2Jspoofed',
        text: async () => 'remote body',
      }),
    )

    const client = new MattermostClient('https://mattermost.example.com', 'fake-token')

    await expect(client.get('/users/me')).rejects.toThrow(
      'API request failed: 500 Server Error\\u001b[2Jspoofed',
    )
  })

  test('honors Mattermost relative reset seconds and bounds remote delays', () => {
    expect(rateLimitDelay({ headers: new Headers({ 'X-RateLimit-Reset': '7' }) }, 0)).toBe(7000)
    expect(rateLimitDelay({ headers: new Headers({ 'X-RateLimit-Reset': '3600' }) }, 0)).toBe(
      5 * 60 * 1000,
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
    vi.useRealTimers()
  })
})
