// Preprocessing pipeline

import type { PreprocessResult } from '../types'
import { sanitizeControlCharacters } from './sanitize'
import { redactSecrets } from './secrets'

export { SECRET_PATTERNS } from './patterns'
export { sanitizeControlCharacters, sanitizeTerminalLabel } from './sanitize'
export { detectSecrets, maskSecret, redactSecrets } from './secrets'

// Main preprocessing function - runs all preprocessing steps
export function preprocess(text: string, options: { redact?: boolean } = {}): PreprocessResult {
  const sanitizedText = sanitizeControlCharacters(text)

  if (options.redact === false) {
    return { text: sanitizedText, redactions: [] }
  }

  const { text: redactedText, redactions } = redactSecrets(sanitizedText)

  return {
    text: redactedText,
    redactions,
  }
}
