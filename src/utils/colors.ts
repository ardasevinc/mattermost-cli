const ansi = {
  reset: '\x1b[0m',
  bold: '\x1b[1m',
  dim: '\x1b[2m',
  italic: '\x1b[3m',
  black: '\x1b[30m',
  red: '\x1b[31m',
  green: '\x1b[32m',
  yellow: '\x1b[33m',
  blue: '\x1b[34m',
  magenta: '\x1b[35m',
  cyan: '\x1b[36m',
  white: '\x1b[37m',
} as const

function colorize(text: string, ...codes: string[]): string {
  return `${codes.join('')}${text}${ansi.reset}`
}

export function bold(text: string): string {
  return colorize(text, ansi.bold)
}

export function dim(text: string): string {
  return colorize(text, ansi.dim)
}

export function cyan(text: string): string {
  return colorize(text, ansi.cyan)
}

export function yellow(text: string): string {
  return colorize(text, ansi.yellow)
}

export function green(text: string): string {
  return colorize(text, ansi.green)
}

export function magenta(text: string): string {
  return colorize(text, ansi.magenta)
}

export function blue(text: string): string {
  return colorize(text, ansi.blue)
}

export function userColor(username: string): string {
  const userColors = [cyan, yellow, green, magenta, blue]
  let hash = 0
  for (const char of username) {
    hash = (hash << 5) - hash + char.charCodeAt(0)
    hash &= hash
  }
  const colorFn = userColors[Math.abs(hash) % userColors.length] ?? cyan
  return colorFn(username)
}
