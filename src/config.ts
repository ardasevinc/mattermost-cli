// Config file handling for ~/.config/mattermost-cli/config.toml

import { access, mkdir, readFile, stat, writeFile } from 'node:fs/promises'
import { homedir } from 'node:os'
import { join } from 'node:path'
import { parse as parseTOML } from 'smol-toml'
import { setActiveMattermostCredential } from './preprocessing'

export interface FileConfig {
  url?: string
  token?: string
  redact?: boolean
  mention_names?: string[]
}

export type ConfigSource = 'cli' | 'env' | 'file' | 'missing'

export interface ResolvedConfigState {
  url?: string
  token?: string
  urlSource: ConfigSource
  tokenSource: ConfigSource
  fileConfig: FileConfig
  configPath: string
  fileExists: boolean
  insecurePermissions: boolean
  fileError?: 'read' | 'parse'
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

async function inspectConfigFile(configPath: string): Promise<{
  config: FileConfig
  exists: boolean
  insecurePermissions: boolean
  error?: 'read' | 'parse'
}> {
  let insecurePermissions = false
  try {
    const stats = await stat(configPath)
    insecurePermissions = (stats.mode & 0o077) !== 0
  } catch (error) {
    if ((error as NodeJS.ErrnoException).code === 'ENOENT') {
      return { config: {}, exists: false, insecurePermissions: false }
    }
    return { config: {}, exists: true, insecurePermissions: false, error: 'read' }
  }

  let content: string
  try {
    content = await readFile(configPath, 'utf-8')
  } catch {
    return { config: {}, exists: true, insecurePermissions, error: 'read' }
  }

  try {
    const parsed = parseTOML(content)
    const url = typeof parsed.url === 'string' ? parsed.url.trim() : undefined
    const token = typeof parsed.token === 'string' ? parsed.token.trim() : undefined
    const redact = typeof parsed.redact === 'boolean' ? parsed.redact : undefined
    const mentionNames = Array.isArray(parsed.mention_names)
      ? parsed.mention_names
          .filter((value): value is string => typeof value === 'string')
          .map((value) => value.trim())
          .filter((value) => value.length > 0)
      : undefined

    return {
      config: {
        url: url || undefined,
        token: token || undefined,
        redact,
        mention_names: mentionNames,
      },
      exists: true,
      insecurePermissions,
    }
  } catch {
    return { config: {}, exists: true, insecurePermissions, error: 'parse' }
  }
}

export async function resolveConfigState(
  options: { url?: string; token?: string },
  environment: NodeJS.ProcessEnv = process.env,
  configPath = CONFIG_PATH,
): Promise<ResolvedConfigState> {
  const file = await inspectConfigFile(configPath)
  const url = options.url || environment.MM_URL || file.config.url
  const token = options.token || environment.MM_TOKEN || file.config.token
  if (token) setActiveMattermostCredential(token)

  return {
    url,
    token,
    urlSource: options.url
      ? 'cli'
      : environment.MM_URL
        ? 'env'
        : file.config.url
          ? 'file'
          : 'missing',
    tokenSource: options.token
      ? 'cli'
      : environment.MM_TOKEN
        ? 'env'
        : file.config.token
          ? 'file'
          : 'missing',
    fileConfig: file.config,
    configPath,
    fileExists: file.exists,
    insecurePermissions: file.insecurePermissions,
    fileError: file.error,
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
  const state = await inspectConfigFile(CONFIG_PATH)
  if (state.insecurePermissions) {
    console.warn(
      `Warning: ${CONFIG_PATH} has insecure permissions.\n` + `  Run: chmod 600 "${CONFIG_PATH}"`,
    )
  }
  if (state.error) {
    console.warn(
      `Warning: Could not ${state.error === 'parse' ? 'parse' : 'read'} config at ${CONFIG_PATH}`,
    )
  }
  return state.config
}

export function getConfigPath(): string {
  return CONFIG_PATH
}

const CONFIG_TEMPLATE = `# Mattermost CLI Configuration
# https://github.com/ardasevinc/mattermost-cli

url = "https://mattermost.example.com"
token = "your-personal-access-token"
# mention_names = ["Arda", "arda.sevinc"]
`

export async function initConfigFile(): Promise<{ created: boolean; path: string }> {
  const { dirname } = await import('node:path')

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
    return {
      exists: false,
      path: CONFIG_PATH,
      hasUrl: false,
      hasToken: false,
      insecurePerms: false,
    }
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
