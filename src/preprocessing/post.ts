import type {
  Post,
  ProcessedAttachment,
  ProcessedFile,
  ProcessedMessage,
  ReactionActor,
  ReactionSummary,
  Redaction,
  User,
} from '../types'
import { setCanonicalPostIdentity } from '../utils/threading'
import { preprocess } from './pipeline'

const DELETED_POST_TEXT = '[deleted post]'
type UserLookup = ReadonlyMap<string, User>

function record(value: unknown): Record<string, unknown> | undefined {
  return typeof value === 'object' && value !== null && !Array.isArray(value)
    ? (value as Record<string, unknown>)
    : undefined
}

function stringValue(value: unknown, allowNumber = false): string | undefined {
  if (typeof value === 'string') return value
  if (allowNumber && typeof value === 'number' && Number.isFinite(value)) return String(value)
  return undefined
}

function arrayValue(value: unknown): unknown[] {
  return Array.isArray(value) ? value : []
}

export function postUserIds(posts: Post[]): string[] {
  const ids = new Set<string>()
  for (const post of posts) {
    if (post.user_id) ids.add(post.user_id)
    for (const candidate of arrayValue(post.metadata?.reactions)) {
      const id = stringValue(record(candidate)?.user_id)
      if (id) ids.add(id)
    }
  }
  return [...ids]
}

