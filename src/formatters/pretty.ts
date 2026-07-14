// Pretty terminal output with ANSI colors

import type { MessageOutput, ProcessedChannel, ProcessedMessage } from '../types'
import { bold, cyan, dim, userColor } from '../utils/colors'
import { formatRelativeTime, formatTime, getDateGroupLabel } from '../utils/date'

function channelHeader(channel: ProcessedChannel): string {
  if (channel.type === 'dm') {
    return `💬 DMs with ${cyan(channel.name)}`
  }
  if (channel.type === 'group') {
    return `💬 Group DM: ${cyan(channel.name)}`
  }
  const name = `#${channel.name}`
  const display = channel.displayName ? ` (${channel.displayName})` : ''
  return `📢 ${cyan(name)}${display}`
}

function channelHeaderPlain(channel: ProcessedChannel): string {
  if (channel.type === 'dm') {
    return `DMs with ${channel.name}`
  }
  if (channel.type === 'group') {
    return `Group DM: ${channel.name}`
  }
  const display = channel.displayName ? ` (${channel.displayName})` : ''
  return `#${channel.name}${display}`
}

export interface PrettyOptions {
  color?: boolean
  relative?: boolean
}

export function formatPretty(
  outputs: MessageOutput[],
  options: PrettyOptions | boolean = true,
): string {
  // Handle legacy boolean parameter
  const opts: PrettyOptions = typeof options === 'boolean' ? { color: options } : options
  const useColor = opts.color ?? true
  const relative = opts.relative ?? false

  if (!useColor) {
    return formatPrettyNoColor(outputs, relative)
  }

  const sections: string[] = []

  for (const output of outputs) {
    sections.push(formatChannelPretty(output, relative))
  }

  return sections.join(`\n${dim('─'.repeat(60))}\n\n`)
}

function formatChannelPretty(output: MessageOutput, relative: boolean): string {
  const { channel, messages } = output
  const lines: string[] = []

  // Header
  lines.push(bold(channelHeader(channel)))
  lines.push('')

  // Group messages by date
  const messagesByDate = groupByDate(messages)

  for (const [date, msgs] of messagesByDate) {
    lines.push(dim(`  ── ${date} ──`))
    lines.push('')

    for (const msg of msgs) {
      lines.push(formatMessagePretty(msg, relative))
    }
  }

  // Redaction notice
  if (output.redactions.length > 0) {
    lines.push('')
    lines.push(dim(`  ⚠ ${output.redactions.length} secret(s) redacted`))
  }
  appendCoverage(lines, output)

  return lines.join('\n')
}

function formatMessagePretty(
  msg: ProcessedMessage,
  relative: boolean,
  indent: string = '  ',
): string {
  const timeStr = relative ? formatRelativeTime(msg.timestamp) : formatTime(msg.timestamp)
  const time = dim(timeStr)
  const user = userColor(msg.user)
  const markers = messageMarkers(msg)

  const lines: string[] = []
  lines.push(
    `${indent}${time} ${bold(user)}${markers ? ` ${dim(markers)}` : ''} ${dim(compactPostRef(msg))}`,
  )

  // Indent message content
  const textIndent = `${indent}  `
  const indentedText = msg.text
    .split('\n')
    .map((line) => `${textIndent}${line}`)
    .join('\n')
  lines.push(indentedText)
  lines.push(dim(`${textIndent}${formatStateTimes(msg)}`))

  // File attachments
  if (msg.fileDetails.length > 0) {
    lines.push(dim(`${textIndent}📎 ${formatFiles(msg)}`))
  }
  appendRichContent(lines, msg, textIndent, (value) => dim(value))

  lines.push('')

  // Render replies
  if (msg.replies && msg.replies.length > 0) {
    for (const reply of msg.replies) {
      lines.push(formatMessagePretty(reply, relative, `${indent}  ↳ `))
    }
  }

  return lines.join('\n')
}

function formatPrettyNoColor(outputs: MessageOutput[], relative: boolean): string {
  const sections: string[] = []

  for (const output of outputs) {
    const { channel, messages } = output
    const lines: string[] = []

    lines.push(channelHeaderPlain(channel))
    lines.push('─'.repeat(40))

    const messagesByDate = groupByDate(messages)

    for (const [date, msgs] of messagesByDate) {
      lines.push(`  -- ${date} --`)
      lines.push('')

      for (const msg of msgs) {
        formatMessageNoColor(msg, relative, lines, '  ')
      }
    }

    if (output.redactions.length > 0) {
      lines.push(`  [${output.redactions.length} secret(s) redacted]`)
    }
    appendCoverage(lines, output)

    sections.push(lines.join('\n'))
  }

  return sections.join(`\n${'='.repeat(60)}\n\n`)
}

