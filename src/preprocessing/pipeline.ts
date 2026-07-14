import type { PreprocessResult } from '../types'
import { sanitizeControlCharacters } from './sanitize'
import { redactSecrets } from './secrets'

export function preprocess(text: string, options: { redact?: boolean } = {}): PreprocessResult {
  if (options.redact === false) {
    return { text: sanitizeControlCharacters(text), redactions: [] }
  }

  const { text: redactedText, redactions } = redactSecrets(text)
  return {
    text: sanitizeControlCharacters(redactedText),
    redactions: redactions.map((redaction) => ({
      ...redaction,
      masked: sanitizeControlCharacters(redaction.masked),
      position: sanitizeControlCharacters(redactedText.slice(0, redaction.position)).length,
    })),
  }
}
