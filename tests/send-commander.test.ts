import { spawnSync } from 'node:child_process'
import { describe, expect, test } from 'vitest'

const entry = ['src/index.ts']
const env = {
  ...process.env,
  MM_URL: 'http://127.0.0.1:1',
  MM_TOKEN: 'test-token',
}

describe('send command wiring', () => {
  test.each([
    ['dm', 'alice'],
    ['group', 'group-channel-id'],
  ])('rejects empty stdin for send %s before network access', (...args) => {
    const result = spawnSync('bun', [...entry, 'send', ...args], {
      cwd: process.cwd(),
      env,
      input: '',
      encoding: 'utf8',
    })

    expect(result.status).toBe(1)
    expect(result.stderr).toContain('Error: Message cannot be empty.')
    expect(result.stderr).not.toContain('network error')
  })

  test('documents both send destinations and stdin behavior', () => {
    const root = spawnSync('bun', [...entry, 'send', '--help'], {
      cwd: process.cwd(),
      env,
      encoding: 'utf8',
    })
    const dm = spawnSync('bun', [...entry, 'send', 'dm', '--help'], {
      cwd: process.cwd(),
      env,
      encoding: 'utf8',
    })
    const group = spawnSync('bun', [...entry, 'send', 'group', '--help'], {
      cwd: process.cwd(),
      env,
      encoding: 'utf8',
    })

    expect(root.status).toBe(0)
    expect(root.stdout).toContain('dm [options] <username>')
    expect(root.stdout).toContain('group [options] <channel-id>')
    expect(dm.stdout).toContain('Send stdin')
    expect(dm.stdout).toContain('--dry-run')
    expect(group.stdout).toContain('Send stdin')
    expect(group.stdout).toContain('--dry-run')
  })
})
