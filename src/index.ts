#!/usr/bin/env bun
// Mattermost DM CLI - Entry point

import { Command } from 'commander'
import { listChannels, fetchDMs } from './cli'

const program = new Command()

program
  .name('mm')
  .description('Mattermost DM CLI - Fetch and display direct messages')
  .version('1.0.0')

// Global options
program
  .option('-t, --token <token>', 'Mattermost personal access token', process.env.MM_TOKEN)
  .option('--url <url>', 'Mattermost server URL', process.env.MM_URL)
  .option('--json', 'Output as JSON', false)
  .option('--no-color', 'Disable colored output')

// Validate required config
function validateConfig(options: { url?: string; token?: string }): void {
  if (!options.url) {
    console.error('Error: Mattermost URL required. Set MM_URL env var or use --url')
    process.exit(1)
  }
  if (!options.token) {
    console.error('Error: Mattermost token required. Set MM_TOKEN env var or use --token')
    process.exit(1)
  }
}

// List channels command
program
  .command('channels')
  .description('List all DM channels')
  .action(async () => {
    const opts = program.opts()
    validateConfig(opts)

    try {
      await listChannels({
        url: opts.url!,
        token: opts.token!,
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
    validateConfig(globalOpts)

    try {
      await fetchDMs({
        url: globalOpts.url!,
        token: globalOpts.token!,
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
