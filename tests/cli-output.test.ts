import { describe, expect, test } from 'vitest'
import { selectOutputMode } from '../src/cli'

describe('global output mode selection', () => {
  test.each([
    { json: true, isTTY: true, expected: 'json' },
    { json: true, isTTY: false, expected: 'json' },
    { json: false, isTTY: true, expected: 'pretty' },
    { json: false, isTTY: false, expected: 'markdown' },
  ] as const)('$expected when json=$json and isTTY=$isTTY', ({ json, isTTY, expected }) => {
    expect(selectOutputMode(json, isTTY)).toBe(expected)
  })
})
