// Preprocessing pipeline

import type { PreprocessResult } from '../types'
import { redactSecrets } from './secrets'

export { SECRET_PATTERNS } from './patterns'
export { detectSecrets, maskSecret, redactSecrets } from './secrets'

// Main preprocessing function - runs all preprocessing steps
export function preprocess(text: string): PreprocessResult {
  // Currently just secret redaction, but designed for extensibility
  const { text: redactedText, redactions } = redactSecrets(text)

  return {
    text: redactedText,
    redactions,
  }
}
