import { useEffect, useMemo, useState, type ReactNode } from 'react'
import { useQuery } from '@tanstack/react-query'
import { api, safePublicationPermalink, type OwnPublication } from '../../shared/api/client'
import { useAccess } from '../../shared/access/AccessProvider'
import { useI18n } from '../../shared/i18n/I18nProvider'
import { PageHeader, Panel, StatusBadge } from '../../shared/ui/Primitives'
import { DailyPerformanceChart, PlatformBreakdownChart, type DailyAnalyticsPoint, type PlatformAnalyticsPoint } from '../analytics/PerformanceCharts'
import styles from './CreatorPortal.module.scss'

const today = new Date().toISOString().slice(0, 10)
const monthAgo = new Date(Date.now() - 30 * 86_400_000).toISOString().slice(0, 10)

function profileState(status: string) {
  return status === 'ACTIVE' ? 'success' : status === 'ARCHIVED' ? 'danger' : 'warning'
}

function portalKey(name: string, scopeKey: string, locale: string) {
  return ['creator-portal', name, scopeKey, locale]
}

function EmptyState({ title, children }: { title: string; children: ReactNode }) {
  return <Panel className={styles.empty}><h2>{title}</h2><p>{children}</p></Panel>
}

function CreatorPublicationTitle({ item, fallback }: { item:OwnPublication; fallback:string }) {
  const title = item.title || fallback
  const permalink = safePublicationPermalink(item.platform, item.permalink)
  return permalink ? <a href={permalink} target="_blank" rel="noopener noreferrer">{title}</a> : title
}

export function CreatorAccountsPage() {
  const { locale, t } = useI18n()
  const { scopeKey, principal } = useAccess()
  const profile = useQuery({ queryKey: portalKey('profile', scopeKey, locale), queryFn: api.ownProfile, retry: false })
  const socials = useQuery({ queryKey: portalKey('socials', scopeKey, locale), queryFn: api.ownSocials, retry: false, enabled: !!profile.data })
  const credentials = useQuery({ queryKey: portalKey('credentials', scopeKey, locale), queryFn: api.ownCredentials, retry: false, enabled: !!profile.data })
  const [revealed, setRevealed] = useState<Record<string, string>>({})
  const [revealError, setRevealError] = useState('')

  // Secrets must not survive a profile switch, a route transition, or unmount.
  useEffect(() => { setRevealed({}); setRevealError('') }, [scopeKey])
  useEffect(() => () => setRevealed({}), [])

  async function reveal(id: string) {
    setRevealError('')
    if (revealed[id]) {
      setRevealed(current => { const next = { ...current }; delete next[id]; return next })
      return
    }
    try {
      const result = await api.revealOwnCredential(id)
      setRevealed(current => ({ ...current, [id]: result.value }))
    } catch (error) { setRevealError(error instanceof Error ? error.message : t('Не удалось показать пароль')) }
  }

  async function copy(value: string) {
    try { await navigator.clipboard.writeText(value) } catch { setRevealError(t('Не удалось скопировать значение')) }
  }

  if (!principal?.creatorProfiles.length) return <section className={styles.page}><PageHeader eyebrow={t('ЛИЧНЫЙ КАБИНЕТ')} title={t('Мои аккаунты')} /><EmptyState title={t('Нет назначенных профилей')}>{t('Обратитесь к владельцу рабочего пространства, чтобы получить доступ к профилю креатора.')}</EmptyState></section>
  if (profile.isError) return <section className={styles.page}><PageHeader eyebrow={t('ЛИЧНЫЙ КАБИНЕТ')} title={t('Мои аккаунты')} /><EmptyState title={t('Профиль недоступен')}>{t(profile.error.message)}</EmptyState></section>
  if (!profile.data) return <section className={styles.page}><PageHeader eyebrow={t('ЛИЧНЫЙ КАБИНЕТ')} title={t('Мои аккаунты')} /><Panel>{t('Загружаем профиль…')}</Panel></section>

  return <section className={styles.page}>
    <PageHeader eyebrow={t('ЛИЧНЫЙ КАБИНЕТ')} title={t('Мои аккаунты')} description={t('Только ваши назначенные аккаунты и данные для входа. Изменения выполняет менеджер.')}/>
    <Panel className={styles.profile}><div><span>{profile.data.companyName}</span><h2>{profile.data.displayName}</h2><p>{profile.data.workComment || t('Комментариев по работе пока нет.')}</p></div><StatusBadge tone={profileState(profile.data.status)}>{profile.data.status === 'ACTIVE' ? t('Активен') : profile.data.status === 'ARCHIVED' ? t('Архивирован') : t('Неактивен')}</StatusBadge></Panel>
    {profile.data.status === 'ARCHIVED' ? <EmptyState title={t('Профиль архивирован')}>{t('Доступ к данным этого профиля закрыт. Выберите другой профиль в меню, если он вам назначен.')}</EmptyState> : <>
      <Panel><div className={styles.sectionHead}><div><h2>{t('Подключённые соцсети')}</h2><p>{t('Список доступен только для просмотра.')}</p></div></div>
        {socials.isPending ? <p>{t('Загружаем аккаунты…')}</p> : socials.data?.items.length ? <div className={styles.socials}>{socials.data.items.map(social => <article key={social.id}><div className={styles.avatar}>{social.platform.slice(0, 1)}</div><div><b>{social.displayName || social.username}</b><span>{social.platform} · @{social.username}</span>{social.lastSyncedAt ? <small>{t('Обновлено:')} {new Date(social.lastSyncedAt).toLocaleString(locale === 'en' ? 'en-US' : 'ru-RU')}</small> : null}</div>{social.profileUrl ? <a href={social.profileUrl} target="_blank" rel="noreferrer">{t('Открыть')}</a> : null}</article>)}</div> : <p className={styles.muted}>{t('К этому профилю ещё не подключены соцсети.')}</p>}
      </Panel>
      <Panel><div className={styles.sectionHead}><div><h2>{t('Данные доступа')}</h2><p>{t('Секретные значения скрыты, пока вы сами не откроете их.')}</p></div></div>
        {revealError ? <p className={styles.error} role="alert">{revealError}</p> : null}
        {credentials.isPending ? <p>{t('Загружаем данные…')}</p> : credentials.data?.items.length ? <div className={styles.credentials}>{credentials.data.items.map(credential => <article key={credential.id}><div><b>{credential.fieldKey}</b><span>{credential.section}</span></div>{credential.hasValue ? credential.isSecret ? <div className={styles.secret}><code>{revealed[credential.id] || '••••••••'}</code><button type="button" onClick={() => void reveal(credential.id)}>{revealed[credential.id] ? t('Скрыть') : t('Показать')}</button>{revealed[credential.id] ? <button type="button" onClick={() => void copy(revealed[credential.id])}>{t('Копировать')}</button> : null}</div> : <code>{credential.value || '—'}</code> : <span className={styles.muted}>{t('Не заполнено')}</span>}</article>)}</div> : <p className={styles.muted}>{t('Данные доступа пока не добавлены.')}</p>}
      </Panel>
    </>}
  </section>
}

