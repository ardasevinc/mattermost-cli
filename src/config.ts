// Config file handling for ~/.config/mattermost-cli/config.toml

import { homedir } from 'os'
import { join } from 'path'
import { stat, mkdir, writeFile, readFile, access } from 'fs/promises'
import { parse as parseTOML } from 'smol-toml'

export interface FileConfig {
  url?: string
  token?: string
}

const CONFIG_PATH = join(homedir(), '.config', 'mattermost-cli', 'config.toml')

/**
 * Check if config file has insecure permissions (group/other readable).
 * Returns true if permissions are too open.
 */
async function hasInsecurePermissions(): Promise<boolean> {
  try {
    const stats = await stat(CONFIG_PATH)
    // Check if group or other have any permissions (mode & 0o077)
    return (stats.mode & 0o077) !== 0
  } catch {
    return false
  }
}

async function fileExists(path: string): Promise<boolean> {
  try {
    await access(path)
    return true
  } catch {
    return false
  }
}

export async function loadConfigFile(): Promise<FileConfig> {
  if (!(await fileExists(CONFIG_PATH))) {
    return {}
  }

  // Warn if config file is readable by group/other (contains token)
  if (await hasInsecurePermissions()) {
    console.warn(
      `Warning: ${CONFIG_PATH} has insecure permissions.\n` +
      `  Run: chmod 600 "${CONFIG_PATH}"`
    )
  }

  try {
    const content = await readFile(CONFIG_PATH, 'utf-8')
    const parsed = parseTOML(content)

    // Trim and treat empty strings as undefined
    const url = typeof parsed.url === 'string' ? parsed.url.trim() : undefined
    const token = typeof parsed.token === 'string' ? parsed.token.trim() : undefined

    return {
      url: url || undefined,
      token: token || undefined,
    }
  } catch {
    console.warn(`Warning: Could not parse config at ${CONFIG_PATH}`)
    return {}
  }
}

export function getConfigPath(): string {
  return CONFIG_PATH
}

const CONFIG_TEMPLATE = `# Mattermost CLI Configuration
# https://github.com/ardasevinc/mattermost-cli

url = "https://mattermost.example.com"
token = "your-personal-access-token"
`

export async function initConfigFile(): Promise<{ created: boolean; path: string }> {
  const { dirname } = await import('path')

  const dir = dirname(CONFIG_PATH)

  // Create directory if it doesn't exist
  await mkdir(dir, { recursive: true })

  if (await fileExists(CONFIG_PATH)) {
    return { created: false, path: CONFIG_PATH }
  }

  // Write template atomically with secure permissions (0o600)
  // Using 'wx' flag ensures we don't overwrite if file was created between check and write
  await writeFile(CONFIG_PATH, CONFIG_TEMPLATE, { mode: 0o600, flag: 'wx' })

  return { created: true, path: CONFIG_PATH }
}

export async function getConfigStatus(): Promise<{
  exists: boolean
  path: string
  hasUrl: boolean
  hasToken: boolean
  insecurePerms: boolean
}> {
  const exists = await fileExists(CONFIG_PATH)

  if (!exists) {
    return { exists: false, path: CONFIG_PATH, hasUrl: false, hasToken: false, insecurePerms: false }
  }

  const insecurePerms = await hasInsecurePermissions()
  const config = await loadConfigFile()

  return {
    exists: true,
    path: CONFIG_PATH,
    hasUrl: !!config.url,
    hasToken: !!config.token,
    insecurePerms,
  }
}
