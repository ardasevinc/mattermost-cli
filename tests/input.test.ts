import { Readable } from 'node:stream'
import { describe, expect, test } from 'vitest'
import { MAX_MESSAGE_BYTES, readMessageInput } from '../src/input'

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

  test('refuses implicit interactive reads', async () => {
    const input = stream('message')
    input.isTTY = true
    await expect(readMessageInput(input)).rejects.toThrow('Message content must be piped on stdin.')
  })
})