function publicationCharts(publications: OwnPublication[], from: string, to: string) {
  const daily = new Map<string, DailyAnalyticsPoint>()
  const platform = new Map<string, PlatformAnalyticsPoint>()
  for (const item of publications) {
    const date = item.publishedAt.slice(0, 10)
    const day = daily.get(date) ?? { date, views: 0, likes: 0, publications: 0 }
    day.views += item.views; day.likes += item.likes; day.publications += 1; daily.set(date, day)
    const platformItem = platform.get(item.platform) ?? { platform: item.platform, views: 0, likes: 0, publications: 0 }
    platformItem.views += item.views; platformItem.likes += item.likes; platformItem.publications += 1; platform.set(item.platform, platformItem)
  }
  const series: DailyAnalyticsPoint[] = []
  for (const cursor = new Date(`${from}T00:00:00`), end = new Date(`${to}T00:00:00`); cursor <= end; cursor.setDate(cursor.getDate() + 1)) {
    const date = `${cursor.getFullYear()}-${String(cursor.getMonth() + 1).padStart(2, '0')}-${String(cursor.getDate()).padStart(2, '0')}`
    series.push(daily.get(date) ?? { date, views: 0, likes: 0, publications: 0 })
  }
  return { daily: series, platforms: [...platform.values()].sort((a, b) => b.views - a.views) }
}

