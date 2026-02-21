import type { ProcessedMessage } from '../types'

function byTimestamp(a: ProcessedMessage, b: ProcessedMessage): number {
  const diff = a.timestamp.getTime() - b.timestamp.getTime()
  if (diff !== 0) return diff
  return a.id.localeCompare(b.id)
}

// Group flat messages into a threaded structure.
// Replies with missing roots are kept as standalone messages.
export function groupIntoThreads(messages: ProcessedMessage[]): ProcessedMessage[] {
  const sorted = [...messages].sort(byTimestamp)
  const rootMap = new Map<string, ProcessedMessage>()
  const roots: ProcessedMessage[] = []
  const standaloneReplies: ProcessedMessage[] = []

  for (const msg of sorted) {
    if (!msg.rootId) {
      const root: ProcessedMessage = { ...msg, replies: [] }
      rootMap.set(msg.id, root)
      roots.push(root)
    }
  }

  for (const msg of sorted) {
    if (!msg.rootId) continue

    const root = rootMap.get(msg.rootId)
    if (root) {
      root.replies?.push(msg)
    } else {
      standaloneReplies.push(msg)
    }
  }

  for (const root of roots) {
    if (root.replies && root.replies.length > 1) {
      root.replies.sort(byTimestamp)
    }
  }

  return [...roots, ...standaloneReplies].sort(byTimestamp)
}
