import { FormEvent, useEffect, useRef, useState } from 'react'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { NavLink, Navigate, Route, Routes, useLocation, useNavigate } from 'react-router-dom'
import { api } from '../shared/api/client'
import { Button } from '../shared/ui/Button'
import { Dashboard } from '../features/dashboard/Dashboard'
import { CreatorsPage } from '../features/creators/CreatorsPage'
import { CompaniesPage } from '../features/companies/CompaniesPage'
import { CreatorDetailPage } from '../features/creators/CreatorDetailPage'
import { AnalyticsPage } from '../features/analytics/AnalyticsPage'
import { ContentGroupsPage, IntegrationsPage, PublicationsPage, SystemPage } from '../features/operations/OperationsPage'
import { LegalPage } from '../features/legal/LegalPages'
import { PublicPage } from '../features/public/PublicSite'
import { RequestAccessPage } from '../features/public/RequestAccessPage'
import { AcceptInvitationPage } from '../features/auth/AcceptInvitationPage'
import { LanguageToggle, useI18n } from '../shared/i18n/I18nProvider'
import styles from './App.module.scss'

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
      await client.invalidateQueries({ queryKey: ['me'] })
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

function Shell() {
  const client = useQueryClient()
  const navigate = useNavigate()
  const location = useLocation()
  const { t } = useI18n()
  const me = useQuery({ queryKey: ['me'], queryFn: api.me })

  if (me.isPending) return <main className={styles.center}>{t('Проверяем сессию…')}</main>
  if (me.isError) return <Navigate to="/login" replace state={{ from: location }} />

  const navigation = [
    ['/app', t('Обзор')],
    ['/app/companies', t('Компании')],
    ['/app/creators', t('Креаторы')],
    ['/app/analytics', t('Аналитика')],
    ['/app/publications', t('Публикации')],
    ['/app/content', t('Креативы')],
    ['/app/integrations', t('Синхронизация')],
    ['/app/system', t('Система')],
  ]

  async function logout() {
    await api.logout()
    client.clear()
    navigate('/login', { replace: true })
  }

  return <div className={styles.shell}>
    <aside>
      <div className={styles.logo}>{t('СТАТЗАВОД')}</div>
      <nav>{navigation.map(([to, label]) => <NavLink key={to} to={to} end={to === '/app'}>{label}</NavLink>)}</nav>
      <div className={styles.user}>
        <div className={styles.languageToggle}><LanguageToggle /></div>
        <b>{me.data.email}</b>
        <span>{me.data.role}</span>
        <button onClick={logout}>{t('Выйти')}</button>
      </div>
    </aside>
    <main className={styles.main}>
      <Routes>
        <Route index element={<Dashboard />} />
        <Route path="companies" element={<CompaniesPage />} />
        <Route path="creators" element={<CreatorsPage />} />
        <Route path="creators/:id" element={<CreatorDetailPage />} />
        <Route path="analytics" element={<AnalyticsPage />} />
        <Route path="publications" element={<PublicationsPage />} />
        <Route path="content" element={<ContentGroupsPage />} />
        <Route path="integrations" element={<IntegrationsPage />} />
        <Route path="system" element={<SystemPage />} />
        <Route path="*" element={<Placeholder />} />
      </Routes>
    </main>
  </div>
}

function Placeholder() {
  const { t } = useI18n()
  return <section className={styles.placeholder}>
    <p className={styles.brand}>{t('В РАБОТЕ')}</p>
    <h1>{t('Раздел готов к подключению')}</h1>
    <p>{t('Базовый API и навигация уже подготовлены. Следующий шаг — подключить данные платформы и наполнить экран.')}</p>
  </section>
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
    <Route path="/accept-invitation" element={<AcceptInvitationPage />} />
    <Route path="/app/*" element={<Shell />} />
    <Route path="*" element={<Navigate to="/" replace />} />
  </Routes>
}
