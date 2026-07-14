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
        updatedAt: new Date('2026-02-21T10:00:00Z'),
        isDeleted: false,
        postType: '',
        isSystem: false,
        isPinned: false,
        files: [],
        fileDetails: [],
        attachments: [],
        reactions: [],
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
  const groupChannel: ProcessedChannel = {
    id: 'ch4',
    type: 'group',
    name: 'Design Crew',
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

    test('group DM header shows its display label without a hash', () => {
      const output = formatPretty([makeOutput(groupChannel)], { color: false })
      expect(output).toContain('Group DM: Design Crew')
      expect(output).not.toContain('#Design Crew')
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

    test('shows current-state markers, file metadata, attachments, and reactions', () => {
      const fixture = makeOutput(publicChannel)
      const message = fixture.messages[0]
      if (!message) throw new Error('missing formatter fixture message')
      message.editedAt = new Date('2026-02-21T10:01:00Z')
      message.isPinned = true
      message.fileDetails = [{ id: 'file1', name: 'report.txt' }]
      message.files = ['file1']
      message.attachments = [
        { title: 'Deploy', titleLink: 'https://example.test/deploy', text: 'Passed' },
      ]
      message.reactions = [
        { emoji: 'white_check_mark', count: 1, actors: [{ id: 'u2', username: 'bob' }] },
      ]

      const output = formatPretty([fixture], { color: false })
      expect(output).toContain('[edited] [pinned]')
      expect(output).toContain('report.txt (file1)')
      expect(output).toContain('Attachment: Deploy')
      expect(output).toContain(':white_check_mark: 1 (bob)')
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
      expect(output).toContain('## #secret\\-stuff')
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

    test('uses safe markdown link destinations for attachment links', () => {
      const fixture = makeOutput(publicChannel)
      const message = fixture.messages[0]
      if (!message) throw new Error('missing formatter fixture message')
      message.isSystem = true
      message.postType = 'system_webhook'
      message.attachments = [{ title: 'Deploy', titleLink: 'https://example.test/a>b\\c' }]
      message.reactions = [{ emoji: 'eyes', count: 1, actors: [{ id: 'u2' }] }]

      const output = formatMarkdown([fixture])
      expect(output).toContain('[system:system\\_webhook]')
      expect(output).toContain('[Deploy](<https://example.test/a%3Eb%5Cc>)')
      expect(output).toContain(':eyes: 1 (u2)')
    })

    test('escapes remote markdown, quotes multiline fields, and rejects unsafe links', () => {
      const fixture = makeOutput(publicChannel)
      const message = fixture.messages[0]
      if (!message) throw new Error('missing formatter fixture message')
      message.user = '[admin](https://evil.test)'
      message.text = 'one\ntwo *bold*'
      message.attachments = [
        {
          title: '[click](https://evil.test)',
          titleLink: 'javascript:alert(1)',
          text: 'first\nsecond',
        },
      ]

      const output = formatMarkdown([fixture])
      expect(output).toContain('**\\[admin\\]\\(https://evil\\.test\\)**')
      expect(output).toContain('> one\n> two \\*bold\\*')
      expect(output).toContain('> first\n> second')
      expect(output).not.toContain('javascript:')
      expect(output).not.toContain('[click](https://evil.test)')
    })

    test('neutralizes block and table markdown from remote text', () => {
      const fixture = makeOutput(publicChannel)
      const message = fixture.messages[0]
      if (!message) throw new Error('missing formatter fixture message')
      message.text = '# heading\n---\n- item\n+ item\n~~strike~~\na | b'

      const output = formatMarkdown([fixture])
      expect(output).toContain('> \\# heading')
      expect(output).toContain('> \\-\\-\\-')
      expect(output).toContain('> \\- item')
      expect(output).toContain('> \\+ item')
      expect(output).toContain('> \\~\\~strike\\~\\~')
      expect(output).toContain('> a \\| b')
    })
  })
})
