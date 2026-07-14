#!/usr/bin/env bun
// Mattermost CLI - Entry point

import { Command } from 'commander'
import { isAgent } from 'is-ai-agent'
import pkg from '../package.json'
import {
  fetchChannel,
  fetchDMs,
  fetchGroupDMs,
  fetchMentions,
  fetchThread,
  listChannels,
  listTeams,
  listUsers,
  searchMessages,
  showUnread,
  showWhoAmI,
  watchChannel,
} from './cli'
import { getConfigPath, getConfigStatus, initConfigFile, resolveConfigState } from './config'
import { formatDoctorReport, runDoctor } from './doctor'
import { setActiveMattermostCredential } from './preprocessing'
import { parsePositiveSafeInteger } from './validation'

const isRunningUnderAgent = isAgent() !== null

function resolveRelative(opts: { relative?: boolean }): boolean {
  if (opts.relative !== undefined) return opts.relative
  return isRunningUnderAgent
}

function resolveRedact(opts: { redact?: boolean }, fileConfig: { redact?: boolean }): boolean {
  if (opts.redact !== undefined) return opts.redact
  if (process.env.MM_REDACT !== undefined) return process.env.MM_REDACT !== 'false'
  if (fileConfig.redact !== undefined) return fileConfig.redact
  return true
}

function validateLimit(value: string): number {
  const n = parsePositiveSafeInteger(value)
  if (n === null) {
    console.error('Error: --limit must be a positive number.')
    process.exit(1)
  }
  return n
}

function validatePeek(value?: string): number | undefined {
  if (value === undefined) return undefined
  const n = parsePositiveSafeInteger(value)
  if (n === null) {
    console.error('Error: --peek must be a positive number.')
    process.exit(1)
  }
  return n
}

function validateDuration(value: string | undefined): string | undefined {
  if (value !== undefined && !/^\d+[hdwm]$/i.test(value)) {
    console.error('Error: --since must use a duration such as "24h", "7d", "1w", or "2m".')
    process.exit(1)
  }
  return value
}

const program = new Command()

program.name('mm').description('Mattermost CLI - Fetch and display messages').version(pkg.version)

program
  .option('-t, --token <token>', 'Mattermost personal access token (or MM_TOKEN env)')
  .option('--url <url>', 'Mattermost server URL (or MM_URL env)')
  .option('--json', 'Output as JSON (JSON Lines for watch)', false)
  .option('--no-color', 'Disable colored output')
  .option(
    '-r, --relative',
    'Show times as relative (e.g., "2 days ago"); auto-enabled under AI agents',
  )
  .option('--no-relative', 'Show absolute dates/times')
  .option('--redact', 'Enable secret redaction (default)')
  .option('--no-redact', 'Disable secret redaction')
  .option('--threads', 'Show thread structure (default)')
  .option('--no-threads', 'Return selected seed posts only (except thread command)')

async function resolveConfig(options: { url?: string; token?: string }): Promise<{
  url: string
  token: string
  fileConfig: { redact?: boolean; mentionNames: string[] }
}> {
  const state = await resolveConfigState(options)
  const { url, token, fileConfig, configPath } = state
  if (token) setActiveMattermostCredential(token)
  if (state.insecurePermissions) {
    console.warn(
      `Warning: ${configPath} has insecure permissions.\n` + `  Run: chmod 600 "${configPath}"`,
    )
  }
  if (state.fileError) {
    console.warn(
      `Warning: Could not ${state.fileError === 'parse' ? 'parse' : 'read'} config at ${configPath}`,
    )
  }

  if (!url) {
    console.error(
      'Error: Mattermost URL required.\n' +
        '  1. Use --url flag\n' +
        '  2. Set MM_URL env var\n' +
        `  3. Add to ${configPath}`,
    )
    process.exit(1)
  }
  if (!token) {
    console.error(
      'Error: Mattermost token required.\n' +
        '  1. Use --token flag\n' +
        '  2. Set MM_TOKEN env var\n' +
        `  3. Add to ${configPath}`,
    )
    process.exit(1)
  }

  return {
    url,
    token,
    fileConfig: {
      redact: fileConfig.redact,
      mentionNames: fileConfig.mention_names ?? [],
    },
  }
}

