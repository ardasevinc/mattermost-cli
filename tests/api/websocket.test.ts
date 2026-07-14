import { describe, expect, test } from 'vitest'
import { getSocketErrorMessage } from '../../src/api/websocket'

describe('getSocketErrorMessage', () => {
  test('reads Mattermost structured WebSocket errors', () => {
    expect(
      getSocketErrorMessage({
        id: 'api.context.session_expired.app_error',
        message: 'Not authorized',
      }),
    ).toBe('Not authorized')
  })

  test('neutralizes terminal controls in remote errors', () => {
    expect(getSocketErrorMessage({ message: 'failed\u001b[2Jspoofed' })).toBe(
      'failed\\u001b[2Jspoofed',
    )
  })

  test('uses a stable fallback for malformed errors', () => {
    expect(getSocketErrorMessage({ id: 'unknown' })).toBe('WebSocket request failed.')
  })
})
