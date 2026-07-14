import { describe, expect, test } from 'vitest'
import { normalizePosts, postUserIds } from '../../src/preprocessing/post'
import type { Post, User } from '../../src/types'

const token = `ghp_${'a'.repeat(36)}`

function makePost(overrides: Partial<Post> = {}): Post {
  return {
    id: 'post-1',
    create_at: 1000,
    update_at: 3000,
    edit_at: 2000,
    delete_at: 0,
    user_id: 'author',
    channel_id: 'channel',
    message: 'latest visible text',
    type: '',
    props: {},
    hashtags: '',
    root_id: '',
    reply_count: 0,
    file_ids: [],
    pending_post_id: '',
    ...overrides,
  }
}

function normalize(post: Post, redact = true) {
  const users = new Map<string, User>([
    ['author', { id: 'author', username: 'alice' } as User],
    ['reactor', { id: 'reactor', username: 'bob' } as User],
  ])
  return normalizePosts(
    [post],
    users,
    'me',
    'https://mattermost.test',
    (_, id) => `/p/${id}`,
    redact,
  )
}

describe('post normalization', () => {
  test('represents the latest visible edited state', () => {
    const { messages } = normalize(makePost())
    expect(messages[0]).toMatchObject({
      text: 'latest visible text',
      updatedAt: new Date(3000),
      editedAt: new Date(2000),
      isDeleted: false,
    })
  })

  test('never leaks stale content from a returned deleted post', () => {
    const { messages } = normalize(
      makePost({
        delete_at: 4000,
        message: `stale ${token}`,
        file_ids: ['secret-file'],
        props: { attachments: [{ text: `stale ${token}` }] },
        metadata: {
          files: [
            {
              id: 'secret-file',
              name: `stale-${token}`,
              extension: 'txt',
              size: 1,
              mime_type: 'text/plain',
            },
          ],
          reactions: [{ user_id: 'reactor', post_id: 'post-1', emoji_name: 'yes', create_at: 1 }],
        },
      }),
    )
    expect(messages[0]).toMatchObject({
      text: '[deleted post]',
      isDeleted: true,
      files: [],
      fileDetails: [],
      attachments: [],
      reactions: [],
    })
    expect(JSON.stringify(messages[0])).not.toContain(token)
  })

  test('uses system for an empty author without requesting an empty user id', () => {
    const post = makePost({ user_id: '', type: 'system_join_channel' })
    expect(postUserIds([post])).toEqual([])
    expect(normalize(post).messages[0]).toMatchObject({
      user: 'system',
      userId: '',
      isSystem: true,
      postType: 'system_join_channel',
    })
  })

  test('sanitizes and redacts webhook, files, attachments, urls, emoji, and reaction users', () => {
    const post = makePost({
      override_username: `hook\u001b ${token}`,
      file_ids: ['file-1'],
      props: {
        attachments: [
          {
            title: `title ${token}`,
            title_link: `https://example.test/${token}>\u001b`,
            text: `body ${token}`,
            fields: [{ title: `field ${token}`, value: `value ${token}` }],
            footer: `footer ${token}`,
            author_name: `author ${token}`,
            image_url: `https://example.test/${token}`,
          },
        ],
      },
      metadata: {
        files: [
          {
            id: 'file-1',
            name: `name\n${token}`,
            extension: `txt\u001b${token}`,
            size: 12,
            mime_type: `text/plain\u001b${token}`,
          },
        ],
        reactions: [
          { user_id: 'reactor', post_id: 'post-1', emoji_name: `yes\u001b${token}`, create_at: 1 },
          { user_id: 'missing', post_id: 'post-1', emoji_name: `yes\u001b${token}`, create_at: 2 },
        ],
      },
    })
    const { messages, redactions } = normalize(post)
    const encoded = JSON.stringify(messages[0])
    expect(messages[0]?.user).toContain('hook\\u001b')
    expect(messages[0]?.userId).toBe('author')
    expect(messages[0]?.fileDetails[0]?.name).toContain('name\\n')
    expect(messages[0]?.reactions[0]).toMatchObject({ count: 2 })
    expect(messages[0]?.reactions[0]?.actors).toEqual([
      { id: 'missing' },
      { id: 'reactor', username: 'bob' },
    ])
    expect(redactions.length).toBeGreaterThan(8)
    expect(encoded).not.toContain(token)
    expect(encoded).not.toContain('\u001b')
    expect(JSON.stringify({ messages, redactions })).not.toMatch(/original(?:Text)?/)
  })

  test('--no-redact retains secrets but still makes controls harmless everywhere', () => {
    const { messages, redactions } = normalize(
      makePost({
        message: `${token}\u001b`,
        override_username: `hook\u001b`,
        props: { attachments: [{ text: `body\u001b${token}` }] },
      }),
      false,
    )
    const encoded = JSON.stringify(messages[0])
    expect(encoded).toContain(token)
    expect(encoded).not.toContain('\u001b')
    expect(redactions).toEqual([])
  })

  test('collects author and reaction actor ids once', () => {
    expect(
      postUserIds([
        makePost({
          metadata: {
            reactions: [
              { user_id: 'reactor', post_id: 'post-1', emoji_name: 'yes', create_at: 1 },
              { user_id: 'reactor', post_id: 'post-1', emoji_name: 'no', create_at: 2 },
            ],
          },
        }),
      ]),
    ).toEqual(['author', 'reactor'])
  })

  test('tolerates malformed attachments and accepts numeric fields and timestamps', () => {
    const { messages } = normalize(
      makePost({
        props: {
          attachments: [
            null,
            42,
            { fields: 'bad' },
            { pretext: 'before', fields: [null, { title: 7, value: 9 }], ts: 123 },
          ],
        },
      }),
    )
    expect(messages[0]?.attachments).toEqual([
      {
        pretext: 'before',
        fields: [{ title: '7', value: '9' }],
        timestamp: '123',
      },
    ])
  })

  test('keeps canonically distinct reactions separate after masked display collisions', () => {
    const first = `ghp_${'a'.repeat(36)}`
    const second = `ghp_${'b'.repeat(36)}`
    const { messages } = normalize(
      makePost({
        metadata: {
          reactions: [
            { user_id: 'reactor', post_id: 'post-1', emoji_name: second, create_at: 2 },
            { user_id: 'missing', post_id: 'post-1', emoji_name: first, create_at: 1 },
          ],
        },
      }),
    )
    expect(messages[0]?.reactions).toHaveLength(2)
    expect(messages[0]?.reactions.map(({ count }) => count)).toEqual([1, 1])
  })

  test('sanitizes every presentation id and records field provenance', () => {
    const { messages, redactions } = normalize(
      makePost({
        id: `reply\u001b${token}`,
        root_id: `root\u001b${token}`,
        user_id: `user\u001b${token}`,
        file_ids: [`file\u001b${token}`],
      }),
    )
    const encoded = JSON.stringify(messages[0])
    expect(encoded).not.toContain(token)
    expect(encoded).not.toContain('\u001b')
    expect(redactions.map(({ field }) => field)).toEqual(
      expect.arrayContaining([
        'post.id',
        'post.userId',
        'post.rootId',
        'file.id',
        'post.permalink',
      ]),
    )
  })
})
