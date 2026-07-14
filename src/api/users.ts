// User fetching with in-memory caching

import type { User } from '../types'
import { getClient } from './client'

// Cache users during the session
const userCache = new Map<string, User>()
const usernameToId = new Map<string, string>()
const MAX_USERS_LIST_PAGE = 200
const MAX_USERS_SEARCH_PAGE = 1000

export interface UserDirectoryResult {
  users: unknown
  truncated: boolean | null
}

export async function fetchUsers(options: {
  query?: string
  teamId?: string
  limit: number
}): Promise<UserDirectoryResult> {
  const client = getClient()
  const query = options.query?.trim()
  const endpointLimit = query ? MAX_USERS_SEARCH_PAGE : MAX_USERS_LIST_PAGE
  const probeLimit = Math.min(options.limit + 1, endpointLimit)
  const users = query
    ? await client.post<unknown>('/users/search', {
        term: query,
        ...(options.teamId ? { team_id: options.teamId } : {}),
        limit: probeLimit,
        allow_inactive: false,
      })
    : await client.get<unknown>(
        `/users?page=0&per_page=${probeLimit}&active=true${options.teamId ? `&in_team=${encodeURIComponent(options.teamId)}` : ''}`,
      )

  const count = Array.isArray(users) ? users.length : 0
  return {
    users,
    truncated:
      count > options.limit
        ? true
        : probeLimit === endpointLimit && count === endpointLimit
          ? null
          : false,
  }
}

export async function getMe(): Promise<User> {
  const client = getClient()
  const user = await client.get<User>('/users/me')
  cacheUser(user)
  return user
}

export async function getUser(userId: string): Promise<User> {
  // Check cache first
  const cached = userCache.get(userId)
  if (cached) return cached

  const client = getClient()
  const user = await client.get<User>(`/users/${userId}`)
  cacheUser(user)
  return user
}

export async function getUserByUsername(username: string): Promise<User> {
  // Check cache first
  const cachedId = usernameToId.get(username.toLowerCase())
  if (cachedId) {
    const cachedUser = userCache.get(cachedId)
    if (cachedUser) return cachedUser
    usernameToId.delete(username.toLowerCase())
  }

  const client = getClient()
  const user = await client.get<User>(`/users/username/${username}`)
  cacheUser(user)
  return user
}

export async function getUsersByIds(userIds: string[]): Promise<User[]> {
  // Filter out already cached
  const uncachedIds = userIds.filter((id) => !userCache.has(id))

  if (uncachedIds.length > 0) {
    const client = getClient()
    const users = await client.post<User[]>('/users/ids', uncachedIds)
    users.forEach(cacheUser)
  }

  // Return all from cache (now populated)
  return userIds.map((id) => userCache.get(id)).filter(Boolean) as User[]
}

function cacheUser(user: User): void {
  if (typeof user?.id !== 'string' || typeof user?.username !== 'string') return
  userCache.set(user.id, user)
  usernameToId.set(user.username.toLowerCase(), user.id)
}

export function getCachedUser(userId: string): User | undefined {
  return userCache.get(userId)
}

export function clearUserCache(): void {
  userCache.clear()
  usernameToId.clear()
}
