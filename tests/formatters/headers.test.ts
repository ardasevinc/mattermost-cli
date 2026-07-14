import { describe, expect, test } from 'vitest'
import { formatMarkdown } from '../../src/formatters/markdown'
import { formatPretty } from '../../src/formatters/pretty'
import type { MessageOutput, ProcessedChannel } from '../../src/types'

function makeOutput(channel: ProcessedChannel): MessageOutput {
  return {
    channel,
    messages: [
      {
        id: 'msg1',
        permalink: 'https://mattermost.example.com/_redirect/pl/msg1',
        user: 'alice',
        userId: 'u1',
        text: 'hello',
        timestamp: new Date('2026-02-21T10:00:00Z'),
        files: [],
      },
    ],
    redactions: [],
    retrieval: {
      selection: {
        source: 'recent',
        selectedCount: 1,
        requestedLimit: 1,
        since: null,
        queryTruncated: true,
      },
      visibleThreads: { status: 'not_requested', hydratedRootCount: 0, failedRootIds: [] },
      visiblePostCount: 1,
      deletedPostsIncluded: false,
    },
  }
}

function withTruncation(output: MessageOutput, queryTruncated: boolean | null): MessageOutput {
  return {
    ...output,
    retrieval: {
      ...output.retrieval,
      selection: { ...output.retrieval.selection, queryTruncated },
    },
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

    test('shows a compact post id, permalink, and coverage warning', () => {
      const output = formatPretty([makeOutput(publicChannel)], { color: false })
      expect(output).toContain('msg1 https://mattermost.example.com/_redirect/pl/msg1')
      expect(output).toContain('Coverage: 1 selected, 1 visible; query truncated')
    })

    test.each([
      [false, 'query complete'],
      [null, 'query completeness unknown'],
    ] as const)('reports %j query coverage honestly', (state, wording) => {
      const output = formatPretty([withTruncation(makeOutput(publicChannel), state)], {
        color: false,
      })
      expect(output).toContain(wording)
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

    test('links the stable post id and reports coverage', () => {
      const output = formatMarkdown([makeOutput(publicChannel)])
      expect(output).toContain('[msg1](<https://mattermost.example.com/_redirect/pl/msg1>)')
      expect(output).toContain('Coverage: 1 selected, 1 visible; query truncated')
    })

    test('keeps a permalink with parentheses parseable as one markdown destination', () => {
      const fixture = makeOutput(publicChannel)
      const message = fixture.messages[0]
      if (!message) throw new Error('missing formatter fixture message')
      message.permalink = 'https://mattermost.example.com/chat)/_redirect/pl/post%28special%29'
      expect(formatMarkdown([fixture])).toContain(
        '[msg1](<https://mattermost.example.com/chat)/_redirect/pl/post%28special%29>)',
      )
    })

    test.each([
      [false, 'query complete'],
      [null, 'query completeness unknown'],
    ] as const)('reports %j query coverage honestly', (state, wording) => {
      const output = formatMarkdown([withTruncation(makeOutput(publicChannel), state)])
      expect(output).toContain(wording)
    })
  })
})
