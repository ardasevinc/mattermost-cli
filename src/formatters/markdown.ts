// Markdown output formatter

import type { DMOutput, ProcessedMessage } from '../types'

export function formatMarkdown(outputs: DMOutput[]): string {
  const sections: string[] = []

  for (const output of outputs) {
    sections.push(formatDMChannel(output))
  }

  return sections.join('\n\n---\n\n')
}

function formatDMChannel(output: DMOutput): string {
  const { channel, messages } = output
  const lines: string[] = []

  lines.push(`## DMs with @${channel.otherUser}`)
  lines.push('')

  // Group messages by date
  const messagesByDate = groupByDate(messages)

  for (const [date, msgs] of messagesByDate) {
    lines.push(`### ${date}`)
    lines.push('')

    for (const msg of msgs) {
      lines.push(formatMessage(msg))
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

function formatMessage(msg: ProcessedMessage): string {
  const time = formatTime(msg.timestamp)
  const lines: string[] = []

  lines.push(`**${msg.user}** (${time}):`)

  // Quote the message content
  const quotedText = msg.text
    .split('\n')
    .map((line) => `> ${line}`)
    .join('\n')

  lines.push(quotedText)

  // Add file attachments if any
  if (msg.files.length > 0) {
    lines.push(`> _Attachments: ${msg.files.join(', ')}_`)
  }

  return lines.join('\n')
}

function groupByDate(
  messages: ProcessedMessage[]
): Map<string, ProcessedMessage[]> {
  const groups = new Map<string, ProcessedMessage[]>()

  // Sort messages oldest first for display
  const sorted = [...messages].sort(
    (a, b) => a.timestamp.getTime() - b.timestamp.getTime()
  )

  for (const msg of sorted) {
    const date = formatDate(msg.timestamp)
    if (!groups.has(date)) {
      groups.set(date, [])
    }
    groups.get(date)!.push(msg)
  }

  return groups
}

function formatDate(date: Date): string {
  return date.toLocaleDateString('en-US', {
    year: 'numeric',
    month: 'long',
    day: 'numeric',
    weekday: 'long',
  })
}

function formatTime(date: Date): string {
  return date.toLocaleTimeString('en-US', {
    hour: 'numeric',
    minute: '2-digit',
    hour12: true,
  })
}
