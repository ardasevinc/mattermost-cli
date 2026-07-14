import { spawnSync } from 'node:child_process'
import { describe, expect, test } from 'vitest'
import { MAX_MESSAGE_CHARACTERS } from '../../src/input'
import { LONG_MARKDOWN, LONG_MARKDOWN_CHARACTERS, SHORT_MARKDOWN } from '../fixtures/markdown'

const url = process.env.MM_E2E_URL
const token = process.env.MM_E2E_TOKEN
if (!url || !token) throw new Error('Disposable Mattermost E2E credentials are required.')
const parsedUrl = new URL(url)
if (parsedUrl.hostname !== '127.0.0.1' && parsedUrl.hostname !== 'localhost') {
  throw new Error('Refusing to run message-write E2E against a non-loopback Mattermost server.')
}

interface Receipt {
  status: 'dry_run' | 'sent'
  destination: {
    type: 'dm' | 'group'
    label: string
    channelId: string | null
    willCreate: boolean
  }
  post?: { id: string; createAt: string; pendingPostId: string; permalink: string }
}

async function api<T>(path: string, init: RequestInit = {}): Promise<T> {
  const response = await fetch(`${url}/api/v4${path}`, {
    ...init,
    headers: {
      Authorization: `Bearer ${token}`,
      'Content-Type': 'application/json',
      ...init.headers,
    },
  })
  if (!response.ok) throw new Error(`Mattermost E2E API request failed: ${response.status}.`)
  return (await response.json()) as T
}

function cli(args: string[], input?: string): Receipt {
  const result = spawnSync('node', ['dist/index.js', '--json', ...args], {
    cwd: process.cwd(),
    encoding: 'utf8',
    env: { ...process.env, MM_URL: url, MM_TOKEN: token },
    input,
  })
  if (result.status !== 0) {
    throw new Error(`CLI failed (${result.status}): ${result.stderr}`)
  }
  return JSON.parse(result.stdout) as Receipt
}

describe.sequential('disposable Mattermost message sending', () => {
  test('dry-runs and sends short Markdown exactly once to a direct message', async () => {
    const dryRun = cli(['send', 'dm', 'alice', '--dry-run'])
    expect(dryRun).toEqual({
      status: 'dry_run',
      destination: {
        type: 'dm',
        label: '@alice',
        channelId: null,
        willCreate: true,
      },
    })

    const message = `${SHORT_MARKDOWN}\n<!-- ${crypto.randomUUID()} -->\n`
    const sent = cli(['send', 'dm', 'alice'], message)
    expect(sent.status).toBe('sent')
    expect(sent.destination).toMatchObject({
      type: 'dm',
      label: '@alice',
      willCreate: false,
    })
    expect(sent.post?.permalink).toBe(`${url}/_redirect/pl/${sent.post?.id}`)
    expect(sent.post?.pendingPostId).toMatch(/^[0-9a-f-]{36}$/)

    const post = await api<{
      id: string
      channel_id: string
      user_id: string
      message: string
    }>(`/posts/${sent.post?.id}`)
    const me = await api<{ id: string }>('/users/me')
    expect(post).toMatchObject({
      id: sent.post?.id,
      channel_id: sent.destination.channelId,
      user_id: me.id,
      message,
    })

    const page = await api<{ order: string[]; posts: Record<string, { message: string }> }>(
      `/channels/${sent.destination.channelId}/posts?per_page=200`,
    )
    expect(page.order.filter((id) => page.posts[id]?.message === message)).toHaveLength(1)
  })

  test('dry-runs and sends long Markdown exactly once to an existing group conversation', async () => {
    const users = await Promise.all(
      ['sender', 'alice', 'bob'].map((username) =>
        api<{ id: string }>(`/users/username/${username}`),
      ),
    )
    const group = await api<{ id: string }>('/channels/group', {
      method: 'POST',
      body: JSON.stringify(users.map((user) => user.id)),
    })

    const dryRun = cli(['send', 'group', group.id, '--dry-run'])
    expect(dryRun).toMatchObject({
      status: 'dry_run',
      destination: { type: 'group', channelId: group.id, willCreate: false },
    })
    const before = await api<{ order: string[] }>(`/channels/${group.id}/posts?per_page=200`)

    const message = `${LONG_MARKDOWN}\n<!-- ${crypto.randomUUID()} -->\n`
    expect([...message].length).toBeGreaterThan(LONG_MARKDOWN_CHARACTERS)
    expect([...message].length).toBeLessThan(MAX_MESSAGE_CHARACTERS)
    const sent = cli(['send', 'group', group.id], message)
    expect(sent).toMatchObject({
      status: 'sent',
      destination: { type: 'group', channelId: group.id, willCreate: false },
    })

    const post = await api<{ message: string; channel_id: string }>(`/posts/${sent.post?.id}`)
    expect(post).toMatchObject({ channel_id: group.id, message })
    const after = await api<{ order: string[]; posts: Record<string, { message: string }> }>(
      `/channels/${group.id}/posts?per_page=200`,
    )
    expect(after.order).toHaveLength(before.order.length + 1)
    expect(after.order.filter((id) => after.posts[id]?.message === message)).toHaveLength(1)
  })
})
