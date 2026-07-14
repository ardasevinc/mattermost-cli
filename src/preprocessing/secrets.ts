// Secret detection and masking

import type { Redaction } from '../types'
import { SECRET_PATTERNS } from './patterns'

interface DetectedSecret {
  type: string
  value: string
  start: number
  end: number
}

interface SecretGroup {
  start: number
  end: number
  secrets: DetectedSecret[]
}

function groupOverlappingSecrets(secrets: DetectedSecret[]): SecretGroup[] {
  const sorted = [...secrets].sort((a, b) => a.start - b.start || b.end - a.end)
  const groups: SecretGroup[] = []

  for (const secret of sorted) {
    const current = groups.at(-1)

    if (!current || secret.start >= current.end) {
      groups.push({ start: secret.start, end: secret.end, secrets: [secret] })
      continue
    }

    current.end = Math.max(current.end, secret.end)
    current.secrets.push(secret)
  }

  return groups
}

// Detect all secrets in text
export function detectSecrets(text: string): DetectedSecret[] {
  const secrets: DetectedSecret[] = []
  const seen = new Set<string>() // Avoid duplicates at same position

  for (const { name, pattern } of SECRET_PATTERNS) {
    // Reset regex state (important for global patterns)
    pattern.lastIndex = 0

    let match: RegExpExecArray | null = pattern.exec(text)
    while (match !== null) {
      // Use captured group if available, otherwise full match
      const value = match[1] || match[0]
      const offset = match[0].indexOf(value)
      const start = match.index + (offset >= 0 ? offset : 0)
      const end = start + value.length
      const key = `${start}:${end}`

      if (!seen.has(key)) {
        seen.add(key)
        secrets.push({ type: name, value, start, end })
      }

      match = pattern.exec(text)
    }
  }

  // Sort by position (start)
  return secrets.sort((a, b) => a.start - b.start)
}

// Mask a secret value, showing first and last few chars
export function maskSecret(value: string, type: string): string {
  // Very short secrets get fully redacted
  if (value.length <= 8) {
    return `[REDACTED:${type}]`
  }

  // Show ~10% of chars on each end, min 2, max 4
  const visibleCount = Math.max(2, Math.min(4, Math.floor(value.length * 0.1)))

  const prefix = value.slice(0, visibleCount)
  const suffix = value.slice(-visibleCount)

  return `${prefix}...${suffix}`
}

// Redact all secrets in text, returning new text and redaction log
export function redactSecrets(text: string): {
  text: string
  redactions: Redaction[]
} {
  const secrets = detectSecrets(text)

  if (secrets.length === 0) {
    return { text, redactions: [] }
  }

  const redactions: Redaction[] = []
  let result = ''
  let lastEnd = 0

  for (const group of groupOverlappingSecrets(secrets)) {
    // Add text before this secret
    result += text.slice(lastEnd, group.start)

    // Mask the full union once so nested/overlapping matches cannot move the cursor backwards
    // and re-append part of a previously redacted value.
    const groupValue = text.slice(group.start, group.end)
    const masked = maskSecret(groupValue, group.secrets[0]?.type ?? 'secret')
    result += masked

    const types = [...new Set(group.secrets.map((secret) => secret.type))]
    redactions.push({
      type: types.join('+'),
      masked,
      position: group.start,
    })

    lastEnd = group.end
  }

  // Add remaining text
  result += text.slice(lastEnd)

  return { text: result, redactions }
}
