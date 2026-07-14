import { sanitizeTerminalLabel } from '../preprocessing'
import type { Post, WSPostEvent } from '../types'
import { normalizeServerUrl } from './url'

interface SocketMessage {
  event?: string
  status?: string
  error?: unknown
  data?: Record<string, unknown>
  seq?: number
  seq_reply?: number
}

export interface WebSocketGap {
  expected: number
  received: number
  reason: 'sequence_mismatch' | 'connection_changed'
}

export interface WebSocketDiagnostics {
  reconnect?: (attempt: number, delayMs: number) => void
  gap?: (gap: WebSocketGap) => void
  connected?: () => void
  malformed?: (message: string) => void
}

export interface WebSocketOptions {
  channelId?: string
  diagnostics?: WebSocketDiagnostics
  WebSocket?: typeof globalThis.WebSocket
  random?: () => number
  handshakeTimeoutMs?: number
  heartbeatIntervalMs?: number
  heartbeatTimeoutMs?: number
  backoffBaseMs?: number
  backoffMaxMs?: number
}

export function getSocketErrorMessage(error: unknown): string {
  if (typeof error === 'string') return sanitizeTerminalLabel(error)
  if (error && typeof error === 'object' && 'message' in error) {
    const message = (error as { message?: unknown }).message
    if (typeof message === 'string') return sanitizeTerminalLabel(message)
  }
  return 'WebSocket request failed.'
}

function toWebSocketUrl(serverUrl: string): URL {
  const url = new URL(normalizeServerUrl(serverUrl))
  url.protocol = url.protocol === 'https:' ? 'wss:' : 'ws:'
  url.pathname = `${url.pathname.replace(/\/+$/, '')}/api/v4/websocket`
  return url
}

function parseSocketMessage(raw: unknown): SocketMessage | null {
  if (typeof raw !== 'string') return null
  try {
    return JSON.parse(raw) as SocketMessage
  } catch {
    return null
  }
}

function parsePostEvent(payload: SocketMessage): {
  post: Post
  channelName: string
  senderName: string
} | null {
  if (payload.event !== 'posted' || !payload.data) return null
  const event = payload as unknown as WSPostEvent
  if (typeof event.data.post !== 'string') return null
  try {
    const value: unknown = JSON.parse(event.data.post)
    if (!value || typeof value !== 'object' || Array.isArray(value)) return null
    const raw = value as Record<string, unknown>
    if (
      typeof raw.id !== 'string' ||
      typeof raw.channel_id !== 'string' ||
      typeof raw.user_id !== 'string' ||
      typeof raw.message !== 'string' ||
      typeof raw.root_id !== 'string' ||
      typeof raw.create_at !== 'number' ||
      !Number.isFinite(raw.create_at) ||
      !Number.isFinite(new Date(raw.create_at).getTime()) ||
      !Array.isArray(raw.file_ids) ||
      !raw.file_ids.every((id) => typeof id === 'string')
    ) {
      return null
    }
    const post: Post = {
      id: raw.id,
      channel_id: raw.channel_id,
      user_id: raw.user_id,
      message: raw.message,
      root_id: raw.root_id,
      create_at: raw.create_at,
      file_ids: raw.file_ids as string[],
      update_at: typeof raw.update_at === 'number' ? raw.update_at : raw.create_at,
      delete_at: typeof raw.delete_at === 'number' ? raw.delete_at : 0,
      edit_at: typeof raw.edit_at === 'number' ? raw.edit_at : 0,
      type: typeof raw.type === 'string' ? raw.type : '',
      props:
        raw.props && typeof raw.props === 'object' && !Array.isArray(raw.props)
          ? (raw.props as Record<string, unknown>)
          : {},
      hashtags: typeof raw.hashtags === 'string' ? raw.hashtags : '',
      reply_count: typeof raw.reply_count === 'number' ? raw.reply_count : 0,
      pending_post_id: typeof raw.pending_post_id === 'string' ? raw.pending_post_id : '',
    }
    return {
      post,
      channelName: typeof event.data.channel_name === 'string' ? event.data.channel_name : '',
      senderName: typeof event.data.sender_name === 'string' ? event.data.sender_name : '',
    }
  } catch {
    return null
  }
}

