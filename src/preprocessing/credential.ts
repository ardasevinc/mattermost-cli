const DEFAULT_OWNER = Symbol('resolved-config')
const credentialByOwner = new Map<symbol, string>()

export function setActiveMattermostCredential(token: string | undefined): void {
  if (token === undefined) {
    credentialByOwner.clear()
  } else if (token) {
    credentialByOwner.set(DEFAULT_OWNER, token)
  }
}

export function registerActiveMattermostCredential(token: string): () => void {
  const owner = Symbol('mattermost-credential-owner')
  if (token) credentialByOwner.set(owner, token)
  return () => {
    if (credentialByOwner.get(owner) === token) credentialByOwner.delete(owner)
  }
}

export function getActiveMattermostCredentials(): readonly string[] {
  return [...new Set(credentialByOwner.values())]
}
