import { describe, expect, it } from 'bun:test'
import type { ProcessedMessage } from '../../src/types'
import { groupIntoThreads } from '../../src/utils/threading'

interface MessageInput {
  id: string
  timestamp: string
  user?: string
  userId?: string
  text?: string
  files?: string[]
  rootId?: string
  replyCount?: number
  replies?: ProcessedMessage[]
}

function makeMessage(overrides: MessageInput): ProcessedMessage {
  return {
    id: overrides.id,
    user: overrides.user ?? 'user',
    userId: overrides.userId ?? 'uid',
    text: overrides.text ?? 'message',
    timestamp: new Date(overrides.timestamp),
    files: overrides.files ?? [],
    rootId: overrides.rootId,
    replyCount: overrides.replyCount,
    replies: overrides.replies,
  }
}

describe('groupIntoThreads', () => {
  it('groups replies under their root and sorts reply order chronologically', () => {
    const root = makeMessage({ id: 'root', timestamp: '2026-02-21T10:00:00.000Z' })
    const lateReply = makeMessage({
      id: 'reply-2',
      rootId: 'root',
      timestamp: '2026-02-21T10:03:00.000Z',
    })
    const earlyReply = makeMessage({
      id: 'reply-1',
      rootId: 'root',
      timestamp: '2026-02-21T10:01:00.000Z',
    })

    const result = groupIntoThreads([lateReply, root, earlyReply])

    expect(result).toHaveLength(1)
    expect(result[0]?.id).toBe('root')
    expect(result[0]?.replies?.map((r) => r.id)).toEqual(['reply-1', 'reply-2'])
  })

  it('keeps replies with missing roots as standalone messages in chronological order', () => {
    const orphanReply = makeMessage({
      id: 'orphan-reply',
      rootId: 'missing-root',
      timestamp: '2026-02-21T08:00:00.000Z',
    })
    const rootOne = makeMessage({ id: 'root-1', timestamp: '2026-02-21T09:00:00.000Z' })
    const rootTwo = makeMessage({ id: 'root-2', timestamp: '2026-02-21T10:00:00.000Z' })

    const result = groupIntoThreads([rootTwo, orphanReply, rootOne])

    expect(result.map((msg) => msg.id)).toEqual(['orphan-reply', 'root-1', 'root-2'])
    expect(result[0]?.rootId).toBe('missing-root')
  })
})
