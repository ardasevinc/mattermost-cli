// Markdown output formatter

import type { MessageOutput, ProcessedChannel, ProcessedMessage } from '../types'
import { formatDateLong, formatRelativeTime, formatTime } from '../utils/date'

export interface MarkdownOptions {
  relative?: boolean
}

export function formatMarkdown(outputs: MessageOutput[], options: MarkdownOptions = {}): string {
  const sections: string[] = []

  for (const output of outputs) {
    sections.push(formatChannelMarkdown(output, options.relative ?? false))
  }

  return sections.join('\n\n---\n\n')
}

function channelHeaderMarkdown(channel: ProcessedChannel): string {
  if (channel.type === 'dm') {
    return `## DMs with ${escapeMarkdown(channel.name)}`
  }
  if (channel.type === 'group') {
    return `## Group DM: ${escapeMarkdown(channel.name)}`
  }
  const display = channel.displayName ? ` (${escapeMarkdown(channel.displayName)})` : ''
  return `## #${escapeMarkdown(channel.name)}${display}`
}

function formatChannelMarkdown(output: MessageOutput, relative: boolean): string {
  const { channel, messages } = output
  const lines: string[] = []

  lines.push(channelHeaderMarkdown(channel))
  lines.push('')

  // Group messages by date
  const messagesByDate = groupByDate(messages)

  for (const [date, msgs] of messagesByDate) {
    lines.push(`### ${date}`)
    lines.push('')

    for (const msg of msgs) {
      lines.push(formatMessage(msg, relative))
      lines.push('')
    }
  }

  // Add redaction summary if any
  if (output.redactions.length > 0) {
    lines.push('')
    lines.push(`_${output.redactions.length} secret(s) redacted_`)
  }

  const state = output.retrieval.selection.queryTruncated
  lines.push('')
  lines.push(
    `_Coverage: ${output.retrieval.selection.selectedCount} selected, ${output.retrieval.visiblePostCount} visible; query ${state === true ? 'truncated' : state === false ? 'complete' : 'completeness unknown'}_`,
  )
  if (output.retrieval.selection.nextCursor) {
    lines.push(`Next cursor: \`${output.retrieval.selection.nextCursor}\``)
  }

  return lines.join('\n')
}

