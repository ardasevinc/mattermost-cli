// Base Mattermost API client with auth and error handling

import { registerActiveMattermostCredential } from '../preprocessing'
import { normalizeServerUrl } from './url'

export const REQUEST_TIMEOUT_MS = 15_000
const MAX_RETRY_DELAY_MS = 30_000
const MAX_RETRIES = 2

export function rateLimitDelay(response: Pick<Response, 'headers'>, retryCount: number): number {
  const retryAfter = response.headers.get('Retry-After')
  if (retryAfter) {
    const seconds = Number(retryAfter)
    const delay = Number.isFinite(seconds) ? seconds * 1000 : Date.parse(retryAfter) - Date.now()
    if (Number.isFinite(delay)) return Math.min(Math.max(delay, 0), MAX_RETRY_DELAY_MS)
  }

  const resetSeconds = Number(response.headers.get('X-RateLimit-Reset'))
  if (Number.isFinite(resetSeconds) && resetSeconds > 0) {
    return Math.min(resetSeconds * 1000, MAX_RETRY_DELAY_MS)
  }
  return Math.min(2 ** retryCount * 1000, MAX_RETRY_DELAY_MS)
}

function retryDelay(retryCount: number): number {
  return Math.min(2 ** retryCount * 1000, MAX_RETRY_DELAY_MS)
}

function waitForRetry(delay: number, reason: string, attempt: number): Promise<void> {
  console.error(`Mattermost request ${reason}; retrying in ${delay}ms (attempt ${attempt + 1}).`)
  return new Promise((resolve) => setTimeout(resolve, delay))
}

class RequestTimeoutError extends Error {}
class RequestTransportError extends Error {}
class InvalidJSONResponseError extends Error {}

export class MattermostMutationOutcomeUnknownError extends Error {
  constructor() {
    super(
      'Mattermost did not confirm the write. Its outcome is unknown; check the destination before retrying.',
    )
    this.name = 'MattermostMutationOutcomeUnknownError'
  }
}

interface RequestAttempt<T> {
  response: Response
  data?: T
}

export class MattermostClient {
  private baseUrl: string
  private token: string
  constructor(baseUrl: string, token: string) {
    this.baseUrl = normalizeServerUrl(baseUrl)
    this.token = token
  }

  private async attempt<T>(
    method: string,
    url: string,
    body?: unknown,
    followRedirects = true,
  ): Promise<RequestAttempt<T>> {
    const controller = new AbortController()
    const timeout = setTimeout(() => controller.abort(), REQUEST_TIMEOUT_MS)

    try {
      let response: Response
      try {
        response = await fetch(url, {
          method,
          headers: {
            Authorization: `Bearer ${this.token}`,
            'Content-Type': 'application/json',
          },
          body: body ? JSON.stringify(body) : undefined,
          signal: controller.signal,
          redirect: followRedirects ? 'follow' : 'manual',
        })
      } catch {
        if (controller.signal.aborted) throw new RequestTimeoutError()
        throw new RequestTransportError()
      }

      if (!response.ok) return { response }

      try {
        return { response, data: (await response.json()) as T }
      } catch (error) {
        if (controller.signal.aborted) throw new RequestTimeoutError()
        if (error instanceof TypeError) throw new RequestTransportError()
        throw new InvalidJSONResponseError()
      }
    } finally {
      clearTimeout(timeout)
    }
  }

  async request<T>(
    method: string,
    path: string,
    body?: unknown,
    retrySafe = method === 'GET',
    retryCount = 0,
  ): Promise<T> {
    const url = `${this.baseUrl}/api/v4${path}`
    let attempt: RequestAttempt<T>
    try {
      attempt = await this.attempt<T>(method, url, body, retrySafe || method === 'GET')
    } catch (error) {
      if (error instanceof InvalidJSONResponseError) {
        if (!retrySafe && method !== 'GET') throw new MattermostMutationOutcomeUnknownError()
        throw new Error('Mattermost returned an invalid JSON response.')
      }
      if (!(error instanceof RequestTimeoutError || error instanceof RequestTransportError)) {
        throw error
      }
      if (!retrySafe || retryCount >= MAX_RETRIES) {
        if (!retrySafe && method !== 'GET') {
          throw new MattermostMutationOutcomeUnknownError()
        }
        if (error instanceof RequestTimeoutError) {
          throw new Error(`Mattermost request timed out after ${REQUEST_TIMEOUT_MS}ms.`)
        }
        throw new Error('Unable to connect to Mattermost due to a network error.')
      }
      const delay = retryDelay(retryCount)
      await waitForRetry(
        delay,
        error instanceof RequestTimeoutError ? 'timed out' : 'hit a network error',
        retryCount,
      )
      return this.request<T>(method, path, body, retrySafe, retryCount + 1)
    }
    const { response } = attempt

    if (
      !retrySafe &&
      method !== 'GET' &&
      ((response.status >= 300 && response.status < 400) || response.status >= 500)
    ) {
      throw new MattermostMutationOutcomeUnknownError()
    }

    if (retrySafe && response.status === 429 && retryCount < MAX_RETRIES) {
      const delay = rateLimitDelay(response, retryCount)
      await waitForRetry(delay, 'was rate limited', retryCount)
      return this.request<T>(method, path, body, retrySafe, retryCount + 1)
    }

    if (retrySafe && [502, 503, 504].includes(response.status) && retryCount < MAX_RETRIES) {
      const delay = retryDelay(retryCount)
      await waitForRetry(delay, `received HTTP ${response.status}`, retryCount)
      return this.request<T>(method, path, body, retrySafe, retryCount + 1)
    }

    if (!response.ok) {
      throw new MattermostAPIError(`API request failed: ${response.status}.`, response.status)
    }

    return attempt.data as T
  }

  get<T>(path: string): Promise<T> {
    return this.request<T>('GET', path)
  }

  post<T>(path: string, body?: unknown, retrySafe = false): Promise<T> {
    return this.request<T>('POST', path, body, retrySafe)
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
  ) {
    super(message)
    this.name = 'MattermostAPIError'
  }
}

// Singleton instance
let client: MattermostClient | null = null
let releaseClientCredential: (() => void) | undefined

export function initClient(baseUrl: string, token: string): MattermostClient {
  const candidate = new MattermostClient(baseUrl, token)
  const releaseCandidateCredential = registerActiveMattermostCredential(token)
  const releasePreviousCredential = releaseClientCredential
  client = candidate
  releaseClientCredential = releaseCandidateCredential
  releasePreviousCredential?.()
  return candidate
}

export function getClient(): MattermostClient {
  if (!client) {
    throw new Error('Mattermost client not initialized. Call initClient() first.')
  }
  return client
}
