import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { cleanup, render, screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { MemoryRouter } from 'react-router-dom'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { api, type AuthPrincipal, type ManagerPermission, type SyncAccount } from '../../shared/api/client'
import { AccessProvider, authQueryKey } from '../../shared/access/AccessProvider'
import { I18nProvider } from '../../shared/i18n/I18nProvider'
import { IntegrationsPage } from './OperationsPage'

const accounts: SyncAccount[] = [
  { id:'warning-account', platform:'YOUTUBE', username:'warning', displayName:'Warning', profileUrl:'', creatorId:'creator-1', creatorName:'Warning creator', accountStatus:'ACTIVE', oauthStatus:'ACTIVE', health:'WARNING', message:'Синхронизация завершилась с ошибкой.', lastSyncedAt:null, tokenExpiresAt:null, consecutiveFailures:1, lastSuccessAt:null },
  { id:'error-account', platform:'INSTAGRAM', username:'error', displayName:'Error', profileUrl:'', creatorId:'creator-2', creatorName:'Error creator', accountStatus:'ERROR', oauthStatus:'REAUTH_REQUIRED', health:'ERROR', message:'Авторизация недействительна', lastSyncedAt:null, tokenExpiresAt:null, consecutiveFailures:1, lastSuccessAt:null },
]

function renderPage(capabilities: ManagerPermission[]) {
  const client = new QueryClient({ defaultOptions: { queries: { retry:false } } })
  const principal: AuthPrincipal = {
    id:'manager-1', email:'manager@example.com', role:'MANAGER', capabilities,
    companies:[{ id:'company-1', name:'Alpha', permissions:capabilities }], creatorProfiles:[],
    activeContext:{ companyId:'company-1', creatorId:null, allCompanies:false },
  }
  client.setQueryData(authQueryKey, principal)
  return render(<QueryClientProvider client={client}><MemoryRouter initialEntries={['/en/app/integrations']}><I18nProvider><AccessProvider><IntegrationsPage /></AccessProvider></I18nProvider></MemoryRouter></QueryClientProvider>)
}

afterEach(() => {
  cleanup()
  vi.restoreAllMocks()
})

describe('integration action capabilities', () => {
  it('does not render or request forbidden synchronization and OAuth actions', async () => {
    vi.spyOn(api, 'integrations').mockResolvedValue({ items:[], accounts })
    const retrySync = vi.spyOn(api, 'requestPlatformSync').mockResolvedValue(undefined)
    const authorize = vi.spyOn(api, 'startAuthorization').mockResolvedValue({ authorizationUrl:'https://example.test/oauth', expiresAt:'' })
    const user = userEvent.setup()
    renderPage([])

    expect(await screen.findByText('Warning creator')).toBeInTheDocument()
    expect(screen.queryByRole('button', { name:'Retry synchronization' })).not.toBeInTheDocument()
    expect(screen.queryByRole('button', { name:'Reconnect' })).not.toBeInTheDocument()
    expect(screen.queryByRole('button', { name:'Reconnect problematic accounts' })).not.toBeInTheDocument()

    await user.click(screen.getByRole('button', { name:/Issues/ }))
    expect(retrySync).not.toHaveBeenCalled()
    expect(authorize).not.toHaveBeenCalled()
  })

  it('maps synchronization and OAuth controls to their individual capabilities', async () => {
    vi.spyOn(api, 'integrations').mockResolvedValue({ items:[], accounts })
    const retrySync = vi.spyOn(api, 'requestPlatformSync').mockResolvedValue(undefined)
    const user = userEvent.setup()
    const { unmount } = renderPage(['SYNC_MANAGE'])

    const retry = await screen.findByRole('button', { name:'Retry synchronization' })
    expect(screen.queryByRole('button', { name:'Reconnect' })).not.toBeInTheDocument()
    expect(screen.queryByRole('button', { name:'Reconnect problematic accounts' })).not.toBeInTheDocument()
    await user.click(retry)
    expect(retrySync.mock.calls[0]?.[0]).toBe('warning-account')

    unmount()
    renderPage(['SOCIAL_CONNECT'])
    expect(await screen.findByRole('button', { name:'Reconnect' })).toBeInTheDocument()
    expect(screen.getByRole('button', { name:'Reconnect problematic accounts' })).toBeInTheDocument()
    expect(screen.queryByRole('button', { name:'Retry synchronization' })).not.toBeInTheDocument()
  })
})