function formatMessage(msg: ProcessedMessage, relative: boolean, depth: number = 0): string {
  const timeStr = relative ? formatRelativeTime(msg.timestamp) : formatTime(msg.timestamp)
  const lines: string[] = []
  const prefix = depth > 0 ? '> '.repeat(depth) : ''

  const safePermalink = safeHttpUrl(msg.permalink)
  const postId = escapeMarkdown(msg.id)
  const postRef = safePermalink ? `[${postId}](<${safePermalink}>)` : postId
  const markers = messageMarkers(msg)
  lines.push(
    `${prefix}**${escapeMarkdown(msg.user)}**${markers ? ` ${markers}` : ''} (${timeStr}, ${postRef}):`,
  )

  // Quote the message content
  const quotePrefix = `${prefix}> `
  lines.push(quoteLines(escapeMarkdown(msg.text), quotePrefix))
  const stateParts = [`Updated ${msg.updatedAt.toISOString()}`]
  if (msg.editedAt) stateParts.push(`edited ${msg.editedAt.toISOString()}`)
  if (msg.deletedAt) stateParts.push(`deleted ${msg.deletedAt.toISOString()}`)
  lines.push(`${quotePrefix}_${stateParts.join('; ')}_`)

  // Add file attachments if any
  if (msg.fileDetails.length > 0) {
    const files = msg.fileDetails
      .map((file) => {
        const label = file.name ? `${file.name} (${file.id})` : file.id
        const details = [
          file.mime,
          file.extension,
          file.size === undefined ? undefined : `${file.size} B`,
        ]
          .filter(Boolean)
          .join(', ')
        return escapeMarkdown(`${label}${details ? `, ${details}` : ''}`)
      })
      .join(', ')
    lines.push(`${quotePrefix}_Files: ${files}_`)
  }

  for (const attachment of msg.attachments) {
    const title = attachment.title ?? 'Attachment'
    const safeTitleLink = attachment.titleLink ? safeHttpUrl(attachment.titleLink) : undefined
    if (attachment.pretext) lines.push(quoteLines(escapeMarkdown(attachment.pretext), quotePrefix))
    lines.push(
      safeTitleLink
        ? `${quotePrefix}**[${escapeMarkdown(title)}](<${safeTitleLink}>)**`
        : `${quotePrefix}**${escapeMarkdown(title)}**`,
    )
    if (attachment.authorName)
      lines.push(quoteLines(`By: ${escapeMarkdown(attachment.authorName)}`, quotePrefix))
    if (attachment.text) lines.push(quoteLines(escapeMarkdown(attachment.text), quotePrefix))
    for (const field of attachment.fields ?? []) {
      lines.push(
        quoteLines(
          `${field.title ? `**${escapeMarkdown(field.title)}:** ` : ''}${escapeMarkdown(field.value ?? '')}`,
          quotePrefix,
        ),
      )
    }
    if (attachment.fallback)
      lines.push(quoteLines(`Fallback: ${escapeMarkdown(attachment.fallback)}`, quotePrefix))
    if (attachment.footer)
      lines.push(quoteLines(`_${escapeMarkdown(attachment.footer)}_`, quotePrefix))
    if (attachment.color) lines.push(`${quotePrefix}Color: ${escapeMarkdown(attachment.color)}`)
    if (attachment.timestamp)
      lines.push(`${quotePrefix}Timestamp: ${escapeMarkdown(attachment.timestamp)}`)
    for (const url of [
      attachment.authorLink,
      attachment.authorIcon,
      attachment.footerIcon,
      attachment.imageUrl,
      attachment.thumbUrl,
    ]) {
      const safeUrl = url ? safeHttpUrl(url) : undefined
      if (safeUrl) lines.push(`${quotePrefix}<${safeUrl}>`)
    }
  }

  if (msg.reactions.length > 0) {
    const reactions = msg.reactions
      .map(({ emoji, count, actors }) => {
        const names = actors.map((actor) => escapeMarkdown(actor.username ?? actor.id)).join(', ')
        return `:${escapeMarkdown(emoji)}: ${count}${names ? ` (${names})` : ''}`
      })
      .join(' · ')
    lines.push(`${quotePrefix}_Reactions: ${reactions}_`)
  }

  // Render replies
  if (msg.replies && msg.replies.length > 0) {
    lines.push('')
    for (const reply of msg.replies) {
      lines.push(formatMessage(reply, relative, depth + 1))
      lines.push('')
    }
  }

  return lines.join('\n')
}

function messageMarkers(msg: ProcessedMessage): string {
  const markers: string[] = []
  if (msg.isDeleted) markers.push('[deleted]')
  else if (msg.editedAt) markers.push('[edited]')
  if (msg.isSystem)
    markers.push(msg.postType ? `[system:${escapeMarkdown(msg.postType)}]` : '[system]')
  if (msg.isPinned) markers.push('[pinned]')
  return markers.join(' ')
}

function escapeMarkdown(value: string): string {
  return value.replace(/([\\`*_[\]{}()<>#+\-.!|~])/g, '\\$1')
}

function quoteLines(value: string, prefix: string): string {
  return value
    .split('\n')
    .map((line) => `${prefix}${line}`)
    .join('\n')
}

function safeHttpUrl(url: string): string | undefined {
  try {
    const parsed = new URL(url)
    if (parsed.protocol !== 'http:' && parsed.protocol !== 'https:') return undefined
    return url.replace(/</g, '%3C').replace(/>/g, '%3E').replace(/\\/g, '%5C')
  } catch {
    return undefined
  }
}

function groupByDate(messages: ProcessedMessage[]): Map<string, ProcessedMessage[]> {
  const groups = new Map<string, ProcessedMessage[]>()

  // Sort messages oldest first for display
  const sorted = [...messages].sort((a, b) => a.timestamp.getTime() - b.timestamp.getTime())

  for (const msg of sorted) {
    const date = formatDateLong(msg.timestamp)
    if (!groups.has(date)) {
      groups.set(date, [])
    }
    groups.get(date)?.push(msg)
  }

  return groups
}
