import { describe, expect, it } from 'vitest'
import type { AuthPrincipal } from '../api/client'
import { accessScopeKey, canAccessRoute, firstAllowedPath, normalizeWorkspaceRole, routeForPath } from './access'

const principal = (overrides: Partial<AuthPrincipal> = {}): AuthPrincipal => ({
  id: 'user-1',
  email: 'person@example.com',
  role: 'OWNER',
  capabilities: [],
  companies: [],
  creatorProfiles: [],
  activeContext: { companyId: null, creatorId: null, allCompanies: true },
  ...overrides,
})

describe('access resolver', () => {
  it('normalizes legacy roles without exposing them to the shell', () => {
    expect(normalizeWorkspaceRole('ADMIN')).toBe('OWNER')
    expect(normalizeWorkspaceRole('ANALYST')).toBe('MANAGER')
    expect(normalizeWorkspaceRole('VIEWER')).toBe('MANAGER')
  })

  it('routes each role to its first permitted surface', () => {
    expect(firstAllowedPath('OWNER', [])).toBe('/app')
    expect(firstAllowedPath('MANAGER', ['STATS_VIEW'])).toBe('/app')
    expect(firstAllowedPath('MANAGER', [])).toBe('/app/companies')
    expect(firstAllowedPath('CREATOR', [])).toBe('/app/my/accounts')
  })

  it('keeps owner, manager, and creator route sets separate', () => {
    expect(canAccessRoute('OWNER', [], 'team')).toBe(true)
    expect(canAccessRoute('MANAGER', [], 'team')).toBe(false)
    expect(canAccessRoute('MANAGER', ['STATS_VIEW'], 'analytics')).toBe(true)
    expect(canAccessRoute('MANAGER', [], 'analytics')).toBe(false)
    expect(canAccessRoute('CREATOR', [], 'own-analytics')).toBe(true)
    expect(canAccessRoute('CREATOR', [], 'creators')).toBe(false)
  })

  it('derives stable, isolated query scopes', () => {
    expect(accessScopeKey(principal())).toBe('user:user-1:workspace:all')
    expect(accessScopeKey(principal({ activeContext: { companyId: 'company-1', creatorId: null, allCompanies: false } }))).toBe('user:user-1:company:company-1')
    expect(accessScopeKey(principal({ role: 'CREATOR', activeContext: { companyId: 'company-1', creatorId: 'creator-1', allCompanies: false } }))).toBe('user:user-1:creator:creator-1')
  })

  it('resolves direct URLs before a protected component mounts', () => {
    expect(routeForPath('/app/creators/creator-1')).toBe('creators')
    expect(routeForPath('/app/team/user-1')).toBe('team')
    expect(routeForPath('/app/my/accounts')).toBe('own-accounts')
    expect(routeForPath('/outside')).toBeNull()
  })
})
