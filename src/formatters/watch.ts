import type { WatchEvent } from '../types'
import { dim, formatTime, userColor } from '../utils'

export function formatWatchJSON(event: WatchEvent): string {
  return JSON.stringify(event)
}

export function formatWatchEvent(event: WatchEvent, color: boolean): string {
  const message = event.message.replace(/\s+/g, ' ').trim() || '[empty message]'
  const time = formatTime(new Date(event.timestamp))
  if (color) return `${dim(`[${time}]`)} ${userColor(event.sender)}: ${message}`
  return `[${time}] ${event.sender}: ${message}`
}
