import { describe, expect, it } from 'vitest'
import { ApiError } from '../../../shared/api/client'
import { canManageContentApprovalPolicy, publishingErrorKind, publishingQueryKey } from './publishing'

describe('publishing data layer', () => {
  it('isolates query cache entries by scope and locale', () => {
    expect(publishingQueryKey('items', 'user:1:company:a', 'ru', false)).not.toEqual(publishingQueryKey('items', 'user:1:company:b', 'ru', false))
    expect(publishingQueryKey('detail', 'user:1:company:a', 'en', false, 'item-1')).toEqual(['publishing', 'detail', 'user:1:company:a', 'en', false, 'item-1'])
  })

  it('does not expose approval controls to creators or an unassigned manager', () => {
    expect(canManageContentApprovalPolicy('CREATOR', true)).toBe(false)
    expect(canManageContentApprovalPolicy('MANAGER', false)).toBe(false)
    expect(canManageContentApprovalPolicy('MANAGER', true)).toBe(true)
  })

  it('keeps not-found, forbidden, conflicts and reconnect failures distinct', () => {
    expect(publishingErrorKind(new ApiError(404, 'missing'))).toBe('not-found')
    expect(publishingErrorKind(new ApiError(403, 'forbidden'))).toBe('forbidden')
    expect(publishingErrorKind(new ApiError(409, 'revision conflict'))).toBe('conflict')
    expect(publishingErrorKind(new ApiError(400, 'reauth required'))).toBe('reauth')
  })
})
