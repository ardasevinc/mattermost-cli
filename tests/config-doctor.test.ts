import { chmod, mkdtemp, writeFile } from 'node:fs/promises'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { describe, expect, it, vi } from 'vitest'
import { resolveConfigState } from '../src/config'
import { formatDoctorReport, runDoctor } from '../src/doctor'
import { preprocess } from '../src/preprocessing'

async function configFile(content: string, mode = 0o600): Promise<string> {
  const directory = await mkdtemp(join(tmpdir(), 'mm-doctor-'))
  const path = join(directory, 'config.toml')
  await writeFile(path, content)
  await chmod(path, mode)
  return path
}

describe('doctor config resolution', () => {
  it('registers a config-file token at resolution before output validation', async () => {
    const token = 'file-active-token'
    const path = await configFile(`url = "https://file.example"\ntoken = "${token}"\n`)
    await resolveConfigState({}, {}, path)
    expect(preprocess(`invalid=${token}`, { redact: false }).text).toBe(
      'invalid=[REDACTED:mattermost_credential]',
    )
  })

  it('reports CLI > env > file sources without exposing values', async () => {
    const path = await configFile('url = "https://file.example"\ntoken = "file-secret"\n')
    const state = await resolveConfigState(
      { url: 'https://cli.example' },
      { MM_URL: 'https://env.example', MM_TOKEN: 'env-secret' },
      path,
    )

    expect(state.urlSource).toBe('cli')
    expect(state.tokenSource).toBe('env')
    expect(state.url).toBe('https://cli.example')
    expect(state.token).toBe('env-secret')
  })

  it('detects insecure config permissions', async () => {
    const path = await configFile('token = "file-secret"\n', 0o644)
    const state = await resolveConfigState({}, {}, path)
    const report = await runDoctor(state)

    expect(report.checks[0]).toMatchObject({ status: 'fail' })
    expect(JSON.stringify(report)).not.toContain('file-secret')
  })

  it('fails insecure permissions when an environment token overrides a stored file token', async () => {
    const path = await configFile('url = "https://file.example"\ntoken = "file-secret"\n', 0o644)
    const state = await resolveConfigState({}, { MM_TOKEN: 'env-secret' }, path)
    const fetcher = vi.fn(async () => Response.json({})) as unknown as typeof fetch
    const report = await runDoctor(state, { fetcher })

    expect(state).toMatchObject({ urlSource: 'file', tokenSource: 'env' })
    expect(report.checks[0]).toMatchObject({ status: 'fail' })
    expect(JSON.stringify(report)).not.toContain('file-secret')
    expect(JSON.stringify(report)).not.toContain('env-secret')
  })

  it('fails closed on an unreadable or malformed config file', async () => {
    const path = await configFile('not valid = [toml')
    const state = await resolveConfigState({}, {}, path)

    expect(state.fileError).toBe('parse')
    expect((await runDoctor(state)).checks[0]).toMatchObject({ status: 'fail' })
  })

  it('distinguishes a missing file from a config read failure', async () => {
    const directory = await mkdtemp(join(tmpdir(), 'mm-doctor-directory-'))
    const readFailure = await resolveConfigState({}, {}, directory)
    const missing = await resolveConfigState({}, {}, join(directory, 'missing.toml'))

    expect(readFailure).toMatchObject({ fileExists: true, fileError: 'read' })
    expect(missing).toMatchObject({ fileExists: false, fileError: undefined })
  })

  it('only warns for insecure permissions when the file stores no token', async () => {
    const path = await configFile('url = "https://file.example"\n', 0o644)
    const state = await resolveConfigState({}, { MM_TOKEN: 'env-secret' }, path)
    const fetcher = vi
      .fn()
      .mockResolvedValueOnce(
        Response.json({ status: 'OK', database_status: 'OK', filestore_status: 'OK' }),
      )
      .mockResolvedValueOnce(
        Response.json({ id: 'user-id', username: 'arda' }),
      ) as unknown as typeof fetch

    expect((await runDoctor(state, { fetcher })).checks[0]).toMatchObject({ status: 'warn' })
  })

  it('fails incomplete config even when tokenless file permissions would otherwise warn', async () => {
    const path = await configFile('url = "https://file.example"\n', 0o644)
    const state = await resolveConfigState({}, {}, path)
    const fetcher = vi.fn(async () =>
      Response.json({ status: 'OK', database_status: 'OK', filestore_status: 'OK' }),
    ) as unknown as typeof fetch
    const report = await runDoctor(state, { fetcher })

    expect(report.ok).toBe(false)
    expect(report.checks[0]).toMatchObject({
      status: 'fail',
      message: 'configuration is incomplete',
    })
    expect(report.checks[1]).toMatchObject({ status: 'pass' })
    expect(report.checks[2]).toMatchObject({ status: 'skipped' })
  })
})

