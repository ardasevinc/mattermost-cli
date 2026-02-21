import { describe, expect, test } from 'bun:test'
import type { MessageOutput, ProcessedChannel } from '../../src/types'
import { formatPretty } from '../../src/formatters/pretty'
import { formatMarkdown } from '../../src/formatters/markdown'

function makeOutput(channel: ProcessedChannel): MessageOutput {
  return {
    channel,
    messages: [
      {
        id: 'msg1',
        user: 'alice',
        userId: 'u1',
        text: 'hello',
        timestamp: new Date('2026-02-21T10:00:00Z'),
        files: [],
      },
    ],
    redactions: [],
  }
}

describe('formatter channel headers', () => {
  const dmChannel: ProcessedChannel = { id: 'ch1', type: 'dm', name: '@bob' }
  const publicChannel: ProcessedChannel = {
    id: 'ch2',
    type: 'public',
    name: 'general',
    displayName: 'General',
  }
  const privateChannel: ProcessedChannel = {
    id: 'ch3',
    type: 'private',
    name: 'secret-stuff',
  }

  describe('pretty formatter (no color)', () => {
    test('DM header shows "DMs with @user"', () => {
      const output = formatPretty([makeOutput(dmChannel)], { color: false })
      expect(output).toContain('DMs with @bob')
    })

    test('public channel header shows "#channel (DisplayName)"', () => {
      const output = formatPretty([makeOutput(publicChannel)], { color: false })
      expect(output).toContain('#general (General)')
    })

    test('private channel header shows "#channel" without display name', () => {
      const output = formatPretty([makeOutput(privateChannel)], { color: false })
      expect(output).toContain('#secret-stuff')
      expect(output).not.toContain('undefined')
    })
  })

  describe('markdown formatter', () => {
    test('DM header uses ## DMs with @user', () => {
      const output = formatMarkdown([makeOutput(dmChannel)])
      expect(output).toContain('## DMs with @bob')
    })

    test('public channel header uses ## #channel (DisplayName)', () => {
      const output = formatMarkdown([makeOutput(publicChannel)])
      expect(output).toContain('## #general (General)')
    })

    test('private channel header omits display name when absent', () => {
      const output = formatMarkdown([makeOutput(privateChannel)])
      expect(output).toContain('## #secret-stuff')
      expect(output).not.toContain('undefined')
    })
  })
})
