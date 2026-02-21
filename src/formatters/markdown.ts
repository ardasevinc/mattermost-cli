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
    return `## DMs with ${channel.name}`
  }
  const display = channel.displayName ? ` (${channel.displayName})` : ''
  return `## #${channel.name}${display}`
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

  return lines.join('\n')
}

function formatMessage(msg: ProcessedMessage, relative: boolean, depth: number = 0): string {
  const timeStr = relative ? formatRelativeTime(msg.timestamp) : formatTime(msg.timestamp)
  const lines: string[] = []
  const prefix = depth > 0 ? '> '.repeat(depth) : ''

  lines.push(`${prefix}**${msg.user}** (${timeStr}):`)

  // Quote the message content
  const quotePrefix = `${prefix}> `
  const quotedText = msg.text
    .split('\n')
    .map((line) => `${quotePrefix}${line}`)
    .join('\n')

  lines.push(quotedText)

  // Add file attachments if any
  if (msg.files.length > 0) {
    lines.push(`${quotePrefix}_Attachments: ${msg.files.join(', ')}_`)
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
