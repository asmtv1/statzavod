import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { act, cleanup, render, screen, waitFor } from '@testing-library/react'
import { useState, type ReactNode } from 'react'
import userEvent from '@testing-library/user-event'
import { MemoryRouter, Route, Routes } from 'react-router-dom'
import { afterEach, describe, expect, it, vi } from 'vitest'
import type { AuthPrincipal } from '../shared/api/client'
import { AccessProvider, authQueryKey, useAccess } from '../shared/access/AccessProvider'
import { authorizationDeniedEvent } from '../shared/api/client'
import { I18nProvider } from '../shared/i18n/I18nProvider'
import { Modal } from '../shared/ui/Primitives'
import { ContextSwitcher, RoleGuard } from './App'

const manager: AuthPrincipal = {
  id: 'manager-1',
  email: 'manager@example.com',
  role: 'MANAGER',
  capabilities: [],
  companies: [
    { id: 'company-1', name: 'Alpha', permissions: [] },
    { id: 'company-2', name: 'Beta', permissions: [] },
  ],
  creatorProfiles: [],
  activeContext: { companyId: 'company-1', creatorId: null, allCompanies: false },
}

function providers(client: QueryClient, children: ReactNode, entries = ['/app']) {
  return <QueryClientProvider client={client}><MemoryRouter initialEntries={entries}><I18nProvider>{children}</I18nProvider></MemoryRouter></QueryClientProvider>
}

afterEach(() => {
  cleanup()
  vi.unstubAllGlobals()
})

describe('role guards and context switcher', () => {
  it('redirects a forbidden direct route before its content mounts', async () => {
    const client = new QueryClient({ defaultOptions: { queries: { staleTime: Infinity, retry: false } } })
    client.setQueryData(authQueryKey, manager)
    render(providers(client, <AccessProvider><Routes>
      <Route path="/app/team" element={<RoleGuard route="team"><div>forbidden content</div></RoleGuard>} />
      <Route path="/app/companies" element={<div>allowed companies</div>} />
    </Routes></AccessProvider>, ['/app/team']))
    expect(await screen.findByText('allowed companies')).toBeInTheDocument()
    expect(screen.queryByText('forbidden content')).not.toBeInTheDocument()
  })

  it('drops scoped data and closes capability-only state after a 403 refresh', async () => {
    let current: AuthPrincipal = { ...manager, capabilities: ['CREDENTIAL_EDIT'] }
    vi.stubGlobal('fetch', vi.fn(async () => new Response(JSON.stringify(current), { status: 200, headers: { 'Content-Type': 'application/json' } })))
    const client = new QueryClient({ defaultOptions: { queries: { retry: false } } })
    client.setQueryData(authQueryKey, current)
    client.setQueryData(['creator-credentials', 'creator-1', 'old-scope'], { secret: 'must disappear' })
    function Probe() {
      const { hasCapability } = useAccess()
      return <span>{hasCapability('CREDENTIAL_EDIT') ? 'Can edit credentials' : 'Cannot edit credentials'}</span>
    }
    render(providers(client, <AccessProvider><Probe /></AccessProvider>))
    expect(await screen.findByText('Can edit credentials')).toBeInTheDocument()
    current = { ...current, capabilities: [] }
    await act(async () => window.dispatchEvent(new Event(authorizationDeniedEvent)))
    await waitFor(() => expect(screen.getByText('Cannot edit credentials')).toBeInTheDocument())
    expect(client.getQueryData(['creator-credentials', 'creator-1', 'old-scope'])).toBeUndefined()
  })

  it('posts a company switch and exposes the new selection only after refresh', async () => {
    let current = manager
    const requests: string[] = []
    vi.stubGlobal('fetch', vi.fn(async (_input: RequestInfo | URL, init?: RequestInit) => {
      const url = String(_input)
      requests.push(`${init?.method ?? 'GET'} ${url}`)
      if (url.endsWith('/auth/context')) {
        const body = JSON.parse(String(init?.body)) as { companyId: string }
        current = { ...current, activeContext: { companyId: body.companyId, creatorId: null, allCompanies: false } }
        return new Response(JSON.stringify({ activeContext: current.activeContext }), { status: 200, headers: { 'Content-Type': 'application/json' } })
      }
      return new Response(JSON.stringify(current), { status: 200, headers: { 'Content-Type': 'application/json' } })
    }))
    const client = new QueryClient({ defaultOptions: { queries: { retry: false } } })
    const user = userEvent.setup()
    render(providers(client, <AccessProvider><ContextSwitcher /></AccessProvider>))
    const switcher = await screen.findByRole('combobox')
    await user.selectOptions(switcher, 'company:company-2')
    await waitFor(() => expect(switcher).toHaveValue('company:company-2'))
    expect(requests).toContain('POST /api/v1/auth/context')
  })
})

describe('accessible modal', () => {
  it('traps focus, closes on Escape, and restores the trigger', async () => {
    const user = userEvent.setup()
    function Harness() {
      const [open, setOpen] = useState(false)
      return <><button onClick={() => setOpen(true)}>Open</button><Modal open={open} title="Title" closeLabel="Close" onClose={() => setOpen(false)}><button>Confirm</button></Modal></>
    }
    render(<Harness />)
    const trigger = screen.getByRole('button', { name: 'Open' })
    await user.click(trigger)
    expect(screen.getByRole('dialog')).toBeInTheDocument()
    expect(screen.getByRole('button', { name: 'Close' })).toHaveFocus()
    await user.keyboard('{Escape}')
    await waitFor(() => expect(screen.queryByRole('dialog')).not.toBeInTheDocument())
    await act(async () => undefined)
    expect(trigger).toHaveFocus()
  })

  it('keeps a controlled form field focused when an inline close callback changes on rerender', async () => {
    const user = userEvent.setup()
    function Harness() {
      const [open, setOpen] = useState(false)
      const [email, setEmail] = useState('')
      return <><button onClick={() => setOpen(true)}>Open</button><Modal open={open} title="New user" closeLabel="Close" onClose={() => setOpen(false)}><label>Email<input value={email} onChange={event => setEmail(event.target.value)} /></label></Modal></>
    }
    render(<Harness />)
    await user.click(screen.getByRole('button', { name: 'Open' }))
    const email = screen.getByRole('textbox', { name: 'Email' })
    await user.click(email)
    await user.type(email, 'manager@example.com')
    expect(email).toHaveValue('manager@example.com')
    expect(email).toHaveFocus()
  })
})
