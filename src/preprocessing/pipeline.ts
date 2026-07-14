import type { PreprocessResult } from '../types'
import { getActiveMattermostCredentials } from './credential'
import { sanitizeControlCharacters } from './sanitize'
import { redactSecrets } from './secrets'

export function preprocess(text: string, options: { redact?: boolean } = {}): PreprocessResult {
  const credentials = getActiveMattermostCredentials()
  return sanitizeResult(
    redactSecrets(text, {
      detectPatterns: options.redact !== false,
      exact: credentials.map((value) => ({ type: 'mattermost_credential', value })),
    }),
  )
}

function sanitizeResult(result: PreprocessResult): PreprocessResult {
  const { text: redactedText, redactions } = result
  return {
    text: sanitizeControlCharacters(redactedText),
    redactions: redactions.map((redaction) => ({
      ...redaction,
      masked: sanitizeControlCharacters(redaction.masked),
      position: sanitizeControlCharacters(redactedText.slice(0, redaction.position)).length,
    })),
  }
}
