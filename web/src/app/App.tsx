import { FormEvent, lazy, Suspense, useCallback, useEffect, useId, useRef, useState, type ReactNode } from 'react'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { NavLink, Navigate, Route, Routes, useLocation, useNavigate } from 'react-router-dom'
import { api } from '../shared/api/client'
import { Button } from '../shared/ui/Button'
import { LegalPage } from '../features/legal/LegalPages'
import { PublicPage } from '../features/public/PublicSite'
import { RequestAccessPage } from '../features/public/RequestAccessPage'
import { AcceptInvitationPage } from '../features/auth/AcceptInvitationPage'
import { LanguageToggle, useI18n } from '../shared/i18n/I18nProvider'
import { AccessProvider, authQueryKey, useAccess } from '../shared/access/AccessProvider'
import type { AppRoute } from '../shared/access/access'
import { useDialogFocus } from '../shared/ui/dialogFocus'
import styles from './App.module.scss'

const Dashboard = lazy(() => import('../features/dashboard/Dashboard').then(module => ({ default: module.Dashboard })))
const CompaniesPage = lazy(() => import('../features/companies/CompaniesPage').then(module => ({ default: module.CompaniesPage })))
const CreatorsPage = lazy(() => import('../features/creators/CreatorsPage').then(module => ({ default: module.CreatorsPage })))
const CreatorDetailPage = lazy(() => import('../features/creators/CreatorDetailPage').then(module => ({ default: module.CreatorDetailPage })))
const AnalyticsPage = lazy(() => import('../features/analytics/AnalyticsPage').then(module => ({ default: module.AnalyticsPage })))
const PublicationsPage = lazy(() => import('../features/operations/OperationsPage').then(module => ({ default: module.PublicationsPage })))
const PublishingPage = lazy(() => import('../features/publishing/PublishingPage').then(module => ({ default: module.PublishingPage })))
const ContentGroupsPage = lazy(() => import('../features/operations/OperationsPage').then(module => ({ default: module.ContentGroupsPage })))
const IntegrationsPage = lazy(() => import('../features/operations/OperationsPage').then(module => ({ default: module.IntegrationsPage })))
const SystemPage = lazy(() => import('../features/operations/OperationsPage').then(module => ({ default: module.SystemPage })))
const TeamPage = lazy(() => import('../features/management/TeamPages').then(module => ({ default: module.TeamPage })))
const TeamPermissionsPage = lazy(() => import('../features/management/TeamPages').then(module => ({ default: module.TeamPermissionsPage })))
const AuditPage = lazy(() => import('../features/management/AuditPage').then(module => ({ default: module.AuditPage })))
const AccountSettingsPage = lazy(() => import('../features/settings/AccountSettingsPage').then(module => ({ default: module.AccountSettingsPage })))
const AccountDeletedPage = lazy(() => import('../features/settings/AccountSettingsPage').then(module => ({ default: module.AccountDeletedPage })))
const CreatorAccountsPage = lazy(() => import('../features/creator-portal/CreatorPortal').then(module => ({ default: module.CreatorAccountsPage })))
const CreatorAnalyticsPage = lazy(() => import('../features/creator-portal/CreatorPortal').then(module => ({ default: module.CreatorAnalyticsPage })))

type LoginLocationState = {
  from?: { pathname: string; search?: string; hash?: string }
}

function Login() {
  const [email, setEmail] = useState('')
  const [password, setPassword] = useState('')
  const [error, setError] = useState('')
  const client = useQueryClient()
  const navigate = useNavigate()
  const location = useLocation()
  const { locale, t } = useI18n()
  const localeRef = useRef(locale)

  useEffect(() => {
    localeRef.current = locale
    setError('')
  }, [locale])

  async function submit(event: FormEvent) {
    event.preventDefault()
    setError('')
    const requestLocale = locale
    try {
      await api.login(email, password)
      await client.invalidateQueries({ queryKey: authQueryKey })
      const from = (location.state as LoginLocationState | null)?.from
      navigate(from ? `${from.pathname}${from.search ?? ''}${from.hash ?? ''}` : '/app', { replace: true })
    } catch (err) {
      if (localeRef.current !== requestLocale) return
      setError(err instanceof Error ? err.message : t('Не удалось войти'))
    }
  }

  return <main className={styles.login}>
    <form onSubmit={submit}>
      <div className={styles.languageToggle}><LanguageToggle /></div>
      <p className={styles.brand}>{t('СТАТЗАВОД')}</p>
      <h1>{t('Вход в систему')}</h1>
      <label>{t('Email')}<input type="email" value={email} onChange={event => setEmail(event.target.value)} autoComplete="email" required /></label>
      <label>{t('Пароль')}<input type="password" value={password} onChange={event => setPassword(event.target.value)} autoComplete="current-password" required /></label>
      {error ? <p className={styles.formError}>{error}</p> : null}
      <Button type="submit">{t('Войти')}</Button>
    </form>
  </main>
}

