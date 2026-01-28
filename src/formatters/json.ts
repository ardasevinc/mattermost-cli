// JSON output formatter

import type { DMOutput } from '../types'

export function formatJSON(outputs: DMOutput[]): string {
  return JSON.stringify(outputs, null, 2)
}

export function formatJSONCompact(outputs: DMOutput[]): string {
  return JSON.stringify(outputs)
}
