import { Readable } from 'node:stream'
import { describe, expect, test } from 'vitest'
import { MAX_MESSAGE_BYTES, MAX_MESSAGE_CHARACTERS, readMessageInput } from '../src/input'
import {
  LONG_MARKDOWN,
  LONG_MARKDOWN_BYTES,
  LONG_MARKDOWN_CHARACTERS,
  SHORT_MARKDOWN,
} from './fixtures/markdown'

function stream(...chunks: Array<Buffer | string>): Readable & { isTTY?: boolean } {
  return Readable.from(chunks)
}

describe('message stdin', () => {
  test('preserves multiline Unicode and its final newline exactly', async () => {
    const message = 'hello 🌍\nsecond line\n'
    await expect(
      readMessageInput(stream('hello ', Buffer.from('🌍\nsecond line\n'))),
    ).resolves.toBe(message)
  })

  test('preserves short structured Markdown exactly', async () => {
    await expect(readMessageInput(stream(SHORT_MARKDOWN))).resolves.toBe(SHORT_MARKDOWN)
  })

  test('preserves long Markdown across arbitrary stream chunks', async () => {
    const bytes = Buffer.from(LONG_MARKDOWN)
    expect(bytes.byteLength).toBe(LONG_MARKDOWN_BYTES)
    expect([...LONG_MARKDOWN].length).toBe(LONG_MARKDOWN_CHARACTERS)
    expect(LONG_MARKDOWN_CHARACTERS).toBeGreaterThanOrEqual(15_500)
    expect(LONG_MARKDOWN_CHARACTERS).toBeLessThan(MAX_MESSAGE_CHARACTERS)
    expect(bytes.byteLength).toBeLessThan(MAX_MESSAGE_BYTES)

    await expect(
      readMessageInput(
        stream(bytes.subarray(0, 17), bytes.subarray(17, 4099), bytes.subarray(4099)),
      ),
    ).resolves.toBe(LONG_MARKDOWN)
  })

  test.each(['', '  \n\t'])('rejects empty or whitespace-only input', async (message) => {
    await expect(readMessageInput(stream(message))).rejects.toThrow('Message cannot be empty.')
  })

  test('rejects invalid UTF-8', async () => {
    await expect(readMessageInput(stream(Buffer.from([0xc3, 0x28])))).rejects.toThrow(
      'Message must be valid UTF-8.',
    )
  })

  test('rejects oversized input while streaming', async () => {
    await expect(
      readMessageInput(stream(Buffer.alloc(MAX_MESSAGE_BYTES), Buffer.from('x'))),
    ).rejects.toThrow(`Message exceeds ${MAX_MESSAGE_BYTES} UTF-8 bytes.`)
  })

  test('accepts the Mattermost character limit exactly', async () => {
    const message = 'a'.repeat(MAX_MESSAGE_CHARACTERS)
    await expect(readMessageInput(stream(message))).resolves.toBe(message)
  })

  test('rejects input above Mattermost character limit even when below its byte limit', async () => {
    const message = 'a'.repeat(MAX_MESSAGE_CHARACTERS + 1)
    expect(Buffer.byteLength(message)).toBeLessThan(MAX_MESSAGE_BYTES)
    await expect(readMessageInput(stream(message))).rejects.toThrow(
      `Message exceeds ${MAX_MESSAGE_CHARACTERS} Unicode characters.`,
    )
  })

  test('refuses implicit interactive reads', async () => {
    const input = stream('message')
    input.isTTY = true
    await expect(readMessageInput(input)).rejects.toThrow('Message content must be piped on stdin.')
  })
})