export function normalizePosts(
  posts: Post[],
  users: UserLookup,
  myUserId: string,
  serverUrl: string,
  buildPermalink: (serverUrl: string, postId: string) => string,
  redact: boolean,
): { messages: ProcessedMessage[]; redactions: Redaction[] } {
  const allRedactions: Redaction[] = []
  const clean = (value: string, field: string, oneLine = false): string => {
    const result = preprocess(value, { redact })
    allRedactions.push(...result.redactions.map((item) => ({ ...item, field })))
    return oneLine ? result.text.replace(/\n/g, '\\n').replace(/\t/g, '\\t') : result.text
  }

  const messages = posts.map((post): ProcessedMessage => {
    const isDeleted = post.delete_at > 0
    const props = record(post.props) ?? {}
    const rawOverride = stringValue(post.override_username) ?? stringValue(props.override_username)
    const rawUsername = users.get(post.user_id)?.username ?? post.user_id
    const displayUser = rawOverride
      ? clean(rawOverride, 'user', true)
      : !post.user_id || post.type.startsWith('system_')
        ? 'system'
        : post.user_id === myUserId
          ? 'you'
          : clean(rawUsername, 'user', true)

    const rawFiles = arrayValue(post.metadata?.files).flatMap((candidate) => {
      const value = record(candidate)
      const id = stringValue(value?.id)
      return value && id ? [{ value, id }] : []
    })
    const metadataFiles = new Map(rawFiles.map(({ id, value }) => [id, value]))
    const rawFileIds = [
      ...new Set([
        ...arrayValue(post.file_ids).flatMap((value) => {
          const id = stringValue(value)
          return id ? [id] : []
        }),
        ...metadataFiles.keys(),
      ]),
    ]
    const fileDetails: ProcessedFile[] = isDeleted
      ? []
      : rawFileIds.map((rawId) => {
          const file = metadataFiles.get(rawId)
          const name = stringValue(file?.name)
          const mime = stringValue(file?.mime_type)
          const extension = stringValue(file?.extension)
          return {
            id: clean(rawId, 'file.id', true),
            ...(name ? { name: clean(name, 'file.name', true) } : {}),
            ...(mime ? { mime: clean(mime, 'file.mime', true) } : {}),
            ...(typeof file?.size === 'number' && Number.isFinite(file.size)
              ? { size: file.size }
              : {}),
            ...(extension ? { extension: clean(extension, 'file.extension', true) } : {}),
          }
        })

    const attachments: ProcessedAttachment[] = isDeleted
      ? []
      : arrayValue(props.attachments).flatMap((candidate, index) => {
          const source = record(candidate)
          if (!source) return []
          const cleanedValues = new Map<string, string | undefined>()
          const take = (key: string, oneLine = false, allowNumber = false) => {
            const cacheKey = `${key}:${oneLine}:${allowNumber}`
            if (cleanedValues.has(cacheKey)) return cleanedValues.get(cacheKey)
            const value = stringValue(source[key], allowNumber)
            const cleaned = value ? clean(value, `attachment.${index}.${key}`, oneLine) : undefined
            cleanedValues.set(cacheKey, cleaned)
            return cleaned
          }
          const fields = arrayValue(source.fields).flatMap((candidateField, fieldIndex) => {
            const field = record(candidateField)
            if (!field) return []
            const title = stringValue(field.title, true)
            const value = stringValue(field.value, true)
            if (!title && !value && typeof field.short !== 'boolean') return []
            return [
              {
                ...(title
                  ? { title: clean(title, `attachment.${index}.fields.${fieldIndex}.title`) }
                  : {}),
                ...(value
                  ? { value: clean(value, `attachment.${index}.fields.${fieldIndex}.value`) }
                  : {}),
                ...(typeof field.short === 'boolean' ? { short: field.short } : {}),
              },
            ]
          })
          const attachment: ProcessedAttachment = {
            ...(take('fallback') ? { fallback: take('fallback') } : {}),
            ...(take('pretext') ? { pretext: take('pretext') } : {}),
            ...(take('title') ? { title: take('title') } : {}),
            ...(take('title_link', true) ? { titleLink: take('title_link', true) } : {}),
            ...(take('text') ? { text: take('text') } : {}),
            ...(fields.length > 0 ? { fields } : {}),
            ...(take('footer') ? { footer: take('footer') } : {}),
            ...(take('footer_icon', true) ? { footerIcon: take('footer_icon', true) } : {}),
            ...(take('author_name') ? { authorName: take('author_name') } : {}),
            ...(take('author_link', true) ? { authorLink: take('author_link', true) } : {}),
            ...(take('author_icon', true) ? { authorIcon: take('author_icon', true) } : {}),
            ...(take('color', true) ? { color: take('color', true) } : {}),
            ...(take('image_url', true) ? { imageUrl: take('image_url', true) } : {}),
            ...(take('thumb_url', true) ? { thumbUrl: take('thumb_url', true) } : {}),
            ...(take('ts', true, true) ? { timestamp: take('ts', true, true) } : {}),
          }
          return Object.keys(attachment).length > 0 ? [attachment] : []
        })

    const reactionGroups = new Map<string, Array<{ rawId: string; actor: ReactionActor }>>()
    if (!isDeleted) {
      for (const candidate of arrayValue(post.metadata?.reactions)) {
        const reaction = record(candidate)
        const rawEmoji = stringValue(reaction?.emoji_name)
        const rawId = stringValue(reaction?.user_id)
        if (!rawEmoji || !rawId) continue
        const username = users.get(rawId)?.username
        const actor: ReactionActor = {
          id: clean(rawId, 'reaction.actor.id', true),
          ...(username ? { username: clean(username, 'reaction.actor.username', true) } : {}),
        }
        const group = reactionGroups.get(rawEmoji)
        if (group) group.push({ rawId, actor })
        else reactionGroups.set(rawEmoji, [{ rawId, actor }])
      }
    }
    const reactions: ReactionSummary[] = [...reactionGroups]
      .sort(([a], [b]) => a.localeCompare(b))
      .map(([rawEmoji, entries]) => {
        entries.sort((a, b) => a.rawId.localeCompare(b.rawId))
        return {
          emoji: clean(rawEmoji, 'reaction.emoji', true),
          count: entries.length,
          actors: entries.map(({ actor }) => actor),
        }
      })

    const message: ProcessedMessage = {
      id: clean(post.id, 'post.id', true),
      permalink: clean(buildPermalink(serverUrl, post.id), 'post.permalink', true),
      user: displayUser,
      userId: clean(post.user_id, 'post.userId', true),
      text: isDeleted ? DELETED_POST_TEXT : clean(post.message, 'post.message'),
      timestamp: new Date(post.create_at),
      updatedAt: new Date(post.update_at),
      ...(post.edit_at > 0 ? { editedAt: new Date(post.edit_at) } : {}),
      ...(post.delete_at > 0 ? { deletedAt: new Date(post.delete_at) } : {}),
      isDeleted,
      postType: clean(post.type, 'post.type', true),
      isSystem: post.type.startsWith('system_') || !post.user_id,
      isPinned: post.is_pinned ?? false,
      files: isDeleted ? [] : rawFileIds.map((id) => clean(id, 'file.id', true)),
      fileDetails,
      attachments,
      reactions,
      rootId: post.root_id ? clean(post.root_id, 'post.rootId', true) : undefined,
      replyCount: post.reply_count || undefined,
    }
    setCanonicalPostIdentity(message, post.id, post.root_id || undefined)
    return message
  })
  return { messages, redactions: allRedactions }
}
