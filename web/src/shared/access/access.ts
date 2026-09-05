import type { AuthPrincipal, WorkspaceRole } from '../api/client'

export type AppRoute =
  | 'dashboard'
  | 'companies'
  | 'creators'
  | 'analytics'
  | 'publications'
  | 'publishing'
  | 'content'
  | 'integrations'
  | 'system'
  | 'team'
  | 'audit'
  | 'settings'
  | 'own-accounts'
  | 'own-analytics'

export type NavigationItem = { route: AppRoute; to: string; label: string }

const managementRoutes = new Set<AppRoute>(['companies', 'creators', 'content', 'integrations', 'system'])
const ownerRoutes = new Set<AppRoute>(['dashboard', ...managementRoutes, 'analytics', 'publications', 'publishing', 'team', 'audit', 'settings'])
const creatorRoutes = new Set<AppRoute>(['own-accounts', 'own-analytics', 'publishing'])

export function normalizeWorkspaceRole(role: AuthPrincipal['role']): WorkspaceRole {
  if (role === 'ADMIN') return 'OWNER'
  if (role === 'ANALYST' || role === 'VIEWER') return 'MANAGER'
  return role
}

export function canAccessRoute(role: WorkspaceRole, capabilities: readonly string[], route: AppRoute) {
  if (role === 'OWNER') return ownerRoutes.has(route)
  if (role === 'CREATOR') return creatorRoutes.has(route)
  if (route === 'dashboard' || route === 'analytics' || route === 'publications') {
    return capabilities.includes('STATS_VIEW')
  }
	if (route === 'publishing') return capabilities.includes('CONTENT_VIEW')
  return managementRoutes.has(route)
}

export function firstAllowedPath(role: WorkspaceRole, capabilities: readonly string[]) {
  if (role === 'CREATOR') return '/app/my/accounts'
  if (role === 'OWNER' || capabilities.includes('STATS_VIEW')) return '/app'
  return '/app/companies'
}

export function routeForPath(pathname: string): AppRoute | null {
  if (pathname === '/app' || pathname === '/app/') return 'dashboard'
  if (pathname.startsWith('/app/companies')) return 'companies'
  if (pathname.startsWith('/app/creators')) return 'creators'
  if (pathname.startsWith('/app/analytics')) return 'analytics'
  if (pathname.startsWith('/app/publications')) return 'publications'
  if (pathname.startsWith('/app/publishing')) return 'publishing'
  if (pathname.startsWith('/app/content')) return 'content'
  if (pathname.startsWith('/app/integrations')) return 'integrations'
  if (pathname.startsWith('/app/system')) return 'system'
  if (pathname.startsWith('/app/team')) return 'team'
  if (pathname.startsWith('/app/audit')) return 'audit'
  if (pathname.startsWith('/app/settings/account')) return 'settings'
  if (pathname.startsWith('/app/my/accounts')) return 'own-accounts'
  if (pathname.startsWith('/app/my/analytics')) return 'own-analytics'
  return null
}

export function accessScopeKey(principal: AuthPrincipal) {
  const role = normalizeWorkspaceRole(principal.role)
  // IDs are globally unique today, but the authenticated user is included as
  // well so a fresh login in the same browser can never reuse another
  // principal's React Query cache before its first refetch completes.
  const identity = `user:${principal.id}`
  if (role === 'OWNER') return principal.activeContext.companyId ? `${identity}:company:${principal.activeContext.companyId}` : `${identity}:workspace:all`
  if (role === 'MANAGER') return principal.activeContext.companyId ? `${identity}:company:${principal.activeContext.companyId}` : `${identity}:company:none`
  return principal.activeContext.creatorId ? `${identity}:creator:${principal.activeContext.creatorId}` : `${identity}:creator:none`
}
