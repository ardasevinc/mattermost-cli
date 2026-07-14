import { describe, expect, test } from 'vitest'
import { parsePositiveSafeInteger } from '../src/validation'

describe('parsePositiveSafeInteger', () => {
  test.each([
    ['1', 1],
    ['20', 20],
    [String(Number.MAX_SAFE_INTEGER), Number.MAX_SAFE_INTEGER],
  ])('accepts canonical positive safe integer %s', (value, expected) => {
    expect(parsePositiveSafeInteger(value)).toBe(expected)
  })

  test.each([
    '',
    '0',
    '-1',
    '+1',
    '01',
    '2.5',
    '20users',
    '1e3',
    ' 20',
    '20 ',
    String(Number.MAX_SAFE_INTEGER + 1),
  ])('rejects non-canonical or unsafe value %j', (value) => {
    expect(parsePositiveSafeInteger(value)).toBeNull()
  })
})
