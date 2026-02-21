import type { Post, WSPostEvent } from '../types'

interface SocketMessage {
  event?: string
  status?: string
  error?: string
  data?: Record<string, unknown>
}

function toWebSocketBaseUrl(serverUrl: string): string {
  const normalized = serverUrl.replace(/\/+$/, '')

  if (normalized.startsWith('https://')) {
    return `wss://${normalized.slice('https://'.length)}`
  }
  if (normalized.startsWith('http://')) {
    return `ws://${normalized.slice('http://'.length)}`
  }

  throw new Error(`Invalid server URL: ${serverUrl}`)
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
  broadcastChannelId?: string
} | null {
  if (payload.event !== 'posted' || !payload.data) return null

  const event = payload as unknown as WSPostEvent
  const postJson = event.data.post

  try {
    const post = JSON.parse(postJson) as Post
    return {
      post,
      channelName: event.data.channel_name,
      senderName: event.data.sender_name,
      broadcastChannelId: event.broadcast?.channel_id,
    }
  } catch {
    console.error('Warning: Could not parse websocket post payload. Skipping event.')
    return null
  }
}

export function connectWebSocket(
  serverUrl: string,
  token: string,
  onPost: (post: Post, channelName: string, senderName: string) => void,
  onError: (error: Error) => void,
  channelId?: string,
): { close: () => void } {
  const wsBaseUrl = toWebSocketBaseUrl(serverUrl)
  const wsUrl = `${wsBaseUrl}/api/v4/websocket`

  let closedByClient = false
  let hasErrored = false

  const emitError = (error: Error): void => {
    if (hasErrored) return
    hasErrored = true
    onError(error)
  }

  const socket = new WebSocket(wsUrl)

  socket.onopen = () => {
    socket.send(
      JSON.stringify({
        seq: 1,
        action: 'authentication_challenge',
        data: { token },
      }),
    )
  }

  socket.onmessage = (event) => {
    const payload = parseSocketMessage(event.data)
    if (!payload) return

    if (payload.status === 'FAIL') {
      const errorText = payload.error?.toLowerCase() ?? ''
      if (errorText.includes('authentication') || errorText.includes('not authorized')) {
        emitError(new Error('Authentication failed. Check your token.'))
      } else {
        emitError(new Error(payload.error || 'WebSocket request failed.'))
      }
      socket.close()
      return
    }

    const parsedPost = parsePostEvent(payload)
    if (!parsedPost) return

    const eventChannelId = parsedPost.broadcastChannelId || parsedPost.post.channel_id
    if (channelId && eventChannelId !== channelId) {
      return
    }

    onPost(parsedPost.post, parsedPost.channelName, parsedPost.senderName)
  }

  socket.onerror = () => {
    emitError(new Error(`WebSocket connection failure: ${wsUrl}`))
  }

  socket.onclose = (event) => {
    if (closedByClient || hasErrored) return

    const reason = event.reason ? ` (${event.reason})` : ''
    emitError(new Error(`WebSocket connection closed: code ${event.code}${reason}`))
  }

  return {
    close: () => {
      closedByClient = true
      socket.close()
    },
  }
}
