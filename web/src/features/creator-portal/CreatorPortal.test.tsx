import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { cleanup, render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, describe, expect, it, vi } from 'vitest'
import type { ReactNode } from 'react'
import { MemoryRouter } from 'react-router-dom'
import { AccessProvider, authQueryKey } from '../../shared/access/AccessProvider'
import type { AuthPrincipal } from '../../shared/api/client'
import { I18nProvider } from '../../shared/i18n/I18nProvider'
import { CreatorAccountsPage, CreatorAnalyticsPage } from './CreatorPortal'

const creator: AuthPrincipal = {
  id: 'user-1', email: 'creator@example.test', role: 'CREATOR', capabilities: [], companies: [],
  creatorProfiles: [{ id: 'creator-1', companyId: 'company-1', companyName: 'Alpha', displayName: 'Alice' }],
  activeContext: { companyId: 'company-1', creatorId: 'creator-1', allCompanies: false },
}

function renderPortal(children: ReactNode, principal = creator) {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  client.setQueryData(authQueryKey, principal)
  return render(<QueryClientProvider client={client}><MemoryRouter><I18nProvider><AccessProvider>{children}</AccessProvider></I18nProvider></MemoryRouter></QueryClientProvider>)
}

afterEach(() => { cleanup(); vi.unstubAllGlobals() })

describe('creator portal', () => {
  it('uses only own portal endpoints and clears a revealed secret when the page unmounts', async () => {
    const calls: string[] = []
    vi.stubGlobal('fetch', vi.fn(async (input: RequestInfo | URL) => {
      const url = String(input); calls.push(url)
      if (url.endsWith('/auth/me')) return Response.json(creator)
      if (url.includes('/creator-portal/profile')) return Response.json({ id: 'creator-1', companyId: 'company-1', companyName: 'Alpha', displayName: 'Alice', status: 'ACTIVE', contacts: [], telegramUsername: '', workStatus: 'OK', workComment: '' })
      if (url.includes('/creator-portal/socials')) return Response.json({ items: [] })
      if (url.includes('/creator-portal/credentials/credential-1/reveal')) return Response.json({ value: 'secret-value' })
      if (url.includes('/creator-portal/credentials')) return Response.json({ items: [{ id: 'credential-1', section: 'Instagram', fieldKey: 'password', isSecret: true, hasValue: true, updatedAt: '' }] })
      return new Response('{}', { status: 404 })
    }))
    const view = renderPortal(<CreatorAccountsPage />)
    await screen.findByText('Alice')
    await userEvent.click(await screen.findByRole('button'))
    expect(await screen.findByText('secret-value')).toBeInTheDocument()
    expect(calls.some(call => call.includes('/creators/'))).toBe(false)
    view.unmount()
    renderPortal(<CreatorAccountsPage />)
    await screen.findByText('Alice')
    expect(screen.queryByText('secret-value')).not.toBeInTheDocument()
  })

  it('shows active-profile analytics and reports an export error without navigating away', async () => {
    vi.stubGlobal('fetch', vi.fn(async (input: RequestInfo | URL) => {
      const url = String(input)
      if (url.endsWith('/auth/me')) return Response.json(creator)
      if (url.includes('/creator-portal/stats')) return Response.json({ creatorId: 'creator-1', creatorName: 'Alice', period: { from: '2026-08-01', to: '2026-08-26' }, kpis: [{ key: 'views', label: 'Views', value: 42 }] })
      if (url.includes('/creator-portal/publications')) return Response.json({ creatorId: 'creator-1', period: {}, items: [] })
      if (url.includes('/creator-portal/export')) return Response.json({ detail: 'Export unavailable' }, { status: 500 })
      return new Response('{}', { status: 404 })
    }))
    renderPortal(<CreatorAnalyticsPage />)
    expect(await screen.findByText('42')).toBeInTheDocument()
    await userEvent.click(screen.getByRole('button'))
    await waitFor(() => expect(screen.getByRole('alert')).toHaveTextContent('Export unavailable'))
  })
})
