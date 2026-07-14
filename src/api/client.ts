// Base Mattermost API client with auth and error handling

import { sanitizeTerminalLabel } from '../preprocessing'
import { normalizeServerUrl } from './url'

const MAX_RATE_LIMIT_DELAY_MS = 5 * 60 * 1000

export function rateLimitDelay(response: Pick<Response, 'headers'>, retryCount: number): number {
  const retryAfter = response.headers.get('Retry-After')
  if (retryAfter) {
    const seconds = Number(retryAfter)
    const delay = Number.isFinite(seconds) ? seconds * 1000 : Date.parse(retryAfter) - Date.now()
    if (Number.isFinite(delay)) return Math.min(Math.max(delay, 0), MAX_RATE_LIMIT_DELAY_MS)
  }

  const resetSeconds = Number(response.headers.get('X-RateLimit-Reset'))
  if (Number.isFinite(resetSeconds) && resetSeconds > 0) {
    return Math.min(resetSeconds * 1000, MAX_RATE_LIMIT_DELAY_MS)
  }
  return Math.min(2 ** retryCount * 1000, MAX_RATE_LIMIT_DELAY_MS)
}

export class MattermostClient {
  private baseUrl: string
  private token: string
  private maxRetries = 3

  constructor(baseUrl: string, token: string) {
    this.baseUrl = normalizeServerUrl(baseUrl)
    this.token = token
  }

  async request<T>(method: string, path: string, body?: unknown, retryCount = 0): Promise<T> {
    const url = `${this.baseUrl}/api/v4${path}`

    const response = await fetch(url, {
      method,
      headers: {
        Authorization: `Bearer ${this.token}`,
        'Content-Type': 'application/json',
      },
      body: body ? JSON.stringify(body) : undefined,
    })

    // Handle rate limiting with exponential backoff
    if (response.status === 429 && retryCount < this.maxRetries) {
      await new Promise((resolve) => setTimeout(resolve, rateLimitDelay(response, retryCount)))
      return this.request<T>(method, path, body, retryCount + 1)
    }

    if (!response.ok) {
      const error = await response.text()
      throw new MattermostAPIError(
        `API request failed: ${response.status} ${sanitizeTerminalLabel(response.statusText)}`,
        response.status,
        error,
      )
    }

    return response.json() as Promise<T>
  }

  get<T>(path: string): Promise<T> {
    return this.request<T>('GET', path)
  }

  post<T>(path: string, body?: unknown): Promise<T> {
    return this.request<T>('POST', path, body)
  }

  put<T>(path: string, body?: unknown): Promise<T> {
    return this.request<T>('PUT', path, body)
  }

  delete<T>(path: string): Promise<T> {
    return this.request<T>('DELETE', path)
  }
}

export class MattermostAPIError extends Error {
  constructor(
    message: string,
    public status: number,
    public body: string,
  ) {
    super(message)
    this.name = 'MattermostAPIError'
  }
}

// Singleton instance
let client: MattermostClient | null = null

export function initClient(baseUrl: string, token: string): MattermostClient {
  client = new MattermostClient(baseUrl, token)
  return client
}

export function getClient(): MattermostClient {
  if (!client) {
    throw new Error('Mattermost client not initialized. Call initClient() first.')
  }
  return client
}
