import { afterEach, beforeEach, describe, expect, test, vi } from 'vitest'
import { connectWebSocket, getSocketErrorMessage } from '../../src/api/websocket'
import { preprocess } from '../../src/preprocessing'
import type { Post } from '../../src/types'

class FakeWebSocket {
  static instances: FakeWebSocket[] = []
  readonly url: string
  readyState = 0
  sent: string[] = []
  closeCalls: Array<[number | undefined, string | undefined]> = []
  onopen: (() => void) | null = null
  onmessage: ((event: MessageEvent) => void) | null = null
  onerror: (() => void) | null = null
  onclose: (() => void) | null = null

  constructor(url: string | URL) {
    this.url = String(url)
    FakeWebSocket.instances.push(this)
  }

  open(): void {
    this.readyState = 1
    this.onopen?.()
  }

  raw(data: unknown): void {
    this.onmessage?.({ data } as MessageEvent)
  }

  message(payload: unknown): void {
    this.raw(JSON.stringify(payload))
  }

  send(data: string): void {
    this.sent.push(data)
  }

  close(code?: number, reason?: string): void {
    this.closeCalls.push([code, reason])
    this.readyState = 3
    this.onclose?.()
  }

  drop(): void {
    this.readyState = 3
    this.onclose?.()
  }

  error(): void {
    this.onerror?.()
  }
}

const post: Post = {
  id: 'post-1',
  create_at: 1,
  update_at: 1,
  delete_at: 0,
  edit_at: 0,
  user_id: 'user-1',
  channel_id: 'channel-1',
  message: 'hello',
  type: '',
  props: {},
  hashtags: '',
  root_id: '',
  reply_count: 0,
  file_ids: [],
  pending_post_id: '',
}

function authenticate(socket: FakeWebSocket, connectionId = 'connection-1', sequence = 0): void {
  socket.open()
  const auth = JSON.parse(socket.sent[0] as string) as { seq: number }
  socket.message({ event: 'hello', seq: sequence, data: { connection_id: connectionId } })
  socket.message({ status: 'OK', seq_reply: auth.seq })
}

function posted(socket: FakeWebSocket, sequence: number, value: Post = post): void {
  socket.message({
    event: 'posted',
    seq: sequence,
    data: { post: JSON.stringify(value), channel_name: 'town', sender_name: 'arda' },
    broadcast: { channel_id: 'misleading-broadcast-id' },
  })
}

beforeEach(() => {
  vi.useFakeTimers()
  FakeWebSocket.instances = []
})

afterEach(() => vi.useRealTimers())

describe('getSocketErrorMessage', () => {
  test('reads and sanitizes Mattermost WebSocket errors', () => {
    expect(getSocketErrorMessage({ message: 'Not authorized' })).toBe('Not authorized')
    expect(getSocketErrorMessage({ message: 'failed\u001b[2Jspoofed' })).toBe(
      'failed\\u001b[2Jspoofed',
    )
    expect(getSocketErrorMessage({ id: 'unknown' })).toBe('WebSocket request failed.')
  })
})