program
  .command('doctor')
  .description('Check configuration, server health, and authentication')
  .action(async () => {
    const opts = program.opts()
    const config = await resolveConfigState(opts)
    const report = await runDoctor(config, {
      redact: resolveRedact(opts, config.fileConfig),
    })
    console.log(opts.json ? JSON.stringify(report) : formatDoctorReport(report))
    if (!report.ok) process.exitCode = 1
  })

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

      const status = await getConfigStatus()
      console.log(`Config path: ${status.path}`)
      console.log(`Exists: ${status.exists ? 'yes' : 'no'}`)
      if (status.exists) {
        console.log(`URL configured: ${status.hasUrl ? 'yes' : 'no'}`)
        console.log(`Token configured: ${status.hasToken ? 'yes' : 'no'}`)
        if (status.insecurePerms) {
          console.log('\nWarning: Config file has insecure permissions.')
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

program
  .command('whoami')
  .description('Show the authenticated user')
  .action(async () => {
    const opts = program.opts()
    const config = await resolveConfig(opts)
    try {
      await showWhoAmI({
        url: config.url,
        token: config.token,
        json: opts.json,
        redact: resolveRedact(opts, config.fileConfig),
      })
    } catch (err) {
      console.error('Error:', err instanceof Error ? err.message : err)
      process.exit(1)
    }
  })

program
  .command('teams')
  .description('List teams for the authenticated user')
  .action(async () => {
    const opts = program.opts()
    const config = await resolveConfig(opts)
    try {
      await listTeams({
        url: config.url,
        token: config.token,
        json: opts.json,
        redact: resolveRedact(opts, config.fileConfig),
      })
    } catch (err) {
      console.error('Error:', err instanceof Error ? err.message : err)
      process.exit(1)
    }
  })

program
  .command('users [query]')
  .description('List or search active users')
  .option('--team <name>', 'Restrict users to a team')
  .option('-l, --limit <number>', 'Max users to show', '20')
  .action(async (query, cmdOpts) => {
    const opts = program.opts()
    const config = await resolveConfig(opts)
    try {
      await listUsers({
        url: config.url,
        token: config.token,
        json: opts.json,
        redact: resolveRedact(opts, config.fileConfig),
        query,
        team: cmdOpts.team,
        limit: validateLimit(cmdOpts.limit),
      })
    } catch (err) {
      console.error('Error:', err instanceof Error ? err.message : err)
      process.exit(1)
    }
  })

program
  .command('channels')
  .description('List account-wide channels with team identity for public/private channels')
  .option('--type <type>', 'Filter by channel type: dm, public, private, group, all', 'all')
  .action(async (cmdOpts) => {
    const opts = program.opts()
    const config = await resolveConfig(opts)

    try {
      await listChannels({
        url: config.url,
        token: config.token,
        json: opts.json,
        color: opts.color,
        relative: resolveRelative(opts),
        redact: resolveRedact(opts, config.fileConfig),
        typeFilter: cmdOpts.type,
      })
    } catch (err) {
      console.error('Error:', err instanceof Error ? err.message : err)
      process.exit(1)
    }
  })

program
  .command('dms')
  .description('Fetch direct messages')
  .option('-u, --user <username...>', 'Filter by username (repeatable)')
  .option('-l, --limit <number>', 'Max total seed messages across matched DMs', '50')
  .option('-s, --since <duration>', 'Time range: "24h", "7d", "30d"', '7d')
  .option('-c, --channel <id>', 'Specific direct-message channel ID (type D only)')
  .option('--cursor <opaque>', 'Resume deterministic channel history')
  .action(async (cmdOpts, command) => {
    const globalOpts = program.opts()
    const config = await resolveConfig(globalOpts)

    try {
      await fetchDMs({
        url: config.url,
        token: config.token,
        json: globalOpts.json,
        color: globalOpts.color,
        relative: resolveRelative(globalOpts),
        redact: resolveRedact(globalOpts, config.fileConfig),
        threads: globalOpts.threads ?? true,
        user: cmdOpts.user || [],
        limit: validateLimit(cmdOpts.limit),
        since: validateDuration(cmdOpts.since) as string,
        channel: cmdOpts.channel,
        cursor: cmdOpts.cursor,
        sinceExplicit: command.getOptionValueSource('since') === 'cli',
      })
    } catch (err) {
      console.error('Error:', err instanceof Error ? err.message : err)
      process.exit(1)
    }
  })

program
  .command('group-dms')
  .description('Fetch group direct messages')
  .option('-l, --limit <number>', 'Max total seed messages across matched group DMs', '50')
  .option('-s, --since <duration>', 'Time range: "24h", "7d", "30d"', '7d')
  .option('-c, --channel <id>', 'Specific group DM channel ID (type G only)')
  .option('--cursor <opaque>', 'Resume deterministic channel history')
  .action(async (cmdOpts, command) => {
    const globalOpts = program.opts()
    const config = await resolveConfig(globalOpts)

    try {
      await fetchGroupDMs({
        url: config.url,
        token: config.token,
        json: globalOpts.json,
        color: globalOpts.color,
        relative: resolveRelative(globalOpts),
        redact: resolveRedact(globalOpts, config.fileConfig),
        threads: globalOpts.threads ?? true,
        limit: validateLimit(cmdOpts.limit),
        since: validateDuration(cmdOpts.since) as string,
        channel: cmdOpts.channel,
        cursor: cmdOpts.cursor,
        sinceExplicit: command.getOptionValueSource('since') === 'cli',
      })
    } catch (err) {
      console.error('Error:', err instanceof Error ? err.message : err)
      process.exit(1)
    }
  })

program
  .command('channel <name>')
  .description('Fetch messages from a channel by name')
  .option('--team <name>', 'Team name (auto-detected if you belong to one team)')
  .option('-l, --limit <number>', 'Max seed messages (thread context may exceed)', '50')
  .option('-s, --since <duration>', 'Time range: "24h", "7d", "30d"', '7d')
  .option('--cursor <opaque>', 'Resume deterministic channel history')
  .action(async (name, cmdOpts, command) => {
    const globalOpts = program.opts()
    const config = await resolveConfig(globalOpts)

    try {
      await fetchChannel({
        url: config.url,
        token: config.token,
        json: globalOpts.json,
        color: globalOpts.color,
        relative: resolveRelative(globalOpts),
        redact: resolveRedact(globalOpts, config.fileConfig),
        threads: globalOpts.threads ?? true,
        channel: name,
        team: cmdOpts.team,
        limit: validateLimit(cmdOpts.limit),
        since: validateDuration(cmdOpts.since) as string,
        cursor: cmdOpts.cursor,
        sinceExplicit: command.getOptionValueSource('since') === 'cli',
      })
    } catch (err) {
      console.error('Error:', err instanceof Error ? err.message : err)
      process.exit(1)
    }
  })

program
  .command('search <query>')
  .description('Search messages within one selected team')
  .option('--team <name>', 'Team name (auto-detected if you belong to one team)')
  .option('-l, --limit <number>', 'Max seed results (thread context may exceed)', '50')
  .action(async (query, cmdOpts) => {
    const globalOpts = program.opts()
    const config = await resolveConfig(globalOpts)

    try {
      await searchMessages({
        url: config.url,
        token: config.token,
        json: globalOpts.json,
        color: globalOpts.color,
        relative: resolveRelative(globalOpts),
        redact: resolveRedact(globalOpts, config.fileConfig),
        threads: globalOpts.threads ?? true,
        query,
        team: cmdOpts.team,
        limit: validateLimit(cmdOpts.limit),
      })
    } catch (err) {
      console.error('Error:', err instanceof Error ? err.message : err)
      process.exit(1)
    }
  })

program
  .command('mentions')
  .description('Find mentions within one selected team')
  .option('--team <name>', 'Team name (auto-detected if you belong to one team)')
  .option('-l, --limit <number>', 'Max seed results (thread context may exceed)', '50')
  .option('-s, --since <duration>', 'Time range: "24h", "7d", "30d"')
  .option('--channel <name>', 'Scope mentions to a channel name')
  .action(async (cmdOpts) => {
    const globalOpts = program.opts()
    const config = await resolveConfig(globalOpts)

    try {
      await fetchMentions({
        url: config.url,
        token: config.token,
        json: globalOpts.json,
        color: globalOpts.color,
        relative: resolveRelative(globalOpts),
        redact: resolveRedact(globalOpts, config.fileConfig),
        threads: globalOpts.threads ?? true,
        team: cmdOpts.team,
        limit: validateLimit(cmdOpts.limit),
        since: validateDuration(cmdOpts.since),
        channel: cmdOpts.channel,
        mentionNames: config.fileConfig.mentionNames,
      })
    } catch (err) {
      console.error('Error:', err instanceof Error ? err.message : err)
      process.exit(1)
    }
  })

program
  .command('unread')
  .description('Show channels with unread messages')
  .option('--team <name>', 'Team name (auto-detected if you belong to one team)')
  .option('--peek <number>', 'Fetch N messages from each unread channel')
  .action(async (cmdOpts) => {
    const globalOpts = program.opts()
    const config = await resolveConfig(globalOpts)

    try {
      await showUnread({
        url: config.url,
        token: config.token,
        json: globalOpts.json,
        color: globalOpts.color,
        relative: resolveRelative(globalOpts),
        redact: resolveRedact(globalOpts, config.fileConfig),
        threads: globalOpts.threads ?? true,
        team: cmdOpts.team,
        peek: validatePeek(cmdOpts.peek),
      })
    } catch (err) {
      console.error('Error:', err instanceof Error ? err.message : err)
      process.exit(1)
    }
  })

program
  .command('watch [channel]')
  .description('Watch posted events in a channel or DM with automatic reconnect')
  .option('--team <name>', 'Team name (auto-detected if you belong to one team)')
  .option('--dm <username>', 'Watch direct messages with a username')
  .action(async (channel, cmdOpts) => {
    const globalOpts = program.opts()
    const config = await resolveConfig(globalOpts)

    try {
      await watchChannel({
        url: config.url,
        token: config.token,
        json: globalOpts.json,
        color: globalOpts.color,
        relative: resolveRelative(globalOpts),
        redact: resolveRedact(globalOpts, config.fileConfig),
        threads: globalOpts.threads ?? true,
        team: cmdOpts.team,
        channel,
        dm: cmdOpts.dm,
      })
    } catch (err) {
      console.error('Error:', err instanceof Error ? err.message : err)
      process.exit(1)
    }
  })

program
  .command('thread <postId>')
  .description('Fetch and display a specific thread')
  .action(async (postId) => {
    const globalOpts = program.opts()
    const config = await resolveConfig(globalOpts)

    try {
      await fetchThread({
        url: config.url,
        token: config.token,
        json: globalOpts.json,
        color: globalOpts.color,
        relative: resolveRelative(globalOpts),
        redact: resolveRedact(globalOpts, config.fileConfig),
        threads: true,
        postId,
      })
    } catch (err) {
      console.error('Error:', err instanceof Error ? err.message : err)
      process.exit(1)
    }
  })

await program.parseAsync()