function Register() {
  const { locale, t } = useI18n()
  return <main className={styles.login}>
    <form className={styles.registerForm}>
      <div className={styles.languageToggle}><LanguageToggle /></div>
      <p className={styles.brand}>{t('СТАТЗАВОД')}</p>
      <h1>{t('Регистрация')}</h1>
      <p className={styles.formLead}>{t('Создайте рабочий аккаунт для команды.')}</p>
      <label>{t('Рабочий email')}<input type="email" autoComplete="email" placeholder={locale === 'en' ? 'name@company.com' : 'name@company.ru'} /></label>
      <label>{t('Пароль')}<input type="password" autoComplete="new-password" placeholder={t('Не менее 12 символов')} /></label>
      <label>{t('Повторите пароль')}<input type="password" autoComplete="new-password" placeholder={t('Повторите пароль')} /></label>
      <Button type="button" disabled>{t('Зарегистрироваться')}</Button>
      <p className={styles.betaNote}>{t('Сервис находится в стадии beta-версии. Регистрация сейчас доступна только по приглашению.')}</p>
    </form>
  </main>
}

type NavigationDefinition = { route: AppRoute; to: string; label: string }

export function RoleGuard({ route, children }: { route: AppRoute; children: ReactNode }) {
  const access = useAccess()
  return access.canAccess(route) ? children : <Navigate to={access.firstAllowedPath} replace />
}

export function ContextSwitcher() {
  const { principal, role, isSwitching, contextError, switchContext } = useAccess()
  const { t } = useI18n()
  if (!principal || !role) return null
  const currentPrincipal = principal

  const label = role === 'CREATOR' ? t('Профиль') : t('Рабочий контекст')
  const value = role === 'CREATOR'
    ? principal.activeContext.creatorId ? `creator:${principal.activeContext.creatorId}` : ''
    : principal.activeContext.companyId ? `company:${principal.activeContext.companyId}` : 'all'

  const options = role === 'CREATOR'
    ? principal.creatorProfiles.map(profile => ({ value: `creator:${profile.id}`, label: `${profile.displayName} · ${profile.companyName}` }))
    : [
        ...(role === 'OWNER' ? [{ value: 'all', label: t('Все компании') }] : []),
        ...principal.companies.map(company => ({ value: `company:${company.id}`, label: company.name })),
      ]

  async function change(next: string) {
    if (next === 'all') return switchContext({ companyId: null })
    const [kind, id] = next.split(':')
    if (kind === 'creator') {
      const profile = currentPrincipal.creatorProfiles.find(item => item.id === id)
      if (profile) await switchContext({ companyId: profile.companyId, creatorId: profile.id })
      return
    }
    await switchContext({ companyId: id })
  }

  if (!options.length) return <p className={styles.noContext} role="status">{role === 'CREATOR' ? t('Нет доступных профилей') : t('Нет назначенных компаний')}</p>
  return <div className={styles.contextSwitcher}>
    <label><span>{label}</span><select aria-label={label} value={value} disabled={isSwitching} onChange={event => void change(event.target.value)}>{options.map(option => <option key={option.value} value={option.value}>{option.label}</option>)}</select></label>
    {contextError ? <small role="alert">{t('Контекст больше недоступен. Выберите другой.')}</small> : null}
  </div>
}

