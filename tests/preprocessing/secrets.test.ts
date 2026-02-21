import { test, expect, describe } from 'bun:test'
import { detectSecrets, maskSecret, redactSecrets } from '../../src/preprocessing/secrets'

describe('detectSecrets', () => {
  test('detects GitHub tokens', () => {
    // GitHub tokens have ghp_ prefix + 36+ chars
    const token = 'ghp_abcdefghijklmnopqrstuvwxyz1234567890'
    const text = `My token is ${token}`
    const secrets = detectSecrets(text)
    const first = secrets.at(0)

    expect(secrets.length).toBe(1)
    expect(first?.type).toBe('github_token')
    expect(first?.value).toBe(token)
  })

  test('detects AWS access keys', () => {
    const text = 'AWS key: AKIAIOSFODNN7EXAMPLE'
    const secrets = detectSecrets(text)
    const first = secrets.at(0)

    expect(secrets.length).toBe(1)
    expect(first?.type).toBe('aws_access_key')
    expect(first?.value).toBe('AKIAIOSFODNN7EXAMPLE')
  })

  test('detects JWTs', () => {
    const jwt =
      'eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9.eyJzdWIiOiIxMjM0NTY3ODkwIn0.dozjgNryP4J3jVmNHl0w5N_XgL0n3I9PlFUP0THsR8U'
    const text = `Bearer ${jwt}`
    const secrets = detectSecrets(text)

    expect(secrets.some((s) => s.type === 'jwt')).toBe(true)
  })

  test('detects connection strings', () => {
    const text = 'DATABASE_URL=postgres://user:password123@localhost:5432/db'
    const secrets = detectSecrets(text)

    expect(secrets.some((s) => s.type === 'connection_string')).toBe(true)
  })

  test('detects Slack tokens', () => {
    const text = 'SLACK_TOKEN=xoxb-123456789012-1234567890123-abcdefghijklmnopqrstuvwx'
    const secrets = detectSecrets(text)

    expect(secrets.some((s) => s.type === 'slack_token')).toBe(true)
  })

  test('detects newly added provider token patterns', () => {
    const githubPat = 'github_pat_' + 'a'.repeat(22)
    const openaiProject = 'sk-proj-' + 'A'.repeat(32)
    const anthropic = 'sk-ant-' + 'b'.repeat(32)
    const google = 'AIza' + 'c'.repeat(35)
    const text = `${githubPat} ${openaiProject} ${anthropic} ${google}`
    const secrets = detectSecrets(text)
    const types = new Set(secrets.map((s) => s.type))

    expect(types.has('github_fine_grained_token')).toBe(true)
    expect(types.has('openai_project_key')).toBe(true)
    expect(types.has('anthropic_key')).toBe(true)
    expect(types.has('google_api_key')).toBe(true)
  })

  test('uses captured-group start position for labeled tokens', () => {
    const token = 'abcdefghijklmnopqrstuvwxyz123456'
    const text = `Authorization: Bearer ${token}`
    const secrets = detectSecrets(text)
    const bearer = secrets.find((s) => s.type === 'bearer_token')

    expect(bearer?.start).toBe(text.indexOf(token))
    expect(bearer?.value).toBe(token)
  })

  test('detects multiple secrets', () => {
    const ghToken = 'ghp_abcdefghijklmnopqrstuvwxyz1234567890'
    const text = `
      GITHUB_TOKEN=${ghToken}
      AWS_KEY=AKIAIOSFODNN7EXAMPLE
    `
    const secrets = detectSecrets(text)

    expect(secrets.length).toBeGreaterThanOrEqual(2)
  })

  test('returns empty for clean text', () => {
    const text = 'Hello, this is a normal message without secrets!'
    const secrets = detectSecrets(text)

    expect(secrets.length).toBe(0)
  })
})

describe('maskSecret', () => {
  test('fully redacts short secrets', () => {
    const masked = maskSecret('abc123', 'test')
    expect(masked).toBe('[REDACTED:test]')
  })

  test('partially masks longer secrets', () => {
    const token = 'ghp_abcdefghijklmnopqrstuvwxyz1234567890'
    const masked = maskSecret(token, 'github_token')
    expect(masked.startsWith('ghp_')).toBe(true)
    expect(masked.includes('...')).toBe(true)
    // 40 chars * 0.1 = 4, so last 4 chars visible
    expect(masked.endsWith('7890')).toBe(true)
  })

  test('shows first and last chars proportionally', () => {
    const secret = 'a'.repeat(40)
    const masked = maskSecret(secret, 'test')

    // Should show ~10% on each end, max 4
    expect(masked).toBe('aaaa...aaaa')
  })
})

describe('redactSecrets', () => {
  test('redacts secrets in text', () => {
    const token = 'ghp_abcdefghijklmnopqrstuvwxyz1234567890'
    const text = `Use this token: ${token}`
    const { text: redacted, redactions } = redactSecrets(text)

    expect(redacted).not.toContain(token)
    expect(redacted).toContain('ghp_')
    expect(redacted).toContain('...')
    expect(redactions.length).toBe(1)
    expect(redactions[0]?.type).toBe('github_token')
  })

  test('preserves non-secret text', () => {
    const text = 'Hello world! No secrets here.'
    const { text: redacted, redactions } = redactSecrets(text)

    expect(redacted).toBe(text)
    expect(redactions.length).toBe(0)
  })

  test('handles multiple secrets', () => {
    const ghToken = 'ghp_abcdefghijklmnopqrstuvwxyz1234567890'
    const text = `
      Token: ${ghToken}
      Key: AKIAIOSFODNN7EXAMPLE
    `
    const { text: redacted, redactions } = redactSecrets(text)

    expect(redacted).not.toContain(ghToken)
    expect(redacted).not.toContain('AKIAIOSFODNN7EXAMPLE')
    expect(redactions.length).toBeGreaterThanOrEqual(2)
  })
})
