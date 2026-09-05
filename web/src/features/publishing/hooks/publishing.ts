import { useInfiniteQuery, useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { useCallback, useMemo } from 'react'
import { ApiError, api, type ContentApprovalPolicy, type ContentCommand, type ContentDraftInput, type ContentItem, type ContentItemSummary, type MediaUploadSession } from '../../../shared/api/client'
import { useAccess } from '../../../shared/access/AccessProvider'
import { useI18n } from '../../../shared/i18n/I18nProvider'
import { useComposerStore } from '../store/composerStore'

export type PublishingErrorKind = 'not-found' | 'forbidden' | 'conflict' | 'reauth' | 'request'

export function publishingErrorKind(error: unknown): PublishingErrorKind {
  if (!(error instanceof ApiError)) return 'request'
  if (error.status === 404) return 'not-found'
  if (error.status === 403) return 'forbidden'
  if (error.status === 409) return 'conflict'
  if (/reauth|reconnect|переподключ/i.test(error.message)) return 'reauth'
  return 'request'
}

export function publishingErrorMessage(error: unknown, t: (key: string) => string) {
  switch (publishingErrorKind(error)) {
    case 'not-found': return t('Публикация больше недоступна.')
    case 'forbidden': return t('У вас нет доступа к этой публикации.')
    case 'conflict': return t('Черновик изменился в другом окне. Обновите данные и повторите действие.')
    case 'reauth': return t('Для одной или нескольких площадок нужно переподключение.')
    default: return error instanceof Error ? error.message : t('Не удалось выполнить действие с публикацией.')
  }
}

export function publishingQueryKey(name: string, scopeKey: string, locale: string, ...parts: unknown[]) {
  return ['publishing', name, scopeKey, locale, ...parts] as const
}

export function canManageContentApprovalPolicy(role: string | null, hasContentEdit: boolean) {
  return role !== 'CREATOR' && hasContentEdit
}

export function usePublishingItems(own: boolean, range?: { from: string; until: string }) {
  const { scopeKey } = useAccess(); const { locale } = useI18n()
  return useQuery({ queryKey: publishingQueryKey('items', scopeKey, locale, own, range?.from ?? '', range?.until ?? ''), queryFn: () => own ? api.ownContentItems(range) : api.contentItems(range), refetchInterval: 30_000 })
}

export function usePublishingCalendar(own: boolean) {
  const items = usePublishingItems(own)
  const entries = useMemo(() => (items.data?.items ?? []).map(item => ({ ...item, day: item.updatedAt.slice(0, 10) })), [items.data])
  return { ...items, data: entries }
}

export function useContentItem(id: string, own: boolean) {
  const { scopeKey } = useAccess(); const { locale } = useI18n()
  return useQuery({ queryKey: publishingQueryKey('detail', scopeKey, locale, own, id), queryFn: () => own ? api.ownContentItem(id) : api.contentItem(id), enabled: Boolean(id), retry: false })
}
export function useContentAttempts(id: string, own: boolean, enabled: boolean) {
  const { scopeKey } = useAccess(); const { locale } = useI18n()
  return useInfiniteQuery({ queryKey: publishingQueryKey('attempts', scopeKey, locale, own, id), queryFn: ({ pageParam }) => api.contentAttempts(id, own, pageParam), initialPageParam:'', getNextPageParam: page => page.nextBefore || undefined, enabled: Boolean(id) && enabled, retry: false })
}

export function useContentApprovalPolicy(creatorID: string) {
  const { scopeKey, role, hasCapability } = useAccess(); const { locale } = useI18n()
  const enabled = Boolean(creatorID) && canManageContentApprovalPolicy(role, hasCapability('CONTENT_EDIT'))
  return useQuery({ queryKey: publishingQueryKey('approval-policy', scopeKey, locale, creatorID), queryFn: () => api.contentApprovalPolicy(creatorID), enabled, retry: false })
}

export function useSaveContentApprovalPolicy(creatorID: string) {
  const { scopeKey } = useAccess(); const { locale } = useI18n(); const queryClient = useQueryClient()
  return useMutation({
    mutationFn: (policy: ContentApprovalPolicy) => api.putContentApprovalPolicy(creatorID, policy),
    onSuccess: policy => queryClient.setQueryData(publishingQueryKey('approval-policy', scopeKey, locale, creatorID), policy),
  })
}

function invalidateContent(queryClient: ReturnType<typeof useQueryClient>) {
  return queryClient.invalidateQueries({ queryKey: ['publishing', 'items'] })
}

export function useContentCommands(own: boolean) {
  const queryClient = useQueryClient()
  return useMutation({
    mutationFn: ({ id, action, scheduledAt }: { id: string; action: ContentCommand; scheduledAt?: string }) => {
      if (own && (action === 'approve' || action === 'reject')) throw new ApiError(403, 'Action is not available')
      return own ? api.commandOwnContentItem(id, action as Exclude<ContentCommand, 'approve' | 'reject'>, scheduledAt) : api.commandContentItem(id, action, scheduledAt)
    },
    onSuccess: async (_result, variables) => {
      await Promise.all([
        invalidateContent(queryClient),
        queryClient.invalidateQueries({ queryKey: ['publishing', 'detail'], predicate: query => query.queryKey.includes(variables.id) }),
      ])
    },
  })
}

export function useSaveContentDraft(own: boolean) {
  const queryClient = useQueryClient()
  return useMutation({
    mutationFn: ({ id, payload, etag }: { id: string; payload: ContentDraftInput; etag: string }) => {
      return own ? api.patchOwnContentItem(id, payload, etag) : api.patchContentItem(id, payload, etag)
    },
    onSuccess: () => invalidateContent(queryClient),
  })
}

export function useContentNotifications(before?: string) {
  const { scopeKey } = useAccess(); const { locale } = useI18n()
  return useQuery({ queryKey: publishingQueryKey('notifications', scopeKey, locale, before ?? ''), queryFn: () => api.notifications(before), retry: false })
}

export function useNotificationActions() {
  const queryClient = useQueryClient()
  const invalidate = useCallback(() => queryClient.invalidateQueries({ queryKey: ['publishing', 'notifications'] }), [queryClient])
  const markRead = useMutation({ mutationFn: api.markNotificationRead, onSuccess: invalidate })
  const markAllRead = useMutation({ mutationFn: api.markAllNotificationsRead, onSuccess: invalidate })
  return { markRead, markAllRead }
}

export function useMediaUpload(own: boolean) {
  const setUpload = useComposerStore(state => state.setUpload)
  const setProgress = useComposerStore(state => state.setProgress)
  const resetUpload = useComposerStore(state => state.resetUpload)
  const upload = useCallback(async (file: File, creatorId?: string): Promise<MediaUploadSession> => {
    const session = await api.createMediaUpload({ creatorId: own ? undefined : creatorId, filename: file.name, mime: file.type || 'application/octet-stream', bytes: file.size }, own)
    setUpload(session.uploadId, 1)
    const parts: { partNumber: number; etag: string }[] = []
    try {
      const count = Math.ceil(file.size / session.partSize)
      for (let index = 0; index < count; index += 1) {
        const partNumber = index + 1
        const signed = await api.signMediaPart(session.uploadId, partNumber, own)
        const headers = new Headers()
        Object.entries(signed.headers).forEach(([key, values]) => headers.set(key, Array.isArray(values) ? values.join(',') : String(values)))
        let response: Response | undefined
        for (let attempt = 0; attempt < 3; attempt += 1) {
          response = await fetch(signed.url, { method: 'PUT', headers, body: file.slice(index * session.partSize, Math.min(file.size, (index + 1) * session.partSize)) })
          if (response.ok || response.status < 500) break
        }
        if (!response?.ok) throw new Error('Media part upload failed')
        const etag = response.headers.get('ETag')
        if (!etag) throw new Error('Storage did not return an ETag')
        parts.push({ partNumber, etag }); setProgress(Math.round((partNumber / count) * 85))
      }
      await api.completeMediaUpload(session.uploadId, parts, own); setProgress(100)
      return session
    } catch (error) {
      await api.abortMediaUpload(session.uploadId, own).catch(() => undefined)
      throw error
    } finally { resetUpload() }
  }, [own, resetUpload, setProgress, setUpload])
  return { upload }
}

export type { ContentItem, ContentItemSummary }
