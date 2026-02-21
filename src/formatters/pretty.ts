// Pretty terminal output with ANSI colors

import type { MessageOutput, ProcessedChannel, ProcessedMessage } from '../types'
import { formatRelativeTime, formatTime, getDateGroupLabel } from '../utils/date'

// ANSI color codes
const colors = {
  reset: '\x1b[0m',
  bold: '\x1b[1m',
  dim: '\x1b[2m',
  italic: '\x1b[3m',

  // Foreground
  black: '\x1b[30m',
  red: '\x1b[31m',
  green: '\x1b[32m',
  yellow: '\x1b[33m',
  blue: '\x1b[34m',
  magenta: '\x1b[35m',
  cyan: '\x1b[36m',
  white: '\x1b[37m',

  // Background
  bgBlack: '\x1b[40m',
  bgRed: '\x1b[41m',
  bgGreen: '\x1b[42m',
  bgYellow: '\x1b[43m',
  bgBlue: '\x1b[44m',
  bgMagenta: '\x1b[45m',
  bgCyan: '\x1b[46m',
  bgWhite: '\x1b[47m',
}

// Simple color helpers
function c(text: string, ...codes: string[]): string {
  return `${codes.join('')}${text}${colors.reset}`
}

function bold(text: string): string {
  return c(text, colors.bold)
}

function dim(text: string): string {
  return c(text, colors.dim)
}

function cyan(text: string): string {
  return c(text, colors.cyan)
}

function yellow(text: string): string {
  return c(text, colors.yellow)
}

function green(text: string): string {
  return c(text, colors.green)
}

function magenta(text: string): string {
  return c(text, colors.magenta)
}

function blue(text: string): string {
  return c(text, colors.blue)
}

// Generate consistent color for username
function userColor(username: string): string {
  const userColors = [cyan, yellow, green, magenta, blue]
  let hash = 0
  for (const char of username) {
    hash = (hash << 5) - hash + char.charCodeAt(0)
    hash = hash & hash
  }
  const colorFn = userColors[Math.abs(hash) % userColors.length] ?? cyan
  return colorFn(username)
}

function channelHeader(channel: ProcessedChannel): string {
  if (channel.type === 'dm') {
    return `💬 DMs with ${cyan(channel.name)}`
  }
  const name = `#${channel.name}`
  const display = channel.displayName ? ` (${channel.displayName})` : ''
  return `📢 ${cyan(name)}${display}`
}

function channelHeaderPlain(channel: ProcessedChannel): string {
  if (channel.type === 'dm') {
    return `DMs with ${channel.name}`
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

  const lines: string[] = []
  lines.push(`${indent}${time} ${bold(user)}`)

  // Indent message content
  const textIndent = `${indent}  `
  const indentedText = msg.text
    .split('\n')
    .map((line) => `${textIndent}${line}`)
    .join('\n')
  lines.push(indentedText)

  // File attachments
  if (msg.files.length > 0) {
    lines.push(dim(`${textIndent}📎 ${msg.files.join(', ')}`))
  }

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
  lines.push(`${indent}[${timeStr}] ${msg.user}`)
  const textIndent = `${indent}  `
  const indentedText = msg.text
    .split('\n')
    .map((line) => `${textIndent}${line}`)
    .join('\n')
  lines.push(indentedText)
  if (msg.files.length > 0) {
    lines.push(`${textIndent}Attachments: ${msg.files.join(', ')}`)
  }
  lines.push('')

  if (msg.replies && msg.replies.length > 0) {
    for (const reply of msg.replies) {
      formatMessageNoColor(reply, relative, lines, `${indent}  > `)
    }
  }
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
