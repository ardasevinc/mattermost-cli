// Base Mattermost API client with auth and error handling

export class MattermostClient {
  private baseUrl: string
  private token: string
  private maxRetries = 3

  constructor(baseUrl: string, token: string) {
    // Remove trailing slash
    this.baseUrl = baseUrl.replace(/\/+$/, '')
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
      const retryAfter = response.headers.get('Retry-After')
      const delay = retryAfter ? parseInt(retryAfter, 10) * 1000 : 2 ** retryCount * 1000

      await new Promise((resolve) => setTimeout(resolve, delay))
      return this.request<T>(method, path, body, retryCount + 1)
    }

    if (!response.ok) {
      const error = await response.text()
      throw new MattermostAPIError(
        `API request failed: ${response.status} ${response.statusText}`,
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