function Sidebar({ open, onClose, navigation, onLogout, notifications }: { open: boolean; onClose: () => void; navigation: NavigationDefinition[]; onLogout: () => Promise<void>; notifications: {items: import('../shared/api/client').ContentNotification[]; unreadCount: number} }) {
  const { principal, role } = useAccess()
  const { t } = useI18n()
  const drawerTitle = useId()
  const ref = useRef<HTMLElement>(null)
  useDialogFocus(open, ref, onClose)
  const roleLabel = role === 'OWNER' ? t('Владелец') : role === 'MANAGER' ? t('Менеджер') : t('Креатор')
  const notificationLabel = (kind: string) => {
    if (kind === 'APPROVAL_REQUESTED') return t('Нужно согласование публикации')
    if (kind === 'APPROVAL_DECIDED') return t('Решение по публикации')
    if (kind === 'REAUTH_REQUIRED') return t('Требуется переподключение аккаунта')
    if (kind === 'PUBLISH_FAILED') return t('Публикация завершилась ошибкой')
    return t('Публикация выполнена')
  }

  return <>
    {open ? <button type="button" className={styles.drawerBackdrop} aria-label={t('Закрыть меню')} onClick={onClose} /> : null}
    <aside ref={ref} className={open ? styles.drawerOpen : ''} role={open ? 'dialog' : undefined} aria-modal={open ? 'true' : undefined} aria-labelledby={open ? drawerTitle : undefined} tabIndex={open ? -1 : undefined}>
      <div className={styles.sidebarHeading}><div className={styles.logo} id={drawerTitle}>{t('СТАТЗАВОД')}</div><button type="button" className={styles.drawerClose} aria-label={t('Закрыть меню')} onClick={onClose}><svg viewBox="0 0 24 24" aria-hidden="true"><path d="M6 6l12 12M18 6L6 18" /></svg></button></div>
      <ContextSwitcher />
      <nav aria-label={t('Основная навигация')}>{navigation.map(item => <NavLink key={item.to} to={item.to} end={item.to === '/app'} onClick={onClose}>{item.label}{item.route === 'publishing' && notifications.unreadCount > 0 ? <span className={styles.notificationBadge} aria-label={`${notifications.unreadCount} ${t('непрочитанных уведомлений')}`}>{notifications.unreadCount}</span> : null}</NavLink>)}</nav>
      {notifications.items.length > 0 ? <section className={styles.notificationCenter} aria-label={t('Центр уведомлений')}><div><b>{t('Уведомления')}</b>{notifications.unreadCount > 0 ? <span className={styles.notificationCount}>{notifications.unreadCount}</span> : null}</div>{notifications.items.slice(0, 5).map(item => <NavLink key={item.id} to={String(item.payload.href ?? '/app/publishing')} onClick={onClose} className={item.readAt ? '' : styles.notificationUnread}>{notificationLabel(item.kind)}</NavLink>)}</section> : null}
      <div className={styles.user}>
        <div className={styles.languageToggle}><LanguageToggle /></div>
        <b>{principal?.email}</b>
        <span>{roleLabel}</span>
        <button onClick={() => void onLogout()}>{t('Выйти')}</button>
      </div>
    </aside>
  </>
}

function ShellContent() {
  const client = useQueryClient()
  const navigate = useNavigate()
  const location = useLocation()
  const { t } = useI18n()
  const access = useAccess()
  const [drawerOpen, setDrawerOpen] = useState(false)
  const notificationsQuery = useQuery({ queryKey: ['content-notifications', access.scopeKey], queryFn: () => api.notifications(), refetchInterval: 30_000, staleTime: 10_000 })
  const closeDrawer = useCallback(() => setDrawerOpen(false), [])

  useEffect(() => setDrawerOpen(false), [location.pathname])

  if (access.isPending || access.isSwitching) return <main className={styles.center}>{access.isSwitching ? t('Меняем рабочий контекст…') : t('Проверяем сессию…')}</main>
  if (access.error || !access.principal || !access.role) return <Navigate to="/login" replace state={{ from: location }} />

  const allNavigation: NavigationDefinition[] = [
    { route: 'dashboard', to: '/app', label: t('Обзор') },
    { route: 'companies', to: '/app/companies', label: t('Компании') },
    { route: 'creators', to: '/app/creators', label: t('Креаторы') },
    { route: 'analytics', to: '/app/analytics', label: t('Аналитика') },
    { route: 'publications', to: '/app/publications', label: t('Публикации') },
    { route: 'publishing', to: '/app/publishing', label: t('Планирование') },
    { route: 'content', to: '/app/content', label: t('Креативы') },
    { route: 'integrations', to: '/app/integrations', label: t('Синхронизация') },
    { route: 'system', to: '/app/system', label: t('Система') },
    { route: 'team', to: '/app/team', label: t('Команда') },
    { route: 'audit', to: '/app/audit', label: t('Аудит') },
    { route: 'settings', to: '/app/settings/account', label: t('Настройки аккаунта') },
    { route: 'own-accounts', to: '/app/my/accounts', label: t('Мои аккаунты') },
    { route: 'own-analytics', to: '/app/my/analytics', label: t('Моя статистика') },
  ]
  const navigation = allNavigation.filter(item => access.canAccess(item.route))

  async function logout() {
    await api.logout()
    client.clear()
    navigate('/login', { replace: true })
  }

  return <div className={styles.shell}>
    <div className={styles.mobileBar}><button type="button" aria-expanded={drawerOpen} aria-controls="app-navigation" aria-label={t('Открыть меню')} onClick={() => setDrawerOpen(true)}><svg viewBox="0 0 24 24" aria-hidden="true"><path d="M4 7h16M4 12h16M4 17h16" /></svg></button><span>{t('СТАТЗАВОД')}</span></div>
    <div id="app-navigation" className={styles.navigationSlot}><Sidebar open={drawerOpen} onClose={closeDrawer} navigation={navigation} onLogout={logout} notifications={notificationsQuery.data ?? {items: [], unreadCount: 0, nextBefore: ''}} /></div>
    <main className={styles.main}>
      <Suspense fallback={<div className={styles.routeLoading}>{t('Загрузка…')}</div>}><Routes>
        <Route index element={<RoleGuard route="dashboard"><Dashboard /></RoleGuard>} />
        <Route path="companies" element={<RoleGuard route="companies"><CompaniesPage /></RoleGuard>} />
        <Route path="creators" element={<RoleGuard route="creators"><CreatorsPage /></RoleGuard>} />
        <Route path="creators/:id" element={<RoleGuard route="creators"><CreatorDetailPage /></RoleGuard>} />
        <Route path="analytics" element={<RoleGuard route="analytics"><AnalyticsPage /></RoleGuard>} />
        <Route path="publications" element={<RoleGuard route="publications"><PublicationsPage /></RoleGuard>} />
        <Route path="publishing" element={<RoleGuard route="publishing"><PublishingPage /></RoleGuard>} />
        <Route path="content" element={<RoleGuard route="content"><ContentGroupsPage /></RoleGuard>} />
        <Route path="integrations" element={<RoleGuard route="integrations"><IntegrationsPage /></RoleGuard>} />
        <Route path="system" element={<RoleGuard route="system"><SystemPage /></RoleGuard>} />
        <Route path="team" element={<RoleGuard route="team"><TeamPage /></RoleGuard>} />
        <Route path="team/:userId" element={<RoleGuard route="team"><TeamPermissionsPage /></RoleGuard>} />
        <Route path="audit" element={<RoleGuard route="audit"><AuditPage /></RoleGuard>} />
        <Route path="settings/account" element={<RoleGuard route="settings"><AccountSettingsPage /></RoleGuard>} />
        <Route path="my/accounts" element={<RoleGuard route="own-accounts"><CreatorAccountsPage /></RoleGuard>} />
        <Route path="my/analytics" element={<RoleGuard route="own-analytics"><CreatorAnalyticsPage /></RoleGuard>} />
        <Route path="*" element={<Navigate to={access.firstAllowedPath} replace />} />
      </Routes></Suspense>
    </main>
  </div>
}

