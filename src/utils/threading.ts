import type { ProcessedMessage } from '../types'

const canonicalIdentities = new WeakMap<ProcessedMessage, { id: string; rootId?: string }>()

export function setCanonicalPostIdentity(
  message: ProcessedMessage,
  id: string,
  rootId?: string,
): void {
  canonicalIdentities.set(message, { id, rootId })
}

function canonicalIdentity(message: ProcessedMessage): { id: string; rootId?: string } {
  return canonicalIdentities.get(message) ?? { id: message.id, rootId: message.rootId }
}

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
    const identity = canonicalIdentity(msg)
    if (!identity.rootId) {
      const root: ProcessedMessage = { ...msg, replies: [] }
      setCanonicalPostIdentity(root, identity.id)
      rootMap.set(identity.id, root)
      roots.push(root)
    }
  }

  for (const msg of sorted) {
    const identity = canonicalIdentity(msg)
    if (!identity.rootId) continue

    const root = rootMap.get(identity.rootId)
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
