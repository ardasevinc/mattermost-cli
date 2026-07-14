import { describe, expect, test } from 'vitest'
import { assertSecureServerUrl, normalizeServerUrl } from '../../src/api/url'

describe('assertSecureServerUrl', () => {
  test.each([
    'https://mattermost.example.com',
    'http://localhost:8065',
    'http://127.0.0.1:8065',
    'http://127.0.0.2:8065',
    'http://[::1]:8065',
  ])('allows secure or loopback URL %s', (url) => {
    expect(() => assertSecureServerUrl(url)).not.toThrow()
  })

  test.each([
    'http://mattermost.example.com',
    'http://localhost.evil:8065',
    'http://127.evil:8065',
  ])('rejects remote plaintext HTTP URL %s', (url) => {
    expect(() => assertSecureServerUrl(url)).toThrow(
      'Refusing to send a Mattermost token over plaintext HTTP',
    )
  })

  test('rejects unsupported URL protocols', () => {
    expect(() => assertSecureServerUrl('ftp://mattermost.example.com')).toThrow(
      'Mattermost URL must use HTTPS',
    )
  })

  test.each([
    'https://user:password@mattermost.example.com',
    'https://mattermost.example.com?token=secret',
    'https://mattermost.example.com#fragment',
  ])('rejects ambiguous or credential-bearing URL %s', (url) => {
    expect(() => assertSecureServerUrl(url)).toThrow('Invalid Mattermost URL')
  })

  test('rejects malformed URLs without echoing them', () => {
    const malformed = 'not a url with secret-token-value'

    expect(() => assertSecureServerUrl(malformed)).toThrow('Invalid Mattermost URL')
    expect(() => assertSecureServerUrl(malformed)).not.toThrow(malformed)
  })
})

describe('normalizeServerUrl', () => {
  test('canonicalizes casing, surrounding whitespace, and trailing slashes', () => {
    expect(normalizeServerUrl('  HTTPS://Mattermost.Example.Com///  ')).toBe(
      'https://mattermost.example.com',
    )
  })

  test('preserves a Mattermost subpath', () => {
    expect(normalizeServerUrl('https://mattermost.example.com/chat/')).toBe(
      'https://mattermost.example.com/chat',
    )
  })
})