function Shell() {
  return <AccessProvider><ShellContent /></AccessProvider>
}

export function App() {
  return <Routes>
    <Route path="/" element={<PublicPage />} />
    <Route path="/features" element={<PublicPage page="features" />} />
    <Route path="/security" element={<PublicPage page="security" />} />
    <Route path="/support" element={<PublicPage page="support" />} />
    <Route path="/request-access" element={<RequestAccessPage />} />
    <Route path="/terms" element={<LegalPage kind="terms" />} />
    <Route path="/privacy" element={<LegalPage kind="privacy" />} />
    <Route path="/security-policy" element={<LegalPage kind="security" />} />
    <Route path="/cookies" element={<LegalPage kind="cookies" />} />
    <Route path="/personal-data-consent" element={<LegalPage kind="consent" />} />
    <Route path="/data-deletion" element={<LegalPage kind="deletion" />} />
    <Route path="/en" element={<PublicPage lang="en" />} />
    <Route path="/en/features" element={<PublicPage page="features" lang="en" />} />
    <Route path="/en/security" element={<PublicPage page="security" lang="en" />} />
    <Route path="/en/support" element={<PublicPage page="support" lang="en" />} />
    <Route path="/en/request-access" element={<RequestAccessPage lang="en" />} />
    <Route path="/en/terms" element={<LegalPage kind="terms" lang="en" />} />
    <Route path="/en/privacy" element={<LegalPage kind="privacy" lang="en" />} />
    <Route path="/en/security-policy" element={<LegalPage kind="security" lang="en" />} />
    <Route path="/en/cookies" element={<LegalPage kind="cookies" lang="en" />} />
    <Route path="/en/personal-data-consent" element={<LegalPage kind="consent" lang="en" />} />
    <Route path="/en/data-deletion" element={<LegalPage kind="deletion" lang="en" />} />
    <Route path="/en/accept-invitation" element={<AcceptInvitationPage />} />
    <Route path="/login" element={<Login />} />
    <Route path="/register" element={<Register />} />
    <Route path="/account-deleted" element={<Suspense fallback={null}><AccountDeletedPage /></Suspense>} />
    <Route path="/accept-invitation" element={<AcceptInvitationPage />} />
    <Route path="/app/*" element={<Shell />} />
    <Route path="*" element={<Navigate to="/" replace />} />
  </Routes>
}
