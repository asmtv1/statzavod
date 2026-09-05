import type { ContentDraftInput, Platform } from '../../shared/api/client'

export type ComposerAccount = { id: string; platform: Platform; displayName: string; username: string; status: string }
export type TikTokCapability = { privacy: string[]; commentsDisabled: boolean; duetDisabled: boolean; stitchDisabled: boolean; warnings: string[]; requiredFields: string[]; reconnectAction?: string; compatible: boolean }
export type ComposerOptions = {
  youtubeTitle: string; youtubeDescription: string; youtubeCategoryId: string; youtubePrivacy: string; madeForKids: boolean | null; notifySubscribers: boolean | null
  vkOwner: '' | 'user' | 'community'; vkOwnerId: string; vkTitle: string; vkDescription: string; vkPrivacy: '' | 'public' | 'private'; vkWallPost: boolean
  coverFrame: string; tiktokPrivacy: string; comments: boolean | null; duet: boolean | null; stitch: boolean | null; explicitConsent: boolean; musicUsageConfirmed: boolean; brandContent: boolean; brandOrganic: boolean
}
export const emptyComposerOptions = (): ComposerOptions => ({ youtubeTitle: '', youtubeDescription: '', youtubeCategoryId: '', youtubePrivacy: '', madeForKids: null, notifySubscribers: null, vkOwner: '', vkOwnerId: '', vkTitle: '', vkDescription: '', vkPrivacy: '', vkWallPost: false, coverFrame: '00:00', tiktokPrivacy: '', comments: null, duet: null, stitch: null, explicitConsent: false, musicUsageConfirmed: false, brandContent: false, brandOrganic: false })
export function moscowScheduleToISO(value: string) { return new Date(`${value}:00+03:00`).toISOString() }
export function isFutureMoscowSchedule(value: string, now = new Date()) { return Boolean(value) && new Date(`${value}:00+03:00`).getTime() > now.getTime() }
export function draftPayload(creatorId: string, own: boolean, description: string, hashtags: string, accounts: ComposerAccount[], selected: string[], media: string, options: ComposerOptions, alternate: Record<string, string> = {}): ContentDraftInput {
  const tags = hashtags.split(/[\s,]+/).map(tag => tag.trim()).filter(Boolean); const caption = description + (tags.length ? `\n\n${tags.join(' ')}` : '')
  return { creatorId: own ? undefined : creatorId, description, hashtags: tags, platformOptions: { coverFrame: options.coverFrame }, targets: accounts.filter(account => selected.includes(account.id)).map(account => ({
    platformAccountId: account.id, platform: account.platform, mediaAssetId: alternate[account.id] || media,
    options: account.platform === 'YOUTUBE' ? { title: options.youtubeTitle, description: options.youtubeDescription || caption, categoryId: options.youtubeCategoryId, privacyStatus: options.youtubePrivacy, madeForKids: options.madeForKids, notifySubscribers: options.notifySubscribers }
      : account.platform === 'TIKTOK' ? { privacyLevel: options.tiktokPrivacy, commentsEnabled: options.comments, duetEnabled: options.duet, stitchEnabled: options.stitch, explicitConsent: options.explicitConsent, musicUsageConfirmed: options.musicUsageConfirmed, commercialDisclosure: { brandContent: options.brandContent, brandOrganic: options.brandOrganic } }
        : account.platform === 'VK' ? { owner: options.vkOwner, ownerId: Number(options.vkOwnerId), title: options.vkTitle || description, description: options.vkDescription || caption, privacy: options.vkPrivacy, wallPost: options.vkWallPost } : {}
  })) }
}
