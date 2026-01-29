import { describe, it, expect, beforeAll, afterAll } from 'bun:test'
import {
  formatDate,
  formatDateLong,
  formatTime,
  formatRelativeTime,
  getDateGroupLabel,
} from './date'

describe('date utilities', () => {
  describe('formatDate', () => {
    it('formats date as DD Mon', () => {
      const date = new Date('2026-01-15T12:00:00')
      expect(formatDate(date)).toBe('15 Jan')
    })

    it('formats date with year when requested', () => {
      const date = new Date('2026-01-15T12:00:00')
      expect(formatDate(date, { includeYear: true })).toBe('15 Jan 2026')
    })
  })

  describe('formatDateLong', () => {
    it('formats with full weekday and month', () => {
      const date = new Date('2026-01-15T12:00:00')
      // Should be "Thursday, 15 January 2026"
      expect(formatDateLong(date)).toContain('Thursday')
      expect(formatDateLong(date)).toContain('January')
      expect(formatDateLong(date)).toContain('2026')
    })
  })

  describe('formatTime', () => {
    it('formats time as 24-hour HH:MM', () => {
      const date = new Date('2026-01-15T14:30:00')
      expect(formatTime(date)).toBe('14:30')
    })

    it('formats morning time correctly', () => {
      const date = new Date('2026-01-15T09:05:00')
      expect(formatTime(date)).toBe('09:05')
    })
  })

  describe('formatRelativeTime', () => {
    it('returns "just now" for very recent times', () => {
      const date = new Date(Date.now() - 30 * 1000) // 30 seconds ago
      expect(formatRelativeTime(date)).toBe('just now')
    })

    it('returns minutes ago for recent times', () => {
      const date = new Date(Date.now() - 5 * 60 * 1000) // 5 minutes ago
      expect(formatRelativeTime(date)).toBe('5 minutes ago')
    })

    it('returns hours ago', () => {
      const date = new Date(Date.now() - 3 * 60 * 60 * 1000) // 3 hours ago
      expect(formatRelativeTime(date)).toBe('3 hours ago')
    })

    it('returns days ago', () => {
      const date = new Date(Date.now() - 2 * 24 * 60 * 60 * 1000) // 2 days ago
      expect(formatRelativeTime(date)).toBe('2 days ago')
    })
  })

  describe('getDateGroupLabel', () => {
    it('returns "Today" for today\'s date', () => {
      const today = new Date()
      expect(getDateGroupLabel(today)).toBe('Today')
    })

    it('returns "Yesterday" for yesterday\'s date', () => {
      const yesterday = new Date()
      yesterday.setDate(yesterday.getDate() - 1)
      expect(getDateGroupLabel(yesterday)).toBe('Yesterday')
    })

    it('returns formatted date for older dates', () => {
      const oldDate = new Date()
      oldDate.setDate(oldDate.getDate() - 5)
      const result = getDateGroupLabel(oldDate)
      // Should be like "24 Jan" (not Today/Yesterday)
      expect(result).not.toBe('Today')
      expect(result).not.toBe('Yesterday')
      expect(result).toMatch(/\d+ \w+/)
    })
  })
})
