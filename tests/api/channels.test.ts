import { describe, expect, test } from 'vitest'
import {
  getOtherUserIdFromDMChannel,
  normalizeChannelName,
  resolveTeamIdFromList,
} from '../../src/api/channels'
import { setActiveMattermostCredential } from '../../src/preprocessing'
import type { Channel, Team } from '../../src/types'

function makeDMChannel(name: string): Channel {
  return {
    id: 'ch1',
    type: 'D',
    display_name: '',
    name,
    header: '',
    purpose: '',
    last_post_at: 0,
    total_msg_count: 0,
    creator_id: '',
  }
}

function makeTeam(id: string, name: string, displayName?: string): Team {
  return {
    id,
    name,
    display_name: displayName ?? name,
    type: 'O',
  }
}

describe('getOtherUserIdFromDMChannel', () => {
  test('extracts other user ID from DM channel name', () => {
    const channel = makeDMChannel('abc123__def456')
    expect(getOtherUserIdFromDMChannel(channel, 'abc123')).toBe('def456')
    expect(getOtherUserIdFromDMChannel(channel, 'def456')).toBe('abc123')
  })

  test('returns null for non-DM channels', () => {
    const channel = { ...makeDMChannel('general'), type: 'O' as const }
    expect(getOtherUserIdFromDMChannel(channel, 'abc123')).toBeNull()
  })

  test('returns null for malformed channel names', () => {
    const channel = makeDMChannel('no-separator')
    expect(getOtherUserIdFromDMChannel(channel, 'abc123')).toBeNull()
  })

  test('returns null when a name part is empty', () => {
    const channel = makeDMChannel('__def456')
    expect(getOtherUserIdFromDMChannel(channel, 'abc123')).toBeNull()
  })
})

describe('normalizeChannelName', () => {
  test('strips leading # from channel names', () => {
    expect(normalizeChannelName('#general')).toBe('general')
  })

  test('keeps names without # unchanged', () => {
    expect(normalizeChannelName('dev')).toBe('dev')
  })
})

describe('resolveTeamIdFromList', () => {
  test('returns only team id when user belongs to one team', () => {
    const teams = [makeTeam('t1', 'core', 'Core')]
    expect(resolveTeamIdFromList(teams)).toBe('t1')
  })

  test('selects team by slug name', () => {
    const teams = [makeTeam('t1', 'core', 'Core'), makeTeam('t2', 'eng', 'Engineering')]
    expect(resolveTeamIdFromList(teams, 'eng')).toBe('t2')
  })

  test('selects team by display name', () => {
    const teams = [makeTeam('t1', 'core', 'Core Team'), makeTeam('t2', 'eng', 'Engineering')]
    expect(resolveTeamIdFromList(teams, 'Core Team')).toBe('t1')
  })

  test('throws clear error when no teams', () => {
    expect(() => resolveTeamIdFromList([])).toThrow('You are not a member of any teams.')
  })

  test('throws clear error when team is missing', () => {
    const teams = [makeTeam('t1', 'core'), makeTeam('t2', 'eng')]
    expect(() => resolveTeamIdFromList(teams, 'sales')).toThrow(
      'Team "sales" not found. Your teams: core, eng',
    )
  })

  test('protects registered credentials in requested and available team labels', () => {
    const credential = 'active-team-token'
    setActiveMattermostCredential(credential)
    expect(() =>
      resolveTeamIdFromList([makeTeam('t1', credential)], `${credential}-missing`),
    ).toThrow(
      'Team "[REDACTED:mattermost_credential]-missing" not found. Your teams: [REDACTED:mattermost_credential]',
    )
  })

  test('throws clear error on multi-team without --team', () => {
    const teams = [makeTeam('t1', 'core', 'Core Team'), makeTeam('t2', 'eng', 'Engineering')]
    expect(() => resolveTeamIdFromList(teams)).toThrow(
      'You belong to multiple teams. Use --team to specify:',
    )
  })

  test('neutralizes terminal controls in team names shown in errors', () => {
    const teams = [makeTeam('t1', 'core\u001b[2J', 'Core\rSpoofed'), makeTeam('t2', 'eng')]

    expect(() => resolveTeamIdFromList(teams)).toThrow('  core\\u001b[2J (Core\\u000dSpoofed)')
  })
})
