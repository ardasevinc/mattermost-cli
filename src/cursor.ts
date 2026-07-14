export interface ChannelHistoryCursor {
  v: 1
  scope: 'channel'
  channelId: string
  boundary: { createAt: number; id: string }
  since: number | null
  safeBeforePostId?: string
}

const MAX_ENCODED_CURSOR_LENGTH = 2048
const MAX_DECODED_CURSOR_LENGTH = 1536
const MAX_DATE_MILLISECONDS = 8_640_000_000_000_000
const MAX_ID_LENGTH = 128
const SAFE_ID = /^[A-Za-z0-9_-]+$/

function invalidCursor(): never {
  throw new Error('Invalid cursor.')
}

function exactKeys(value: Record<string, unknown>, required: string[], optional: string[] = []) {
  const allowed = new Set([...required, ...optional])
  return (
    required.every((key) => Object.hasOwn(value, key)) &&
    Object.keys(value).every((key) => allowed.has(key))
  )
}

function isSafeId(value: unknown): value is string {
  return (
    typeof value === 'string' &&
    value.length > 0 &&
    value.length <= MAX_ID_LENGTH &&
    SAFE_ID.test(value)
  )
}

function validateCursor(value: unknown): ChannelHistoryCursor {
  if (!value || typeof value !== 'object' || Array.isArray(value)) return invalidCursor()
  const candidate = value as Record<string, unknown>
  if (
    !exactKeys(candidate, ['v', 'scope', 'channelId', 'boundary', 'since'], ['safeBeforePostId'])
  ) {
    return invalidCursor()
  }
  const boundary = candidate.boundary
  if (!boundary || typeof boundary !== 'object' || Array.isArray(boundary)) return invalidCursor()
  const boundaryValue = boundary as Record<string, unknown>
  if (!exactKeys(boundaryValue, ['createAt', 'id'])) return invalidCursor()
  if (
    candidate.v !== 1 ||
    candidate.scope !== 'channel' ||
    !isSafeId(candidate.channelId) ||
    !isSafeId(boundaryValue.id) ||
    typeof boundaryValue.createAt !== 'number' ||
    !Number.isSafeInteger(boundaryValue.createAt) ||
    boundaryValue.createAt < 0 ||
    boundaryValue.createAt > MAX_DATE_MILLISECONDS ||
    !(
      candidate.since === null ||
      (typeof candidate.since === 'number' &&
        Number.isSafeInteger(candidate.since) &&
        candidate.since >= 0 &&
        candidate.since <= boundaryValue.createAt)
    ) ||
    (Object.hasOwn(candidate, 'safeBeforePostId') && !isSafeId(candidate.safeBeforePostId))
  ) {
    return invalidCursor()
  }
  return value as ChannelHistoryCursor
}

export function encodeChannelHistoryCursor(cursor: ChannelHistoryCursor): string {
  const validated = validateCursor(cursor)
  const encoded = Buffer.from(JSON.stringify(validated), 'utf8').toString('base64url')
  if (encoded.length > MAX_ENCODED_CURSOR_LENGTH) return invalidCursor()
  return encoded
}

export function decodeChannelHistoryCursor(encoded: string): ChannelHistoryCursor {
  if (
    encoded.length === 0 ||
    encoded.length > MAX_ENCODED_CURSOR_LENGTH ||
    !/^[A-Za-z0-9_-]+$/.test(encoded)
  ) {
    return invalidCursor()
  }
  let text: string
  try {
    const bytes = Buffer.from(encoded, 'base64url')
    if (bytes.length === 0 || bytes.length > MAX_DECODED_CURSOR_LENGTH) return invalidCursor()
    text = bytes.toString('utf8')
    if (Buffer.from(text, 'utf8').toString('base64url') !== encoded) return invalidCursor()
  } catch {
    return invalidCursor()
  }
  let value: unknown
  try {
    value = JSON.parse(text)
  } catch {
    return invalidCursor()
  }
  return validateCursor(value)
}

export function comparePostIds(a: string, b: string): number {
  return a < b ? -1 : a > b ? 1 : 0
}
