export const MAX_MESSAGE_BYTES = 65_535

export interface MessageInputStream extends AsyncIterable<Uint8Array | string> {
  isTTY?: boolean
}

export async function readMessageInput(stream: MessageInputStream): Promise<string> {
  if (stream.isTTY) throw new Error('Message content must be piped on stdin.')

  const chunks: Uint8Array[] = []
  let bytes = 0
  for await (const chunk of stream) {
    const encoded = typeof chunk === 'string' ? Buffer.from(chunk) : chunk
    bytes += encoded.byteLength
    if (bytes > MAX_MESSAGE_BYTES) {
      throw new Error(`Message exceeds ${MAX_MESSAGE_BYTES} UTF-8 bytes.`)
    }
    chunks.push(encoded)
  }

  let message: string
  try {
    message = new TextDecoder('utf-8', { fatal: true }).decode(Buffer.concat(chunks))
  } catch {
    throw new Error('Message must be valid UTF-8.')
  }
  if (!message.trim()) throw new Error('Message cannot be empty.')
  return message
}
