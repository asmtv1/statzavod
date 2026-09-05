import { useCallback, useEffect, useMemo, useState, type ReactNode } from 'react'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { createContext, useContext } from 'react'
import { api, ApiError, authorizationDeniedEvent, type AuthPrincipal } from '../api/client'
import { accessScopeKey, canAccessRoute, firstAllowedPath, normalizeWorkspaceRole, type AppRoute } from './access'

export const authQueryKey = ['auth', 'me'] as const

type ContextSelection = { companyId?: string | null; creatorId?: string | null }

type AccessState = {
  principal: AuthPrincipal | null
  role: ReturnType<typeof normalizeWorkspaceRole> | null
  scopeKey: string
  isPending: boolean
  isSwitching: boolean
  error: Error | null
  contextError: string
  hasCapability: (capability: string) => boolean
  canAccess: (route: AppRoute) => boolean
  firstAllowedPath: string
  switchContext: (selection: ContextSelection) => Promise<void>
  refreshAccess: () => Promise<void>
}

const AccessContext = createContext<AccessState | null>(null)

function isContextValid(principal: AuthPrincipal) {
  const role = normalizeWorkspaceRole(principal.role)
  if (role === 'OWNER') return !principal.activeContext.companyId || principal.companies.some(company => company.id === principal.activeContext.companyId)
  if (role === 'MANAGER') return principal.companies.some(company => company.id === principal.activeContext.companyId)
  return principal.creatorProfiles.some(profile => profile.id === principal.activeContext.creatorId)
}

function fallbackContext(principal: AuthPrincipal): ContextSelection | null {
  const role = normalizeWorkspaceRole(principal.role)
  if (role === 'OWNER') return isContextValid(principal) ? null : { companyId: null }
  if (role === 'MANAGER') {
    const company = principal.companies[0]
    return company && !isContextValid(principal) ? { companyId: company.id } : null
  }
  const profile = principal.creatorProfiles[0]
  return profile && !isContextValid(principal) ? { companyId: profile.companyId, creatorId: profile.id } : null
}

export function AccessProvider({ children }: { children: ReactNode }) {
  const queryClient = useQueryClient()
  const [isSwitching, setIsSwitching] = useState(false)
  const [contextError, setContextError] = useState('')
  const me = useQuery({ queryKey: authQueryKey, queryFn: api.me, retry: false })

  const clearScopedCache = useCallback(async () => {
    await queryClient.cancelQueries({ predicate: query => query.queryKey[0] !== 'auth' })
    queryClient.removeQueries({ predicate: query => query.queryKey[0] !== 'auth' })
  }, [queryClient])

  const refreshAccess = useCallback(async () => {
    await clearScopedCache()
    // `invalidateQueries` alone is eventually consistent.  A 403 means that
    // the currently rendered controls may no longer be authorized, so wait
    // for /auth/me before consumers get a chance to keep editing/revealing.
    await queryClient.refetchQueries({ queryKey: authQueryKey, type: 'active' })
  }, [clearScopedCache, queryClient])

  const switchContext = useCallback(async (selection: ContextSelection) => {
    setIsSwitching(true)
    setContextError('')
    try {
      const result = await api.setContext(selection)
      queryClient.setQueryData<AuthPrincipal>(authQueryKey, current => current ? { ...current, activeContext: result.activeContext } : current)
      await clearScopedCache()
      await queryClient.invalidateQueries({ queryKey: authQueryKey })
    } catch (error) {
      await clearScopedCache()
      await queryClient.invalidateQueries({ queryKey: authQueryKey })
      setContextError(error instanceof Error ? error.message : 'Context is unavailable')
      if (!(error instanceof ApiError) || (error.status !== 403 && error.status !== 404)) throw error
    } finally {
      setIsSwitching(false)
    }
  }, [clearScopedCache, queryClient])

  useEffect(() => {
    if (!me.data || isSwitching) return
    const selection = fallbackContext(me.data)
    if (selection) void switchContext(selection)
  }, [isSwitching, me.data, switchContext])

  // A revoked/expired cookie can be followed by a different login without a
  // full document reload. Do not leave any tenant result available between
  // those two auth states.
  useEffect(() => {
    if (me.error) void clearScopedCache()
  }, [clearScopedCache, me.error])

  useEffect(() => {
    const denied = () => void refreshAccess()
    const focused = () => void queryClient.invalidateQueries({ queryKey: authQueryKey })
    window.addEventListener(authorizationDeniedEvent, denied)
    window.addEventListener('focus', focused)
    return () => {
      window.removeEventListener(authorizationDeniedEvent, denied)
      window.removeEventListener('focus', focused)
    }
  }, [queryClient, refreshAccess])

  const value = useMemo<AccessState>(() => {
    const principal = me.data ?? null
    const role = principal ? normalizeWorkspaceRole(principal.role) : null
    const capabilities = principal?.capabilities ?? []
    return {
      principal,
      role,
      scopeKey: principal ? accessScopeKey(principal) : 'anonymous',
      isPending: me.isPending,
      isSwitching,
      error: me.error,
      contextError,
      hasCapability: capability => role === 'OWNER' || capabilities.includes(capability),
      canAccess: route => role ? canAccessRoute(role, capabilities, route) : false,
      firstAllowedPath: role ? firstAllowedPath(role, capabilities) : '/login',
      switchContext,
      refreshAccess,
    }
  }, [contextError, isSwitching, me.data, me.error, me.isPending, refreshAccess, switchContext])

  return <AccessContext.Provider value={value}>{children}</AccessContext.Provider>
}

export function useAccess() {
  const value = useContext(AccessContext)
  if (!value) throw new Error('useAccess must be used inside AccessProvider')
  return value
}
