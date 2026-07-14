import { describe, expect, test } from 'vitest'
import {
  preprocess,
  sanitizeControlCharacters,
  sanitizeTerminalLabel,
} from '../../src/preprocessing'

describe('sanitizeControlCharacters', () => {
  test('neutralizes ANSI and OSC terminal controls without hiding their presence', () => {
    const text = 'before\u001b[2Jmiddle\u001b]52;c;YXR0YWNrZXI=\u0007after'

    expect(sanitizeControlCharacters(text)).toBe(
      'before\\u001b[2Jmiddle\\u001b]52;c;YXR0YWNrZXI=\\u0007after',
    )
  })

  test('preserves line feeds and tabs while neutralizing carriage-return forgery', () => {
    const text = 'first\r\nsecond\rforged\n\tindented'

    expect(sanitizeControlCharacters(text)).toBe('first\nsecond\\u000dforged\n\tindented')
  })

  test('keeps remotely controlled labels on one visible line', () => {
    expect(sanitizeTerminalLabel('alice\r\nadmin\trole')).toBe('alice\\nadmin\\trole')
  })
})

describe('preprocess', () => {
  test('always sanitizes terminal controls when redaction is disabled', () => {
    const result = preprocess('hello\u001b[31mworld', { redact: false })

    expect(result.text).toBe('hello\\u001b[31mworld')
    expect(result.redactions).toEqual([])
  })
})
