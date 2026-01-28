// User fetching with in-memory caching

import type { User } from '../types'
import { getClient } from './client'

// Cache users during the session
const userCache = new Map<string, User>()
const usernameToId = new Map<string, string>()

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
    return userCache.get(cachedId)!
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
