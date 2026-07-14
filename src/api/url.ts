function isLoopbackHostname(hostname: string): boolean {
  if (hostname === 'localhost' || hostname === '[::1]') return true

  const octets = hostname.split('.')
  return (
    octets.length === 4 &&
    octets[0] === '127' &&
    octets.every((octet) => /^\d{1,3}$/.test(octet) && Number(octet) <= 255)
  )
}

export function normalizeServerUrl(serverUrl: string): string {
  let url: URL

  try {
    url = new URL(serverUrl)
  } catch {
    throw new Error('Invalid Mattermost URL')
  }

  if (url.username || url.password || url.search || url.hash) {
    throw new Error(
      'Invalid Mattermost URL: credentials, query strings, and fragments are not allowed',
    )
  }

  if (url.protocol !== 'https:' && url.protocol !== 'http:') {
    throw new Error('Mattermost URL must use HTTPS, or HTTP on a loopback host.')
  }

  if (url.protocol === 'http:' && !isLoopbackHostname(url.hostname)) {
    throw new Error(
      'Refusing to send a Mattermost token over plaintext HTTP. Use HTTPS or a loopback URL.',
    )
  }

  return url.toString().replace(/\/+$/, '')
}

export function assertSecureServerUrl(serverUrl: string): void {
  normalizeServerUrl(serverUrl)
}

export function buildPostPermalink(serverUrl: string, postId: string): string {
  const encodedPostId = encodeURIComponent(postId).replace(
    /[!'()*]/g,
    (character) => `%${character.charCodeAt(0).toString(16).toUpperCase()}`,
  )
  return `${normalizeServerUrl(serverUrl)}/_redirect/pl/${encodedPostId}`
}
