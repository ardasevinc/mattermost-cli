import { spawnSync } from 'node:child_process'
import { randomUUID } from 'node:crypto'
import { fileURLToPath } from 'node:url'
import path from 'node:path'

const root = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '..')
const composeFile = path.join(root, 'tests/e2e/compose.yml')
const project = `mattermost-cli-e2e-${process.pid}-${randomUUID().slice(0, 8)}`
const requestedPort = process.env.MM_E2E_PORT || '0'
const compose = ['compose', '-p', project, '-f', composeFile]

function run(command, args, options = {}) {
  const result = spawnSync(command, args, {
    cwd: root,
    encoding: 'utf8',
    stdio: options.capture ? 'pipe' : 'inherit',
    env: { ...process.env, MM_E2E_PORT: requestedPort, ...options.env },
  })
  if (result.status !== 0) {
    if (options.capture) {
      if (result.stdout) process.stderr.write(result.stdout)
      if (result.stderr) process.stderr.write(result.stderr)
    }
    throw new Error(`${command} exited with status ${result.status ?? 'unknown'}`)
  }
  return result.stdout || ''
}

function mmctl(args, capture = false) {
  return run(
    'docker',
    [...compose, 'exec', '-T', 'mattermost', 'mmctl', '--local', ...args],
    { capture },
  )
}

function cleanup() {
  run('docker', [...compose, 'down', '--volumes', '--remove-orphans'])
  const containers = run('docker', [...compose, 'ps', '-aq'], { capture: true }).trim()
  const volumes = run(
    'docker',
    ['volume', 'ls', '-q', '--filter', `label=com.docker.compose.project=${project}`],
    { capture: true },
  ).trim()
  if (containers || volumes) {
    throw new Error('Docker E2E cleanup left project resources behind')
  }
}

try {
  run('docker', [...compose, 'up', '-d', '--wait', '--wait-timeout', '180'])
  const published = run('docker', [...compose, 'port', 'mattermost', '8065'], {
    capture: true,
  }).trim()
  const port = published.match(/:(\d+)$/)?.[1]
  if (!port) throw new Error('Docker did not report the Mattermost E2E port')
  const url = `http://127.0.0.1:${port}`

  mmctl([
    '--quiet',
    'user',
    'create',
    '--email',
    'sender@example.test',
    '--username',
    'sender',
    '--password',
    'E2ePassword1!',
    '--system-admin',
    '--email-verified',
    '--disable-welcome-email',
  ])
  for (const username of ['alice', 'bob']) {
    mmctl([
      '--quiet',
      'user',
      'create',
      '--email',
      `${username}@example.test`,
      '--username',
      username,
      '--password',
      'E2ePassword1!',
      '--email-verified',
      '--disable-welcome-email',
    ])
  }
  mmctl(['--quiet', 'team', 'create', '--name', 'e2e', '--display-name', 'E2E'])
  mmctl(['--quiet', 'team', 'users', 'add', 'e2e', 'sender', 'alice', 'bob'])
  const generated = JSON.parse(
    mmctl(['--json', 'token', 'generate', 'sender', 'mattermost-cli-e2e'], true),
  )
  const token = generated?.[0]?.token
  if (typeof token !== 'string' || token.length === 0) {
    throw new Error('Mattermost did not return an E2E access token')
  }

  run('bunx', ['vitest', 'run', '--config', 'vitest.e2e.config.ts'], {
    env: { MM_E2E_URL: url, MM_E2E_TOKEN: token },
  })
} finally {
  cleanup()
}
