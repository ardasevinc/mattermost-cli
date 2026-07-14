import { describe, expect, test } from 'vitest'
import {
  preprocess,
  registerActiveMattermostCredential,
  sanitizeControlCharacters,
  sanitizeTerminalLabel,
  setActiveMattermostCredential,
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

  test('makes bidi controls visible while preserving emoji joiners and ordinary Unicode', () => {
    expect(sanitizeControlCharacters('مرحبا\u200e\u200f\u202eabc\u2066x\u2069 👩‍💻 café')).toBe(
      'مرحبا\\u200e\\u200f\\u202eabc\\u2066x\\u2069 👩‍💻 café',
    )
  })
})

describe('preprocess', () => {
  test('always protects only the exact active credential with redaction disabled', () => {
    const credential = '9xuqwrwgstrb3mzrxb83nb357a'
    setActiveMattermostCredential(credential)
    try {
      const result = preprocess(`credential=${credential} object=aaaaaaaaaaaaaaaaaaaaaaaaaa`, {
        redact: false,
      })
      expect(result.text).not.toContain(credential)
      expect(result.text).toContain('aaaaaaaaaaaaaaaaaaaaaaaaaa')
      expect(result.redactions).toHaveLength(1)
      expect(JSON.stringify(result.redactions)).not.toContain(credential)
    } finally {
      setActiveMattermostCredential(undefined)
    }
  })

  test('protects every registered credential and gives it overlap-mask precedence', () => {
    const first = 'active-token-one'
    const second = 'active-token-two'
    const releaseFirst = registerActiveMattermostCredential(first)
    const releaseSecond = registerActiveMattermostCredential(second)
    try {
      const result = preprocess(`password=${first} and ${second}`, { redact: true })
      expect(result.text).toBe(
        'password=[REDACTED:mattermost_credential] and [REDACTED:mattermost_credential]',
      )
      expect(result.redactions.every(({ type }) => type.startsWith('mattermost_credential'))).toBe(
        true,
      )
    } finally {
      releaseFirst()
      releaseSecond()
    }
  })

  test('default-owner replacement removes stale short credentials', () => {
    setActiveMattermostCredential('old')
    setActiveMattermostCredential('new')
    expect(preprocess('old new', { redact: false }).text).toBe(
      'old [REDACTED:mattermost_credential]',
    )
  })

  test('owned credentials use refcounts across duplicate live values', () => {
    const first = registerActiveMattermostCredential('shared')
    const second = registerActiveMattermostCredential('shared')
    first()
    expect(preprocess('shared', { redact: false }).text).toBe('[REDACTED:mattermost_credential]')
    second()
    expect(preprocess('shared', { redact: false }).text).toBe('shared')
  })
  test('always sanitizes terminal controls when redaction is disabled', () => {
    const result = preprocess('hello\u001b[31mworld', { redact: false })

    expect(result.text).toBe('hello\\u001b[31mworld')
    expect(result.redactions).toEqual([])
  })

  test('reports redaction positions after visible control expansion', () => {
    const token = `ghp_${'a'.repeat(36)}`
    const result = preprocess(`x\u001b ${token}`)

    expect(result.redactions[0]?.position).toBe('x\\u001b '.length)
  })

  test('reports multiple redactions at their offsets in final emitted text', () => {
    const first = `ghp_${'a'.repeat(36)}`
    const second = `ghp_${'b'.repeat(36)}`
    const result = preprocess(`x\u001b ${first} gap ${second}`)

    expect(result.redactions).toHaveLength(2)
    for (const redaction of result.redactions) {
      expect(redaction.position).toBe(result.text.indexOf(redaction.masked))
    }
  })
})
