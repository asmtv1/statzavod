import { describe, expect, it } from 'vitest'
import { draftPayload, emptyComposerOptions, isFutureMoscowSchedule, moscowScheduleToISO } from './composer'
import { useComposerStore } from './store/composerStore'

const options = { ...emptyComposerOptions(), youtubeTitle: 'Title', youtubeCategoryId: '22', youtubePrivacy: 'private', madeForKids: false, notifySubscribers: false, vkOwner: 'community' as const, vkOwnerId: '42', vkPrivacy: 'public' as const, coverFrame: '00:01', tiktokPrivacy: 'SELF_ONLY', comments: true, duet: false, stitch: true, explicitConsent: true, musicUsageConfirmed: true }
const account = { id: 'tiktok-account', platform: 'TIKTOK' as const, displayName: 'TikTok', username: 'creator', status: 'ACTIVE' }

describe('publishing composer contracts', () => {
  it('converts Moscow local schedule to RFC3339 and rejects a past Moscow time', () => {
    expect(moscowScheduleToISO('2026-08-29T12:00')).toBe('2026-08-29T09:00:00.000Z')
    expect(isFutureMoscowSchedule('2026-08-29T12:00', new Date('2026-08-29T09:00:00.000Z'))).toBe(false)
  })
  it('includes every required TikTok control in its target options', () => {
    const payload = draftPayload('creator', false, 'Caption', '#one #two', [account], [account.id], 'asset', options)
    expect(payload.targets[0].options).toMatchObject({ privacyLevel: 'SELF_ONLY', commentsEnabled: true, duetEnabled: false, stitchEnabled: true, explicitConsent: true, musicUsageConfirmed: true, commercialDisclosure: { brandContent: false, brandOrganic: false } })
  })
  it('serializes exact YouTube, TikTok, and VK target schemas with alternate media', () => {
    const accounts = [account, { ...account, id: 'youtube', platform: 'YOUTUBE' as const }, { ...account, id: 'vk', platform: 'VK' as const }]
    const payload = draftPayload('creator', false, 'Caption', '#one', accounts, accounts.map(x => x.id), 'base', options, { youtube: 'youtube-media', vk: 'vk-media' })
    expect(payload.targets.find(x => x.platform === 'YOUTUBE')).toMatchObject({ mediaAssetId: 'youtube-media', options: { title: 'Title', categoryId: '22', privacyStatus: 'private', madeForKids: false, notifySubscribers: false } })
    expect(payload.targets.find(x => x.platform === 'VK')).toMatchObject({ mediaAssetId: 'vk-media', options: { owner: 'community', ownerId: 42, privacy: 'public', wallPost: false } })
  })
  it('keeps File values out of the Zustand composer store', () => {
    useComposerStore.getState().setUpload('upload-id', 45)
    expect(Object.keys(useComposerStore.getState())).not.toContain('file')
    expect(useComposerStore.getState().progress).toBe(45)
  })
})
