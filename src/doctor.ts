import { normalizeServerUrl } from './api/url'
import type { ResolvedConfigState } from './config'
import { preprocess, sanitizeTerminalLabel } from './preprocessing'

export type DoctorStatus = 'pass' | 'warn' | 'fail' | 'skipped'

export interface DoctorCheck {
  name: 'configuration' | 'server' | 'authentication'
  status: DoctorStatus
  message: string
  details?: Record<string, unknown>
}

export interface DoctorReport {
  ok: boolean
  checks: DoctorCheck[]
}

type Fetcher = typeof fetch

export interface DoctorOptions {
  fetcher?: Fetcher
  redact?: boolean
  timeoutMs?: number
}

const DEFAULT_TIMEOUT_MS = 10_000

function configurationCheck(config: ResolvedConfigState): DoctorCheck {
  const details = { urlSource: config.urlSource, tokenSource: config.tokenSource }
  if (config.fileError) {
    return {
      name: 'configuration',
      status: 'fail',
      message: 'config file could not be loaded',
      details,
    }
  }
  if (config.insecurePermissions && config.fileConfig.token) {
    return {
      name: 'configuration',
      status: 'fail',
      message: 'config file permissions expose a stored token; run chmod 600',
      details,
    }
  }
  if (!config.url || !config.token) {
    return {
      name: 'configuration',
      status: 'fail',
      message: 'configuration is incomplete',
      details,
    }
  }
  if (config.insecurePermissions) {
    return {
      name: 'configuration',
      status: 'warn',
      message: 'config file permissions are broader than recommended; run chmod 600',
      details,
    }
  }
  return { name: 'configuration', status: 'pass', message: 'credentials resolved', details }
}

async function requestJson(
  fetcher: Fetcher,
  url: string,
  headers?: RequestInit['headers'],
  timeoutMs = DEFAULT_TIMEOUT_MS,
): Promise<{ ok: true; value: unknown } | { ok: false; status?: number }> {
  const controller = new AbortController()
  let timer: ReturnType<typeof setTimeout> | undefined
  try {
    const request = (async (): Promise<
      { ok: true; value: unknown } | { ok: false; status?: number }
    > => {
      const response = await fetcher(url, {
        method: 'GET',
        headers,
        signal: controller.signal,
      })
      if (!response.ok) return { ok: false, status: response.status }
      try {
        return { ok: true, value: await response.json() }
      } catch {
        return { ok: false, status: response.status }
      }
    })()
    const timeout = new Promise<{ ok: false }>((resolve) => {
      timer = setTimeout(() => {
        controller.abort()
        resolve({ ok: false })
      }, timeoutMs)
    })
    return await Promise.race([request, timeout])
  } catch {
    return { ok: false }
  } finally {
    if (timer !== undefined) clearTimeout(timer)
  }
}

function safeRemoteString(value: unknown, config: ResolvedConfigState, redact: boolean): string {
  if (typeof value !== 'string' || value.length === 0) return 'unknown'
  const withoutConfiguredToken = config.token
    ? value.split(config.token).join('[REDACTED:token]')
    : value
  return sanitizeTerminalLabel(preprocess(withoutConfiguredToken, { redact }).text)
}

export async function runDoctor(
  config: ResolvedConfigState,
  options: DoctorOptions = {},
): Promise<DoctorReport> {
  const fetcher = options.fetcher ?? fetch
  const redact = options.redact ?? true
  const timeoutMs = options.timeoutMs ?? DEFAULT_TIMEOUT_MS
  const checks: DoctorCheck[] = [configurationCheck(config)]
  let baseUrl: string | undefined

  if (!config.url) {
    checks.push({ name: 'server', status: 'skipped', message: 'Mattermost URL is missing' })
  } else {
    try {
      baseUrl = normalizeServerUrl(config.url)
      const result = await requestJson(
        fetcher,
        `${baseUrl}/api/v4/system/ping?get_server_status=true`,
        undefined,
        timeoutMs,
      )
      if (!result.ok) {
        checks.push({
          name: 'server',
          status: 'fail',
          message: 'server health request failed',
          details: result.status ? { httpStatus: result.status } : undefined,
        })
      } else {
        const payload =
          typeof result.value === 'object' && result.value !== null ? result.value : {}
        const record = payload as Record<string, unknown>
        const status = safeRemoteString(record.status, config, redact)
        const databaseStatus = safeRemoteString(record.database_status, config, redact)
        const filestoreStatus = safeRemoteString(record.filestore_status, config, redact)
        const healthValues = [status, databaseStatus, filestoreStatus]
        const checkStatus: DoctorStatus = healthValues.some(
          (value) => value !== 'OK' && value !== 'unknown',
        )
          ? 'fail'
          : healthValues.includes('unknown')
            ? 'warn'
            : 'pass'
        checks.push({
          name: 'server',
          status: checkStatus,
          message:
            checkStatus === 'pass'
              ? 'server is healthy'
              : checkStatus === 'warn'
                ? 'server responded with incomplete health data'
                : 'server reported an unhealthy component',
          details: {
            status,
            databaseStatus,
            filestoreStatus,
          },
        })
      }
    } catch {
      checks.push({
        name: 'server',
        status: 'fail',
        message: 'Mattermost URL is invalid or unsafe',
      })
    }
  }

  if (!baseUrl || !config.token) {
    checks.push({
      name: 'authentication',
      status: 'skipped',
      message: !config.token ? 'Mattermost token is missing' : 'valid Mattermost URL is missing',
    })
  } else {
    const result = await requestJson(
      fetcher,
      `${baseUrl}/api/v4/users/me`,
      {
        Authorization: `Bearer ${config.token}`,
      },
      timeoutMs,
    )
    if (!result.ok) {
      checks.push({
        name: 'authentication',
        status: 'fail',
        message: 'authentication request failed',
        details: result.status ? { httpStatus: result.status } : undefined,
      })
    } else {
      const payload = typeof result.value === 'object' && result.value !== null ? result.value : {}
      const record = payload as Record<string, unknown>
      if (
        typeof record.id !== 'string' ||
        record.id.trim().length === 0 ||
        typeof record.username !== 'string' ||
        record.username.trim().length === 0
      ) {
        checks.push({
          name: 'authentication',
          status: 'fail',
          message: 'authentication response was invalid',
        })
      } else {
        checks.push({
          name: 'authentication',
          status: 'pass',
          message: 'authenticated',
          details: {
            id: safeRemoteString(record.id, config, redact),
            username: safeRemoteString(record.username, config, redact),
          },
        })
      }
    }
  }

  return { ok: checks.every((check) => check.status !== 'fail'), checks }
}

export function formatDoctorReport(report: DoctorReport): string {
  return report.checks
    .map((check) => {
      const details = check.details
        ? ` (${Object.entries(check.details)
            .map(([key, value]) => `${key}=${String(value)}`)
            .join(', ')})`
        : ''
      return `${check.status.padEnd(7)} ${check.name}: ${check.message}${details}`
    })
    .join('\n')
}
