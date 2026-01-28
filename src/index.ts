#!/usr/bin/env bun
// Mattermost DM CLI - Entry point

import { Command } from 'commander'
import { listChannels, fetchDMs } from './cli'
import pkg from '../package.json'

const program = new Command()

program
  .name('mm')
  .description('Mattermost DM CLI - Fetch and display direct messages')
  .version(pkg.version)

// Global options (don't use env vars as defaults - they leak in --help)
program
  .option('-t, --token <token>', 'Mattermost personal access token (or MM_TOKEN env)')
  .option('--url <url>', 'Mattermost server URL (or MM_URL env)')
  .option('--json', 'Output as JSON', false)
  .option('--no-color', 'Disable colored output')

// Resolve config from CLI options + env vars
function resolveConfig(options: { url?: string; token?: string }): { url: string; token: string } {
  const url = options.url || process.env.MM_URL
  const token = options.token || process.env.MM_TOKEN

  if (!url) {
    console.error('Error: Mattermost URL required. Set MM_URL env var or use --url')
    process.exit(1)
  }
  if (!token) {
    console.error('Error: Mattermost token required. Set MM_TOKEN env var or use --token')
    process.exit(1)
  }

  return { url, token }
}

// List channels command
program
  .command('channels')
  .description('List all DM channels')
  .action(async () => {
    const opts = program.opts()
    const config = resolveConfig(opts)

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
    const config = resolveConfig(globalOpts)

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

// Parse and run
program.parse()
