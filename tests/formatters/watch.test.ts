import { describe, expect, test } from 'vitest'
import { formatWatchEvent, formatWatchJSON } from '../../src/formatters'
import type { WatchEvent } from '../../src/types'

const event: WatchEvent = {
  type: 'posted',
  postId: 'post-1',
  channelId: 'channel-1',
  channelName: 'town-square',
  sender: 'arda\\nadmin',
  senderId: 'user-1',
  message: 'hello\nworld sk-****-7890',
  timestamp: '2026-07-14T12:34:00.000Z',
  fileIds: [],
  redactions: [{ type: 'OpenAI API Key', masked: 'sk-****-7890', position: 12 }],
}

describe('watch formatters', () => {
  test('emits one stable JSON object per line without diagnostics', () => {
    const line = formatWatchJSON(event)
    expect(line).not.toContain('\n')
    expect(JSON.parse(line)).toEqual(event)
  })

  test('renders sanitized human output on one line', () => {
    expect(formatWatchEvent(event, false)).toMatch(
      /^\[\d{2}:\d{2}\] arda\\nadmin: hello world sk-\*\*\*\*-7890$/,
    )
  })
})
