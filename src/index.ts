#!/usr/bin/env bun
// Mattermost CLI - Entry point

import { Command } from 'commander'
import { listChannels, fetchDMs } from './cli'
import { loadConfigFile, getConfigPath, initConfigFile, getConfigStatus } from './config'
import pkg from '../package.json'

const program = new Command()

program
  .name('mm')
  .description('Mattermost CLI - Fetch and display messages')
  .version(pkg.version)

// Global options (don't use env vars as defaults - they leak in --help)
program
  .option('-t, --token <token>', 'Mattermost personal access token (or MM_TOKEN env)')
  .option('--url <url>', 'Mattermost server URL (or MM_URL env)')
  .option('--json', 'Output as JSON', false)
  .option('--no-color', 'Disable colored output')

// Resolve config from CLI options → env vars → config file
async function resolveConfig(options: { url?: string; token?: string }): Promise<{ url: string; token: string }> {
  // Check CLI args and env vars first
  let url = options.url || process.env.MM_URL
  let token = options.token || process.env.MM_TOKEN

  // Only load config file if we're still missing values (avoids noisy warnings)
  if (!url || !token) {
    const fileConfig = await loadConfigFile()
    url = url || fileConfig.url
    token = token || fileConfig.token
  }

  const configPath = getConfigPath()

  if (!url) {
    console.error(
      'Error: Mattermost URL required.\n' +
      '  1. Use --url flag\n' +
      '  2. Set MM_URL env var\n' +
      `  3. Add to ${configPath}`
    )
    process.exit(1)
  }
  if (!token) {
    console.error(
      'Error: Mattermost token required.\n' +
      '  1. Use --token flag\n' +
      '  2. Set MM_TOKEN env var\n' +
      `  3. Add to ${configPath}`
    )
    process.exit(1)
  }

  return { url, token }
}

// Config management command
program
  .command('config')
  .description('Manage config file')
  .option('--path', 'Print config file path')
  .option('--init', 'Create config file with template')
  .action(async (opts) => {
    try {
      if (opts.path) {
        console.log(getConfigPath())
        return
      }

      if (opts.init) {
        const result = await initConfigFile()
        if (result.created) {
          console.log(`Created config file: ${result.path}`)
          console.log('Edit the file to add your Mattermost URL and token.')
        } else {
          console.log(`Config file already exists: ${result.path}`)
        }
        return
      }

      // Default: show config status
      const status = await getConfigStatus()
      console.log(`Config path: ${status.path}`)
      console.log(`Exists: ${status.exists ? 'yes' : 'no'}`)
      if (status.exists) {
        console.log(`URL configured: ${status.hasUrl ? 'yes' : 'no'}`)
        console.log(`Token configured: ${status.hasToken ? 'yes' : 'no'}`)
        if (status.insecurePerms) {
          console.log(`\nWarning: Config file has insecure permissions.`)
          console.log(`  Run: chmod 600 "${status.path}"`)
        }
      } else {
        console.log('\nRun `mm config --init` to create a config file.')
      }
    } catch (err) {
      console.error('Error:', err instanceof Error ? err.message : err)
      process.exit(1)
    }
  })

// List channels command
program
  .command('channels')
  .description('List all DM channels')
  .action(async () => {
    const opts = program.opts()
    const config = await resolveConfig(opts)

    try {
      await listChannels({
        url: config.url,
        token: config.token,
        json: opts.json,
        color: opts.color,
      })
    } catch (err) {
      console.error('Error:', err instanceof Error ? err.message : err)
      process.exit(1)
    }
  })

// Fetch DMs command
program
  .command('dms')
  .description('Fetch direct messages')
  .option('-u, --user <username...>', 'Filter by username (repeatable)')
  .option('-l, --limit <number>', 'Max messages to fetch', '50')
  .option('-s, --since <duration>', 'Time range: "24h", "7d", "30d"', '7d')
  .option('-c, --channel <id>', 'Specific channel ID')
  .action(async (cmdOpts) => {
    const globalOpts = program.opts()
    const config = await resolveConfig(globalOpts)

    try {
      await fetchDMs({
        url: config.url,
        token: config.token,
        json: globalOpts.json,
        color: globalOpts.color,
        user: cmdOpts.user || [],
        limit: parseInt(cmdOpts.limit, 10),
        since: cmdOpts.since,
        channel: cmdOpts.channel,
      })
    } catch (err) {
      console.error('Error:', err instanceof Error ? err.message : err)
      process.exit(1)
    }
  })

// Parse and run (use parseAsync for proper async action handling)
await program.parseAsync()
