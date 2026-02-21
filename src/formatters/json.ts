// JSON output formatter

import type { MessageOutput } from '../types'

export function formatJSON(outputs: MessageOutput[]): string {
  return JSON.stringify(outputs, null, 2)
}

export function formatJSONCompact(outputs: MessageOutput[]): string {
  return JSON.stringify(outputs)
}