function formatMessageNoColor(
  msg: ProcessedMessage,
  relative: boolean,
  lines: string[],
  indent: string,
): void {
  const timeStr = relative ? formatRelativeTime(msg.timestamp) : formatTime(msg.timestamp)
  const markers = messageMarkers(msg)
  lines.push(
    `${indent}[${timeStr}] ${msg.user}${markers ? ` ${markers}` : ''} ${compactPostRef(msg)}`,
  )
  const textIndent = `${indent}  `
  const indentedText = msg.text
    .split('\n')
    .map((line) => `${textIndent}${line}`)
    .join('\n')
  lines.push(indentedText)
  lines.push(`${textIndent}${formatStateTimes(msg)}`)
  if (msg.fileDetails.length > 0) {
    lines.push(`${textIndent}Files: ${formatFiles(msg)}`)
  }
  appendRichContent(lines, msg, textIndent, (value) => value)
  lines.push('')

  if (msg.replies && msg.replies.length > 0) {
    for (const reply of msg.replies) {
      formatMessageNoColor(reply, relative, lines, `${indent}  > `)
    }
  }
}

function compactPostRef(msg: ProcessedMessage): string {
  const id = msg.id.length > 8 ? msg.id.slice(0, 8) : msg.id
  return `${id} ${msg.permalink}`
}

function messageMarkers(msg: ProcessedMessage): string {
  const markers: string[] = []
  if (msg.isDeleted) markers.push('[deleted]')
  else if (msg.editedAt) markers.push('[edited]')
  if (msg.isSystem) markers.push(msg.postType ? `[system:${msg.postType}]` : '[system]')
  if (msg.isPinned) markers.push('[pinned]')
  return markers.join(' ')
}

function formatFiles(msg: ProcessedMessage): string {
  return msg.fileDetails
    .map((file) => {
      const label = file.name ? `${file.name} (${file.id})` : file.id
      const details = [
        file.mime,
        file.extension,
        file.size === undefined ? undefined : `${file.size} B`,
      ]
        .filter(Boolean)
        .join(', ')
      return `${label}${details ? `, ${details}` : ''}`
    })
    .join(', ')
}

function formatStateTimes(msg: ProcessedMessage): string {
  const values = [`Updated ${msg.updatedAt.toISOString()}`]
  if (msg.editedAt) values.push(`edited ${msg.editedAt.toISOString()}`)
  if (msg.deletedAt) values.push(`deleted ${msg.deletedAt.toISOString()}`)
  return values.join('; ')
}

function appendRichContent(
  lines: string[],
  msg: ProcessedMessage,
  indent: string,
  decorate: (value: string) => string,
): void {
  for (const attachment of msg.attachments) {
    if (attachment.pretext) lines.push(decorate(`${indent}${attachment.pretext}`))
    if (attachment.title) lines.push(decorate(`${indent}Attachment: ${attachment.title}`))
    if (attachment.titleLink) lines.push(decorate(`${indent}  Link: ${attachment.titleLink}`))
    if (attachment.authorName) lines.push(decorate(`${indent}  By: ${attachment.authorName}`))
    if (attachment.text) lines.push(decorate(`${indent}  ${attachment.text}`))
    for (const field of attachment.fields ?? []) {
      lines.push(
        decorate(`${indent}  ${field.title ? `${field.title}: ` : ''}${field.value ?? ''}`),
      )
    }
    if (attachment.footer) lines.push(decorate(`${indent}  ${attachment.footer}`))
    if (attachment.fallback) lines.push(decorate(`${indent}  Fallback: ${attachment.fallback}`))
    if (attachment.color) lines.push(decorate(`${indent}  Color: ${attachment.color}`))
    if (attachment.timestamp) lines.push(decorate(`${indent}  Timestamp: ${attachment.timestamp}`))
    for (const url of [
      attachment.authorLink,
      attachment.authorIcon,
      attachment.footerIcon,
      attachment.imageUrl,
      attachment.thumbUrl,
    ]) {
      if (url) lines.push(decorate(`${indent}  ${url}`))
    }
  }
  if (msg.reactions.length > 0) {
    const reactions = msg.reactions
      .map(({ emoji, count, actors }) => {
        const names = actors.map((actor) => actor.username ?? actor.id).join(', ')
        return `:${emoji}: ${count}${names ? ` (${names})` : ''}`
      })
      .join('  ')
    lines.push(decorate(`${indent}Reactions: ${reactions}`))
  }
}

function appendCoverage(lines: string[], output: MessageOutput): void {
  const state = output.retrieval.selection.queryTruncated
  lines.push(
    `  Coverage: ${output.retrieval.selection.selectedCount} selected, ${output.retrieval.visiblePostCount} visible; query ${state === true ? 'truncated' : state === false ? 'complete' : 'completeness unknown'}`,
  )
}

function groupByDate(messages: ProcessedMessage[]): Map<string, ProcessedMessage[]> {
  const groups = new Map<string, ProcessedMessage[]>()

  const sorted = [...messages].sort((a, b) => a.timestamp.getTime() - b.timestamp.getTime())

  for (const msg of sorted) {
    const date = getDateGroupLabel(msg.timestamp)
    if (!groups.has(date)) {
      groups.set(date, [])
    }
    groups.get(date)?.push(msg)
  }

  return groups
}
