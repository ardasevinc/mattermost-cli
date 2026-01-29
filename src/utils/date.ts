// Centralized date formatting utilities

/**
 * Format date as "D Mon" or "D Mon YYYY" (European style)
 */
export function formatDate(date: Date, options?: { includeYear?: boolean }): string {
  return date.toLocaleDateString('en-GB', {
    day: 'numeric',
    month: 'short',
    year: options?.includeYear ? 'numeric' : undefined,
  })
}

/**
 * Format date with full weekday and month names (for markdown headings)
 */
export function formatDateLong(date: Date): string {
  return date.toLocaleDateString('en-GB', {
    weekday: 'long',
    day: 'numeric',
    month: 'long',
    year: 'numeric',
  })
}

/**
 * Format time as HH:MM (24-hour)
 */
export function formatTime(date: Date): string {
  return date.toLocaleTimeString('en-GB', {
    hour: '2-digit',
    minute: '2-digit',
    hour12: false,
  })
}

/**
 * Format as relative time using Intl.RelativeTimeFormat
 * e.g., "2 days ago", "5 minutes ago", "just now"
 */
export function formatRelativeTime(date: Date): string {
  const now = Date.now()
  const diff = date.getTime() - now // negative for past
  const absDiff = Math.abs(diff)

  const rtf = new Intl.RelativeTimeFormat('en', { numeric: 'auto' })

  const minute = 60 * 1000
  const hour = 60 * minute
  const day = 24 * hour
  const week = 7 * day
  const month = 30 * day
  const year = 365 * day

  if (absDiff < minute) {
    return 'just now'
  } else if (absDiff < hour) {
    return rtf.format(Math.round(diff / minute), 'minute')
  } else if (absDiff < day) {
    return rtf.format(Math.round(diff / hour), 'hour')
  } else if (absDiff < week) {
    return rtf.format(Math.round(diff / day), 'day')
  } else if (absDiff < month) {
    return rtf.format(Math.round(diff / week), 'week')
  } else if (absDiff < year) {
    return rtf.format(Math.round(diff / month), 'month')
  } else {
    return rtf.format(Math.round(diff / year), 'year')
  }
}

/**
 * Get date group label for message grouping.
 * Returns "Today", "Yesterday", or formatted date.
 */
export function getDateGroupLabel(date: Date): string {
  const today = new Date()
  const yesterday = new Date(today)
  yesterday.setDate(yesterday.getDate() - 1)

  if (date.toDateString() === today.toDateString()) {
    return 'Today'
  }
  if (date.toDateString() === yesterday.toDateString()) {
    return 'Yesterday'
  }

  return formatDate(date, {
    includeYear: date.getFullYear() !== today.getFullYear(),
  })
}
