import { spawnSync } from 'node:child_process'
import { describe, expect, test } from 'vitest'
import { encodeChannelHistoryCursor } from '../src/cursor'

const validCursor = encodeChannelHistoryCursor({
  v: 1,
  scope: 'channel',
  channelId: 'general',
  boundary: { createAt: 100, id: 'anchor' },
  since: null,
})

function run(args: string[]) {
  return spawnSync(
    'bun',
    ['src/index.ts', '--url', 'http://127.0.0.1:1', '--token', 'test-token', ...args],
    { encoding: 'utf8', timeout: 5_000 },
  )
}

describe('Commander cursor validation', () => {
  test('rejects a malformed cursor before any network failure can occur', () => {
    const result = run(['channel', 'general', '--cursor', ''])

    expect(result.status).toBe(1)
    expect(result.stderr).toContain('Error: Invalid cursor.')
    expect(result.stderr).not.toContain('fetch failed')
  })

  test('distinguishes explicit --since from its Commander default', () => {
    const explicit = run(['channel', 'general', '--cursor', validCursor, '--since', '7d'])
    expect(explicit.status).toBe(1)
    expect(explicit.stderr).toContain('A cursor cannot be combined with --since.')
    expect(explicit.stderr).not.toContain('fetch failed')

    const defaulted = run(['channel', 'general', '--cursor', validCursor])
    expect(defaulted.status).toBe(1)
    expect(defaulted.stderr).not.toContain('A cursor cannot be combined with --since.')
    expect(defaulted.stderr).not.toContain('Invalid cursor.')
    expect(defaulted.stderr).toContain('Unable to connect')
  })
})