export function CreatorAnalyticsPage() {
  const { locale, t } = useI18n()
  const { scopeKey, principal } = useAccess()
  const [from, setFrom] = useState(monthAgo)
  const [to, setTo] = useState(today)
  const [exportError, setExportError] = useState('')
  const enabled = !!principal?.activeContext.creatorId && !!from && !!to && from <= to
  const stats = useQuery({ queryKey: [...portalKey('stats', scopeKey, locale), from, to], queryFn: () => api.ownStats(from, to), enabled, retry: false })
  const publications = useQuery({ queryKey: [...portalKey('publications', scopeKey, locale), from, to], queryFn: () => api.ownPublications(from, to), enabled, retry: false })
  const number = new Intl.NumberFormat(locale === 'en' ? 'en-US' : 'ru-RU')
  const charts = useMemo(() => publicationCharts(publications.data?.items ?? [], from, to), [from, publications.data?.items, to])

  async function exportExcel() {
    setExportError('')
    try {
      const blob = await api.exportOwnCreator(from, to)
      const url = URL.createObjectURL(blob)
      const anchor = document.createElement('a'); anchor.href = url; anchor.download = `statzavod-${from}-${to}.xlsx`; anchor.click(); URL.revokeObjectURL(url)
    } catch (error) { setExportError(error instanceof Error ? error.message : t('Не удалось подготовить Excel')) }
  }

  if (!principal?.creatorProfiles.length) return <section className={styles.page}><PageHeader eyebrow={t('ЛИЧНАЯ АНАЛИТИКА')} title={t('Моя статистика')} /><EmptyState title={t('Нет назначенных профилей')}>{t('Статистика появится после назначения профиля креатора.')}</EmptyState></section>
  return <section className={styles.page}>
    <PageHeader eyebrow={t('ЛИЧНАЯ АНАЛИТИКА')} title={t('Моя статистика')} description={t('Статистика только по активному профилю в переключателе слева.')} actions={<button className={styles.export} type="button" onClick={() => void exportExcel()} disabled={!enabled}>{t('Скачать Excel')}</button>}/>
    <Panel className={styles.filters}><label>{t('С')}<input aria-label={t('С')} type="date" value={from} max={to} onChange={event => setFrom(event.target.value)} /></label><label>{t('По')}<input aria-label={t('По')} type="date" value={to} min={from} onChange={event => setTo(event.target.value)} /></label><span>{from && to ? `${new Date(from).toLocaleDateString(locale === 'en' ? 'en-US' : 'ru-RU')} — ${new Date(to).toLocaleDateString(locale === 'en' ? 'en-US' : 'ru-RU')}` : t('Укажите период')}</span></Panel>
    {exportError ? <p className={styles.error} role="alert">{exportError}</p> : null}
    {!enabled ? <EmptyState title={t('Выберите профиль и период')}>{t('Для аналитики нужен активный профиль креатора и корректный диапазон дат.')}</EmptyState> : stats.isError || publications.isError ? <EmptyState title={t('Не удалось загрузить статистику')}>{t((stats.error ?? publications.error)?.message || '')}</EmptyState> : stats.isPending || publications.isPending ? <Panel>{t('Собираем статистику…')}</Panel> : <>
      <div className={styles.kpis}>{stats.data?.kpis.map(kpi => <Panel key={kpi.key}><span>{kpi.label}</span><strong>{number.format(kpi.value)}</strong></Panel>)}</div>
      {!publications.data?.items.length ? <EmptyState title={t('За выбранный период публикаций нет')}>{t('Попробуйте изменить период или дождитесь следующей синхронизации.')}</EmptyState> : <>
        <div className={styles.charts}><Panel><h2>{t('Динамика по дням')}</h2><DailyPerformanceChart data={charts.daily}/></Panel><Panel><h2>{t('Площадки')}</h2><PlatformBreakdownChart data={charts.platforms}/></Panel></div>
        <Panel><div className={styles.sectionHead}><div><h2>{t('Публикации')}</h2><p>{t('Результат публикаций активного профиля за выбранный период.')}</p></div></div><div className={styles.table}><div><span>{t('Публикация')}</span><span>{t('Платформа')}</span><span>{t('Просмотры')}</span><span>{t('Реакции')}</span></div>{publications.data.items.map(item => <article key={item.id}><span><CreatorPublicationTitle item={item} fallback={t('Без названия')} /><small>{new Date(item.publishedAt).toLocaleDateString(locale === 'en' ? 'en-US' : 'ru-RU')}</small></span><span>{item.platform}</span><strong>{number.format(item.views)}</strong><strong>{number.format(item.likes)}</strong></article>)}</div></Panel>
      </>}
    </>}
  </section>
}
