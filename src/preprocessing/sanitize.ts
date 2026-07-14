function isUnsafeControlCode(code: number): boolean {
  return (
    (code >= 0x00 && code <= 0x08) ||
    (code >= 0x0b && code <= 0x1f) ||
    (code >= 0x7f && code <= 0x9f)
  )
}

function visibleUnicodeEscape(code: number): string {
  return `\\u${code.toString(16).padStart(4, '0')}`
}

/**
 * Make terminal control bytes visible instead of executable while preserving ordinary message
 * whitespace. This protects pretty, plain, markdown, JSON, and live watch output through one
 * preprocessing boundary.
 */
export function sanitizeControlCharacters(text: string): string {
  let result = ''

  for (const character of text.replace(/\r\n/g, '\n')) {
    const code = character.charCodeAt(0)
    result += isUnsafeControlCode(code) ? visibleUnicodeEscape(code) : character
  }

  return result
}

/** Keep remotely controlled labels and errors on one visible terminal line. */
export function sanitizeTerminalLabel(text: string): string {
  return sanitizeControlCharacters(text).replace(/\n/g, '\\n').replace(/\t/g, '\\t')
}