describe('doctor checks', () => {
  it('runs ping without auth and skips authentication when the token is missing', async () => {
    const fetcher = vi.fn(async () =>
      Response.json({ status: 'OK', database_status: 'OK' }),
    ) as unknown as typeof fetch
    const state = await resolveConfigState({ url: 'https://mm.example' }, {}, '/missing')
    const report = await runDoctor(state, { fetcher })

    expect(fetcher).toHaveBeenCalledOnce()
    expect(fetcher).toHaveBeenCalledWith(
      'https://mm.example/api/v4/system/ping?get_server_status=true',
      expect.objectContaining({
        method: 'GET',
        headers: undefined,
        signal: expect.any(AbortSignal),
      }),
    )
    expect(report.checks[0]).toMatchObject({ status: 'fail' })
    expect(report.checks[1]).toMatchObject({ status: 'warn' })
    expect(report.checks[1]?.details).toMatchObject({ filestoreStatus: 'unknown' })
    expect(report.checks[2]).toMatchObject({ status: 'skipped' })
  })

  it('whitelists server and user fields', async () => {
    const token = 'super-secret-token'
    const fetcher = vi
      .fn()
      .mockResolvedValueOnce(
        Response.json({
          status: 'OK',
          database_status: 'OK',
          filestore_status: 'OK',
          secret: token,
        }),
      )
      .mockResolvedValueOnce(
        Response.json({ id: 'user-id', username: 'arda', email: token, roles: token }),
      ) as unknown as typeof fetch
    const state = await resolveConfigState({ url: 'https://mm.example', token }, {}, '/missing')
    const report = await runDoctor(state, { fetcher })
    const output = JSON.stringify(report)

    expect(report.ok).toBe(true)
    expect(output).not.toContain(token)
    expect(output).not.toContain('email')
    expect(report.checks[2]?.details).toEqual({ id: 'user-id', username: 'arda' })
  })

  it('sanitizes failures and does not abort before all checks are present', async () => {
    const token = 'never-print-this'
    const fetcher = vi.fn(
      async () => new Response(`${token}\nremote body`, { status: 401 }),
    ) as unknown as typeof fetch
    const state = await resolveConfigState({ url: 'https://mm.example', token }, {}, '/missing')
    const report = await runDoctor(state, { fetcher })
    const output = `${JSON.stringify(report)}\n${formatDoctorReport(report)}`

    expect(report.ok).toBe(false)
    expect(report.checks).toHaveLength(3)
    expect(output).not.toContain(token)
    expect(output).not.toContain('remote body')
    expect(report.checks[1]).toMatchObject({ status: 'fail', details: { httpStatus: 401 } })
    expect(report.checks[2]).toMatchObject({ status: 'fail', details: { httpStatus: 401 } })
  })

  it('fails unhealthy server values and skips auth for unsafe URLs', async () => {
    const unhealthyFetcher = vi.fn(async () =>
      Response.json({ status: 'OK', database_status: 'DOWN', filestore_status: 'OK' }),
    ) as unknown as typeof fetch
    const healthyConfig = await resolveConfigState({ url: 'https://mm.example' }, {}, '/missing')
    const unhealthy = await runDoctor(healthyConfig, { fetcher: unhealthyFetcher })
    expect(unhealthy.checks[1]).toMatchObject({ status: 'fail' })

    const unsafeConfig = await resolveConfigState(
      { url: 'http://mm.example', token: 'secret' },
      {},
      '/missing',
    )
    const neverFetch = vi.fn() as unknown as typeof fetch
    const unsafe = await runDoctor(unsafeConfig, { fetcher: neverFetch })
    expect(neverFetch).not.toHaveBeenCalled()
    expect(unsafe.checks[1]).toMatchObject({ status: 'fail' })
    expect(unsafe.checks[2]).toMatchObject({ status: 'skipped' })
  })

  it('redacts configured tokens and sanitizes terminal controls in all remote strings', async () => {
    const token = 'super-secret-token'
    const osc = '\u001b]8;;https://evil.example\u0007click\u001b]8;;\u0007'
    const boundaryProbe = '\u001bghp_abcdefghijklmnopqrstuvwxyz1234567890'
    const state = await resolveConfigState({ url: 'https://mm.example', token }, {}, '/missing')

    for (const redact of [true, false]) {
      const fetcher = vi
        .fn()
        .mockResolvedValueOnce(
          Response.json({
            status: 'OK',
            database_status: `${token}${osc}`,
            filestore_status: 'OK',
          }),
        )
        .mockResolvedValueOnce(
          Response.json({ id: `${token}${osc}`, username: `arda${osc}${boundaryProbe}` }),
        ) as unknown as typeof fetch
      const report = await runDoctor(state, { fetcher, redact })
      const output = `${JSON.stringify(report)}\n${formatDoctorReport(report)}`
      expect(output).not.toContain(token)
      expect(output).not.toContain('\u001b')
      expect(output).not.toContain('\u0007')
      expect(output).toContain('\\u001b')
      expect(output).toContain('[REDACTED:token]')
      if (redact) expect(output).not.toContain('ghp_abcdefghijklmnopqrstuvwxyz1234567890')
    }
  })

  it('times out each request independently and still attempts authentication', async () => {
    const fetcher = vi
      .fn()
      .mockImplementationOnce(() => new Promise<Response>(() => {}))
      .mockResolvedValueOnce(
        Response.json({ id: 'user-id', username: 'arda' }),
      ) as unknown as typeof fetch
    const state = await resolveConfigState(
      { url: 'https://mm.example', token: 'token' },
      {},
      '/missing',
    )
    const report = await runDoctor(state, { fetcher, timeoutMs: 5 })

    expect(fetcher).toHaveBeenCalledTimes(2)
    expect(report.checks[1]).toMatchObject({ status: 'fail' })
    expect(report.checks[2]).toMatchObject({ status: 'pass' })
  })

  it('rejects empty authentication identity fields', async () => {
    const fetcher = vi
      .fn()
      .mockResolvedValueOnce(
        Response.json({ status: 'OK', database_status: 'OK', filestore_status: 'OK' }),
      )
      .mockResolvedValueOnce(Response.json({ id: ' ', username: '' })) as unknown as typeof fetch
    const state = await resolveConfigState(
      { url: 'https://mm.example', token: 'token' },
      {},
      '/missing',
    )
    const report = await runDoctor(state, { fetcher })

    expect(report.checks[2]).toMatchObject({ status: 'fail' })
  })
})