describe('connectWebSocket', () => {
  test('releases credentials when URL validation fails synchronously', () => {
    expect(() => connectWebSocket('not a URL', 'invalid-url-token', vi.fn(), vi.fn())).toThrow(
      'Invalid Mattermost URL',
    )
    expect(preprocess('invalid-url-token', { redact: false }).text).toBe('invalid-url-token')
  })

  test('releases credentials when the initial socket constructor throws', () => {
    class ThrowingWebSocket {
      constructor() {
        throw new Error('socket construction failed')
      }
    }
    expect(() =>
      connectWebSocket('https://mm.example.com', 'constructor-token', vi.fn(), vi.fn(), {
        WebSocket: ThrowingWebSocket as unknown as typeof WebSocket,
      }),
    ).toThrow('socket construction failed')
    expect(preprocess('constructor-token', { redact: false }).text).toBe('constructor-token')
  })

  test('keeps concurrent socket credentials live and releases each on close', () => {
    const first = connectWebSocket('https://mm.example.com', 'socket-one', vi.fn(), vi.fn(), {
      WebSocket: FakeWebSocket as unknown as typeof WebSocket,
    })
    const second = connectWebSocket('https://mm.example.com', 'socket-two', vi.fn(), vi.fn(), {
      WebSocket: FakeWebSocket as unknown as typeof WebSocket,
    })
    expect(preprocess('socket-one socket-two', { redact: false }).text).toBe(
      '[REDACTED:mattermost_credential] [REDACTED:mattermost_credential]',
    )
    first.close()
    expect(preprocess('socket-one socket-two', { redact: false }).text).toBe(
      'socket-one [REDACTED:mattermost_credential]',
    )
    second.close()
    expect(preprocess('socket-one socket-two', { redact: false }).text).toBe(
      'socket-one socket-two',
    )
  })

  test('resumes from the next expected server sequence', () => {
    const connection = connectWebSocket('https://mm.example.com/base', 'secret', vi.fn(), vi.fn(), {
      WebSocket: FakeWebSocket as unknown as typeof WebSocket,
      random: () => 0,
    })
    const first = FakeWebSocket.instances[0] as FakeWebSocket
    authenticate(first)
    first.message({ event: 'typing', seq: 1, data: {} })
    first.drop()
    vi.advanceTimersByTime(800)

    const resumed = FakeWebSocket.instances[1] as FakeWebSocket
    expect(resumed.url).toContain('connection_id=connection-1')
    expect(resumed.url).toContain('sequence_number=2')
    connection.close()
  })

  test('rejects a sequence mismatch without emitting the event', () => {
    const onPost = vi.fn()
    const gap = vi.fn()
    const connection = connectWebSocket('https://mm.example.com', 'secret', onPost, vi.fn(), {
      WebSocket: FakeWebSocket as unknown as typeof WebSocket,
      diagnostics: { gap },
    })
    const socket = FakeWebSocket.instances[0] as FakeWebSocket
    authenticate(socket)
    posted(socket, 2)

    expect(onPost).not.toHaveBeenCalled()
    expect(gap).toHaveBeenCalledWith({ expected: 1, received: 2, reason: 'sequence_mismatch' })
    expect(socket.closeCalls[0]).toEqual([4000, 'sequence mismatch'])
    connection.close()
  })

  test('diagnoses a changed connection id and resets the sequence', () => {
    const gap = vi.fn()
    const onPost = vi.fn()
    const connection = connectWebSocket('https://mm.example.com', 'secret', onPost, vi.fn(), {
      WebSocket: FakeWebSocket as unknown as typeof WebSocket,
      random: () => 0,
      diagnostics: { gap },
    })
    const first = FakeWebSocket.instances[0] as FakeWebSocket
    authenticate(first)
    first.drop()
    vi.advanceTimersByTime(800)
    const second = FakeWebSocket.instances[1] as FakeWebSocket
    authenticate(second, 'connection-2', 0)
    posted(second, 1)

    expect(gap).toHaveBeenCalledWith({
      expected: 1,
      received: 0,
      reason: 'connection_changed',
    })
    expect(onPost).toHaveBeenCalledTimes(1)
    connection.close()
  })

  test('advances sequence for unrelated events and filters by post channel id', () => {
    const onPost = vi.fn()
    const connection = connectWebSocket('https://mm.example.com', 'secret', onPost, vi.fn(), {
      channelId: 'channel-1',
      WebSocket: FakeWebSocket as unknown as typeof WebSocket,
    })
    const socket = FakeWebSocket.instances[0] as FakeWebSocket
    authenticate(socket)
    socket.message({ event: 'typing', seq: 1, data: {} })
    posted(socket, 2, { ...post, channel_id: 'other-channel' })
    posted(socket, 3)

    expect(onPost).toHaveBeenCalledTimes(1)
    expect(onPost).toHaveBeenCalledWith(post, 'town', 'arda')
    connection.close()
  })

  test('uses bounded exponential backoff with plus/minus twenty percent jitter', () => {
    const reconnect = vi.fn()
    const connection = connectWebSocket('https://mm.example.com', 'secret', vi.fn(), vi.fn(), {
      WebSocket: FakeWebSocket as unknown as typeof WebSocket,
      random: () => 0,
      backoffBaseMs: 100,
      backoffMaxMs: 300,
      diagnostics: { reconnect },
    })
    ;(FakeWebSocket.instances[0] as FakeWebSocket).drop()
    vi.advanceTimersByTime(80)
    ;(FakeWebSocket.instances[1] as FakeWebSocket).drop()
    vi.advanceTimersByTime(160)
    ;(FakeWebSocket.instances[2] as FakeWebSocket).drop()
    vi.advanceTimersByTime(240)
    ;(FakeWebSocket.instances[3] as FakeWebSocket).drop()
    expect(reconnect.mock.calls.map((call) => call[1])).toEqual([80, 160, 240, 240])
    connection.close()

    const upper = vi.fn()
    const second = connectWebSocket('https://mm.example.com', 'secret', vi.fn(), vi.fn(), {
      WebSocket: FakeWebSocket as unknown as typeof WebSocket,
      random: () => 1,
      diagnostics: { reconnect: upper },
    })
    ;(FakeWebSocket.instances.at(-1) as FakeWebSocket).drop()
    expect(upper).toHaveBeenCalledWith(1, 1200)
    second.close()
  })

  test('turns a synchronous reconnect constructor failure into bounded fatal cleanup', async () => {
    class ReconnectThrowingWebSocket extends FakeWebSocket {
      constructor(url: string | URL) {
        super(url)
        if (FakeWebSocket.instances.length > 1) throw new Error('reconnect construction failed')
      }
    }
    const credential = 'reconnect-constructor-token'
    const onError = vi.fn()
    const connection = connectWebSocket('https://mm.example.com', credential, vi.fn(), onError, {
      WebSocket: ReconnectThrowingWebSocket as unknown as typeof WebSocket,
      random: () => 0,
    })
    ;(FakeWebSocket.instances[0] as FakeWebSocket).drop()
    expect(() => vi.advanceTimersByTime(800)).not.toThrow()
    await connection.done
    expect(onError).toHaveBeenCalledTimes(1)
    expect(onError).toHaveBeenCalledWith(new Error('WebSocket connection failed.'))
    expect(preprocess(credential, { redact: false }).text).toBe(credential)
    expect(vi.getTimerCount()).toBe(0)
  })

  test('error plus close schedules one retry and stale socket callbacks are ignored', () => {
    const onPost = vi.fn()
    const reconnect = vi.fn()
    const connection = connectWebSocket('https://mm.example.com', 'secret', onPost, vi.fn(), {
      WebSocket: FakeWebSocket as unknown as typeof WebSocket,
      random: () => 0,
      diagnostics: { reconnect },
    })
    const first = FakeWebSocket.instances[0] as FakeWebSocket
    authenticate(first)
    first.error()
    first.drop()
    expect(reconnect).toHaveBeenCalledTimes(1)
    vi.advanceTimersByTime(800)
    authenticate(FakeWebSocket.instances[1] as FakeWebSocket, 'connection-2')
    posted(first, 1)
    expect(onPost).not.toHaveBeenCalled()
    connection.close()
  })

  test('re-arms heartbeat after its matching response and cancels timers on close', async () => {
    const connection = connectWebSocket('https://mm.example.com', 'secret', vi.fn(), vi.fn(), {
      WebSocket: FakeWebSocket as unknown as typeof WebSocket,
      heartbeatIntervalMs: 20,
      heartbeatTimeoutMs: 10,
    })
    const socket = FakeWebSocket.instances[0] as FakeWebSocket
    authenticate(socket)
    vi.advanceTimersByTime(20)
    const ping = JSON.parse(socket.sent[1] as string) as { seq: number }
    socket.message({ status: 'OK', seq_reply: ping.seq })
    vi.advanceTimersByTime(20)
    expect(socket.sent).toHaveLength(3)

    connection.close()
    await connection.done
    expect(vi.getTimerCount()).toBe(0)
  })

  test('reports malformed payloads without writing directly to console', () => {
    const malformed = vi.fn()
    const error = vi.spyOn(console, 'error').mockImplementation(() => undefined)
    const connection = connectWebSocket('https://mm.example.com', 'secret', vi.fn(), vi.fn(), {
      WebSocket: FakeWebSocket as unknown as typeof WebSocket,
      diagnostics: { malformed },
    })
    const socket = FakeWebSocket.instances[0] as FakeWebSocket
    socket.raw('{nope')
    authenticate(socket)
    socket.message({ event: 'posted', seq: 1, data: { post: '{bad' } })
    expect(malformed).toHaveBeenCalledTimes(2)
    expect(error).not.toHaveBeenCalled()
    connection.close()
  })

  test('rejects malformed post fields before watch formatting can throw', () => {
    const malformed = vi.fn()
    const onPost = vi.fn()
    const connection = connectWebSocket('https://mm.example.com', 'secret', onPost, vi.fn(), {
      WebSocket: FakeWebSocket as unknown as typeof WebSocket,
      diagnostics: { malformed },
    })
    const socket = FakeWebSocket.instances[0] as FakeWebSocket
    authenticate(socket)
    const invalid = [
      { ...post, message: null },
      { ...post, user_id: 42 },
      { ...post, create_at: null },
      { ...post, create_at: 1e100 },
      { ...post, file_ids: [7] },
    ]
    invalid.forEach((value, index) => {
      socket.message({
        event: 'posted',
        seq: index + 1,
        data: { post: JSON.stringify(value), channel_name: 'town', sender_name: 'arda' },
      })
    })

    expect(onPost).not.toHaveBeenCalled()
    expect(malformed).toHaveBeenCalledTimes(invalid.length)
    connection.close()
  })

  test('normalizes malformed optional post scalars before watch emission', () => {
    const onPost = vi.fn()
    const connection = connectWebSocket('https://mm.example.com', 'secret', onPost, vi.fn(), {
      WebSocket: FakeWebSocket as unknown as typeof WebSocket,
    })
    const socket = FakeWebSocket.instances[0] as FakeWebSocket
    authenticate(socket)
    posted(socket, 1, {
      ...post,
      reply_count: '7' as unknown as number,
      is_pinned: 'true' as unknown as boolean,
    })
    expect(onPost).toHaveBeenCalledWith(expect.objectContaining({ reply_count: 0 }), 'town', 'arda')
    expect((onPost.mock.calls[0]?.[0] as Post).is_pinned).toBeUndefined()
    connection.close()
  })

  test('uses fixed local close text for hostile FAIL reasons', () => {
    const credential = 'mattermost-secret-token'
    const connection = connectWebSocket('https://mm.example.com', credential, vi.fn(), vi.fn(), {
      WebSocket: FakeWebSocket as unknown as typeof WebSocket,
    })
    const socket = FakeWebSocket.instances[0] as FakeWebSocket
    socket.open()
    socket.message({
      status: 'FAIL',
      seq_reply: 999,
      error: { message: `${credential}\u202e${'x'.repeat(1000)}` },
    })
    expect(socket.closeCalls[0]).toEqual([4000, 'request failed'])
    expect(JSON.stringify(socket.closeCalls)).not.toContain(credential)
    connection.close()
  })

  test('does not mistake author-related FAIL text for authentication failure', () => {
    const onError = vi.fn()
    const connection = connectWebSocket('https://mm.example.com', 'secret', vi.fn(), onError, {
      WebSocket: FakeWebSocket as unknown as typeof WebSocket,
    })
    const socket = FakeWebSocket.instances[0] as FakeWebSocket
    socket.open()
    socket.message({ status: 'FAIL', seq_reply: 999, error: { message: 'Author was not found' } })
    expect(onError).not.toHaveBeenCalled()
    expect(socket.closeCalls[0]).toEqual([4000, 'request failed'])
    connection.close()
  })

  test('normalizes optional WebSocket timestamps outside the ECMAScript Date range', () => {
    const onPost = vi.fn()
    const connection = connectWebSocket('https://mm.example.com', 'secret', onPost, vi.fn(), {
      WebSocket: FakeWebSocket as unknown as typeof WebSocket,
    })
    const socket = FakeWebSocket.instances[0] as FakeWebSocket
    authenticate(socket)
    posted(socket, 1, {
      ...post,
      update_at: 8.64e15 + 1,
      edit_at: -8.64e15 - 1,
      delete_at: Number.POSITIVE_INFINITY,
    })
    expect(onPost).toHaveBeenCalledWith(
      expect.objectContaining({ update_at: 1, edit_at: 0, delete_at: 0 }),
      'town',
      'arda',
    )
    connection.close()
  })

  test('accepts the maximum finite ECMAScript timestamp without toISOString failure', () => {
    const onPost = vi.fn()
    const connection = connectWebSocket('https://mm.example.com', 'secret', onPost, vi.fn(), {
      WebSocket: FakeWebSocket as unknown as typeof WebSocket,
    })
    const socket = FakeWebSocket.instances[0] as FakeWebSocket
    authenticate(socket)
    posted(socket, 1, { ...post, create_at: 8.64e15 })

    expect(onPost).toHaveBeenCalledWith(
      expect.objectContaining({ create_at: 8.64e15 }),
      'town',
      'arda',
    )
    connection.close()
  })

  test('uses a fifteen second handshake timeout by default', () => {
    const connection = connectWebSocket('https://mm.example.com', 'secret', vi.fn(), vi.fn(), {
      WebSocket: FakeWebSocket as unknown as typeof WebSocket,
    })
    const socket = FakeWebSocket.instances[0] as FakeWebSocket
    vi.advanceTimersByTime(14_999)
    expect(socket.closeCalls).toHaveLength(0)
    vi.advanceTimersByTime(1)
    expect(socket.closeCalls[0]).toEqual([4000, 'handshake timeout'])
    connection.close()
  })

  test('treats authentication failure as fatal and never reconnects', async () => {
    const onError = vi.fn()
    const connection = connectWebSocket('https://mm.example.com', 'secret', vi.fn(), onError, {
      WebSocket: FakeWebSocket as unknown as typeof WebSocket,
    })
    const socket = FakeWebSocket.instances[0] as FakeWebSocket
    socket.open()
    const auth = JSON.parse(socket.sent[0] as string) as { seq: number }
    socket.message({ status: 'FAIL', seq_reply: auth.seq, error: { message: 'Not authorized' } })
    await connection.done
    vi.runAllTimers()
    expect(onError).toHaveBeenCalledWith(new Error('Authentication failed. Check your token.'))
    expect(FakeWebSocket.instances).toHaveLength(1)
    expect(preprocess('secret', { redact: false }).text).toBe('secret')
  })

  test('cleans up before a throwing fatal-error callback and still settles done', async () => {
    const credential = 'throwing-callback-token'
    const onError = vi.fn(() => {
      expect(preprocess(credential, { redact: false }).text).toBe(credential)
      throw new Error('callback failure')
    })
    const connection = connectWebSocket('https://mm.example.com', credential, vi.fn(), onError, {
      WebSocket: FakeWebSocket as unknown as typeof WebSocket,
    })
    const socket = FakeWebSocket.instances[0] as FakeWebSocket
    socket.open()
    const auth = JSON.parse(socket.sent[0] as string) as { seq: number }
    expect(() =>
      socket.message({ status: 'FAIL', seq_reply: auth.seq, error: { message: 'Unauthorized' } }),
    ).not.toThrow()
    await connection.done
    expect(onError).toHaveBeenCalledTimes(1)
    expect(vi.getTimerCount()).toBe(0)
  })
})
