import { spawn } from 'node:child_process'
import { randomUUID } from 'node:crypto'
import { rmSync } from 'node:fs'
import { fileURLToPath } from 'node:url'
import os from 'node:os'
import path from 'node:path'

const root = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '..')
const composeFile = path.join(root, 'tests/e2e/compose.yml')
const project = `mattermost-cli-e2e-${process.pid}-${randomUUID().slice(0, 8)}`
const markerTeam = `mm-e2e-${randomUUID().replaceAll('-', '').slice(0, 16)}`
const requestedPort = process.env.MM_E2E_PORT || '0'
const compose = ['compose', '-p', project, '-f', composeFile]
const goBinary = path.join(os.tmpdir(), `${project}-mm`)
const children = new Set()

function run(command, args, options = {}) {
  return new Promise((resolve, reject) => {
    const child = spawn(command, args, {
      cwd: root,
      stdio: options.capture ? ['ignore', 'pipe', 'pipe'] : 'inherit',
      env: { ...process.env, MM_E2E_PORT: requestedPort, ...options.env },
    })
    children.add(child)
    let stdout = ''
    let stderr = ''
    if (options.capture) {
      child.stdout.setEncoding('utf8')
      child.stderr.setEncoding('utf8')
      child.stdout.on('data', (chunk) => {
        stdout += chunk
      })
      child.stderr.on('data', (chunk) => {
        stderr += chunk
      })
    }
    child.once('error', (error) => {
      children.delete(child)
      reject(error)
    })
    child.once('close', (code, signal) => {
      children.delete(child)
      if (code === 0) {
        resolve(stdout)
        return
      }
      if (options.capture && !options.sensitive) {
        if (stdout) process.stderr.write(stdout)
        if (stderr) process.stderr.write(stderr)
      }
      reject(new Error(`${command} exited with status ${code ?? signal ?? 'unknown'}`))
    })
  })
}

async function mmctl(args, capture = false, sensitive = false) {
  return run(
    'docker',
    [...compose, 'exec', '-T', 'mattermost', 'mmctl', '--local', ...args],
    { capture, sensitive },
  )
}

let cleanupPromise

function cleanup() {
  cleanupPromise ??= cleanupOnce()
  return cleanupPromise
}

async function cleanupOnce() {
  await run('docker', [...compose, 'down', '--volumes', '--remove-orphans'])
  const containers = (await run('docker', [...compose, 'ps', '-aq'], { capture: true })).trim()
  const volumes = (
    await run(
      'docker',
      ['volume', 'ls', '-q', '--filter', `label=com.docker.compose.project=${project}`],
      { capture: true },
    )
  ).trim()
  const networks = (
    await run(
      'docker',
      ['network', 'ls', '-q', '--filter', `label=com.docker.compose.project=${project}`],
      { capture: true },
    )
  ).trim()
  if (containers || volumes || networks) {
    throw new Error('Docker E2E cleanup left project resources behind')
  }
}

let terminating = false
for (const [signal, exitCode] of [
  ['SIGINT', 130],
  ['SIGTERM', 143],
]) {
  process.once(signal, () => {
    void terminate(exitCode)
  })
}

async function terminate(exitCode) {
  if (terminating) return
  terminating = true
  for (const child of children) child.kill('SIGTERM')
  try {
    await cleanup()
  } finally {
    rmSync(goBinary, { force: true })
    process.exit(exitCode)
  }
}

try {
  await run('docker', [...compose, 'up', '-d', '--wait', '--wait-timeout', '180'])
  const published = (
    await run('docker', [...compose, 'port', 'mattermost', '8065'], {
      capture: true,
    })
  ).trim()
  const port = published.match(/:(\d+)$/)?.[1]
  if (!port) throw new Error('Docker did not report the Mattermost E2E port')
  const url = `http://127.0.0.1:${port}`

  await mmctl([
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
  for (const username of ['alice', 'bob', 'carol', 'dave']) {
    await mmctl([
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
  await mmctl(['--quiet', 'team', 'create', '--name', 'e2e', '--display-name', 'E2E'])
  await mmctl([
    '--quiet',
    'team',
    'users',
    'add',
    'e2e',
    'sender',
    'alice',
    'bob',
    'carol',
    'dave',
  ])
  await mmctl([
    '--quiet',
    'team',
    'create',
    '--name',
    markerTeam,
    '--display-name',
    `Mattermost CLI E2E ${markerTeam}`,
  ])
  await mmctl(['--quiet', 'team', 'users', 'add', markerTeam, 'sender'])
  const generated = JSON.parse(
    await mmctl(['--json', 'token', 'generate', 'sender', 'mattermost-cli-e2e'], true, true),
  )
  const token = generated?.[0]?.token
  if (typeof token !== 'string' || token.length === 0) {
    throw new Error('Mattermost did not return an E2E access token')
  }

  await run('bunx', ['vitest', 'run', '--config', 'vitest.e2e.config.ts'], {
    env: { MM_E2E_URL: url, MM_E2E_TOKEN: token },
  })
  await run('go', ['build', '-tags=e2e', '-o', goBinary, './cmd/mm'])
  await run('go', ['test', '-tags=e2e', '-count=1', './tests/e2e'], {
    env: {
      MM_E2E_URL: url,
      MM_E2E_TOKEN: token,
      MM_E2E_BINARY: goBinary,
      MM_E2E_MARKER_TEAM: markerTeam,
    },
  })
} finally {
  try {
    await cleanup()
  } finally {
    rmSync(goBinary, { force: true })
  }
}
