#!/usr/bin/env node
'use strict'

const { spawnSync } = require('node:child_process')

const packages = {
  'darwin-arm64': '@ardasevinc/mattermost-cli-darwin-arm64',
  'darwin-x64': '@ardasevinc/mattermost-cli-darwin-amd64',
  'linux-arm64': '@ardasevinc/mattermost-cli-linux-arm64',
  'linux-x64': '@ardasevinc/mattermost-cli-linux-amd64',
}

const target = `${process.platform}-${process.arch}`
const packageName = packages[target]
if (!packageName) {
  process.stderr.write(`mattermost-cli: unsupported platform ${target}\n`)
  process.exit(1)
}

let binary
try {
  binary = require.resolve(`${packageName}/bin/mm`)
} catch {
  process.stderr.write(
    `mattermost-cli: native package ${packageName} is missing; reinstall mattermost-cli without omitting optional dependencies\n`,
  )
  process.exit(1)
}

const result = spawnSync(binary, process.argv.slice(2), { stdio: 'inherit' })
if (result.error) {
  process.stderr.write('mattermost-cli: could not start the native mm binary\n')
  process.exit(1)
}
if (result.signal) {
  try {
    process.kill(process.pid, result.signal)
  } catch {
    process.exit(1)
  }
} else {
  process.exit(result.status ?? 1)
}
