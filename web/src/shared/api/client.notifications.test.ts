import { afterEach, describe, expect, it, vi } from 'vitest'
import { api } from './client'

afterEach(() => vi.unstubAllGlobals())

describe('content notifications client', () => {
  it('encodes keyset cursor and keeps deep-link payload intact', async () => {
    const fetchMock = vi.fn(async (input: RequestInfo | URL) => {
      expect(String(input)).toBe('/api/v1/notifications?before=2026-08-29T10%3A00%3A00Z%2Cnotification-1')
      return new Response(JSON.stringify({ items: [{ id: 'notification-1', payload: { href: '/app/publishing?contentItemId=item-1' } }], unreadCount: 1, nextBefore: '' }), { status: 200, headers: { 'Content-Type': 'application/json' } })
    })
    vi.stubGlobal('fetch', fetchMock)
    const result = await api.notifications('2026-08-29T10:00:00Z,notification-1')
    expect(result.unreadCount).toBe(1)
    expect(result.items[0].payload.href).toBe('/app/publishing?contentItemId=item-1')
    expect(fetchMock).toHaveBeenCalledOnce()
  })
})
