import { afterEach, describe, expect, test, vi } from 'vitest'
import { MattermostClient } from '../../src/api/client'

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
})
