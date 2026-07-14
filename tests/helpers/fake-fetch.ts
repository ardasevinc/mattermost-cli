import { expect, vi } from 'vitest'

export interface FakeRequest {
  method: string
  url: URL
  body: unknown
}

interface Route {
  method: string
  path: string
  handle: (request: FakeRequest) => unknown
}

export function installRouteFetch(routes: Route[]) {
  const requests: FakeRequest[] = []
  const fake = vi.fn(async (input: string | URL | Request, init?: RequestInit) => {
    const url = new URL(typeof input === 'string' || input instanceof URL ? input : input.url)
    const method = init?.method ?? (input instanceof Request ? input.method : 'GET')
    const body = typeof init?.body === 'string' ? JSON.parse(init.body) : undefined
    const request = { method, url, body }
    requests.push(request)
    const route = routes.find(
      (candidate) => candidate.method === method && candidate.path === url.pathname,
    )
    expect(route, `unhandled fake route: ${method} ${url.pathname}`).toBeDefined()
    const value = await route?.handle(request)
    return value instanceof Response ? value : Response.json(value)
  })
  vi.stubGlobal('fetch', fake)
  return { requests, fake }
}