/** A resumable Mattermost WebSocket. Reconnection is scheduled only from onclose. */
export function connectWebSocket(
  serverUrl: string,
  token: string,
  onPost: (post: Post, channelName: string, senderName: string) => void,
  onError: (error: Error) => void,
  channelIdOrOptions?: string | WebSocketOptions,
): { close: () => void; done: Promise<void> } {
  const options: WebSocketOptions =
    typeof channelIdOrOptions === 'string'
      ? { channelId: channelIdOrOptions }
      : (channelIdOrOptions ?? {})
  const Socket = options.WebSocket ?? globalThis.WebSocket
  const random = options.random ?? Math.random
  const handshakeTimeoutMs = options.handshakeTimeoutMs ?? 15_000
  const heartbeatIntervalMs = options.heartbeatIntervalMs ?? 30_000
  const heartbeatTimeoutMs = options.heartbeatTimeoutMs ?? 10_000
  const backoffBaseMs = options.backoffBaseMs ?? 1_000
  const backoffMaxMs = options.backoffMaxMs ?? 30_000

  let socket: WebSocket | undefined
  let generation = 0
  let stopped = false
  let fatal = false
  let reconnectAttempt = 0
  let connectionId: string | undefined
  let nextServerSequence = 0
  let actionSequence = 1
  let handshakeTimer: ReturnType<typeof setTimeout> | undefined
  let heartbeatTimer: ReturnType<typeof setTimeout> | undefined
  let pongTimer: ReturnType<typeof setTimeout> | undefined
  let reconnectTimer: ReturnType<typeof setTimeout> | undefined
  let pendingPingSeq: number | undefined
  let resolveDone!: () => void
  const done = new Promise<void>((resolve) => {
    resolveDone = resolve
  })

  const clearConnectionTimers = (): void => {
    if (handshakeTimer) clearTimeout(handshakeTimer)
    if (heartbeatTimer) clearTimeout(heartbeatTimer)
    if (pongTimer) clearTimeout(pongTimer)
    handshakeTimer = heartbeatTimer = pongTimer = undefined
    pendingPingSeq = undefined
  }

  const close = (): void => {
    if (stopped) return
    stopped = true
    generation++
    clearConnectionTimers()
    if (reconnectTimer) clearTimeout(reconnectTimer)
    reconnectTimer = undefined
    const active = socket
    socket = undefined
    if (active && active.readyState < 2) active.close(1000, 'client shutdown')
    resolveDone()
  }

  const failFatal = (error: Error): void => {
    if (fatal || stopped) return
    fatal = true
    onError(error)
    close()
  }

  const scheduleHeartbeat = (currentGeneration: number): void => {
    if (stopped || currentGeneration !== generation) return
    heartbeatTimer = setTimeout(() => {
      if (stopped || currentGeneration !== generation || !socket || socket.readyState !== 1) return
      pendingPingSeq = actionSequence++
      socket.send(JSON.stringify({ seq: pendingPingSeq, action: 'ping', data: {} }))
      pongTimer = setTimeout(() => {
        if (stopped || currentGeneration !== generation) return
        socket?.close(4000, 'heartbeat timeout')
      }, heartbeatTimeoutMs)
    }, heartbeatIntervalMs)
  }

  const scheduleReconnect = (): void => {
    if (stopped || fatal || reconnectTimer) return
    const exponential = Math.min(backoffMaxMs, backoffBaseMs * 2 ** reconnectAttempt)
    const delay = Math.round(exponential * (0.8 + random() * 0.4))
    reconnectAttempt++
    options.diagnostics?.reconnect?.(reconnectAttempt, delay)
    reconnectTimer = setTimeout(() => {
      reconnectTimer = undefined
      connect()
    }, delay)
  }

  const connect = (): void => {
    if (stopped || fatal) return
    const currentGeneration = ++generation
    const url = toWebSocketUrl(serverUrl)
    if (connectionId) {
      url.searchParams.set('connection_id', connectionId)
      url.searchParams.set('sequence_number', String(nextServerSequence))
    }
    const current = new Socket(url.toString())
    socket = current
    let authenticated = false
    let helloReceived = false
    let authSeq = -1

    const completeHandshake = (): void => {
      if (!authenticated || !helloReceived || currentGeneration !== generation) return
      if (handshakeTimer) clearTimeout(handshakeTimer)
      handshakeTimer = undefined
      reconnectAttempt = 0
      options.diagnostics?.connected?.()
      scheduleHeartbeat(currentGeneration)
    }

    const rejectSequence = (received: number): void => {
      options.diagnostics?.gap?.({
        expected: nextServerSequence,
        received,
        reason: 'sequence_mismatch',
      })
      current.close(4000, 'sequence mismatch')
    }

    handshakeTimer = setTimeout(() => {
      if (currentGeneration === generation) current.close(4000, 'handshake timeout')
    }, handshakeTimeoutMs)

    current.onopen = () => {
      if (currentGeneration !== generation || stopped) return
      authSeq = actionSequence++
      current.send(
        JSON.stringify({
          seq: authSeq,
          action: 'authentication_challenge',
          data: { token },
        }),
      )
    }

    current.onmessage = (event) => {
      if (currentGeneration !== generation || stopped) return
      const payload = parseSocketMessage(event.data)
      if (!payload) {
        options.diagnostics?.malformed?.('Malformed WebSocket message skipped.')
        return
      }

      if (payload.status === 'FAIL') {
        const message = getSocketErrorMessage(payload.error)
        const normalized = message.toLowerCase()
        if (
          payload.seq_reply === authSeq ||
          normalized.includes('auth') ||
          normalized.includes('authorized')
        ) {
          failFatal(new Error('Authentication failed. Check your token.'))
        } else {
          current.close(4000, message)
        }
        return
      }

      if (payload.status === 'OK' && payload.seq_reply === authSeq) {
        authenticated = true
        completeHandshake()
      } else if (payload.status === 'OK' && payload.seq_reply === pendingPingSeq && pongTimer) {
        clearTimeout(pongTimer)
        pongTimer = undefined
        pendingPingSeq = undefined
        scheduleHeartbeat(currentGeneration)
      }

      if (payload.event) {
        if (typeof payload.seq !== 'number') {
          options.diagnostics?.malformed?.('WebSocket event without a sequence skipped.')
          return
        }
        if (payload.event === 'hello' && typeof payload.data?.connection_id === 'string') {
          const receivedConnectionId = payload.data.connection_id
          if (connectionId && receivedConnectionId !== connectionId) {
            options.diagnostics?.gap?.({
              expected: nextServerSequence,
              received: payload.seq,
              reason: 'connection_changed',
            })
            nextServerSequence = 0
          }
          connectionId = receivedConnectionId
        }
        if (payload.seq !== nextServerSequence) {
          rejectSequence(payload.seq)
          return
        }
        nextServerSequence = payload.seq + 1
        if (payload.event === 'hello') {
          helloReceived = true
          completeHandshake()
        }
      }

      if (!authenticated || !helloReceived || payload.event !== 'posted') return
      const parsed = parsePostEvent(payload)
      if (!parsed) {
        options.diagnostics?.malformed?.('Malformed WebSocket post payload skipped.')
        return
      }
      if (options.channelId && parsed.post.channel_id !== options.channelId) return
      onPost(parsed.post, parsed.channelName, parsed.senderName)
    }

    current.onerror = () => {
      if (currentGeneration !== generation || stopped) return
      if (current.readyState < 2) current.close(4000, 'connection failure')
    }

    current.onclose = () => {
      if (currentGeneration !== generation || stopped) return
      generation++
      clearConnectionTimers()
      if (socket === current) socket = undefined
      scheduleReconnect()
    }
  }

  connect()
  return { close, done }
}
