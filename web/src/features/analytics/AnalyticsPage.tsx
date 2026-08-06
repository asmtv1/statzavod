import { useEffect, useMemo, useRef, useState } from 'react'
import { useMutation, useQuery } from '@tanstack/react-query'
import { Link } from 'react-router-dom'
import { api, type CreatorAnalytics } from '../../shared/api/client'
import { useI18n } from '../../shared/i18n/I18nProvider'
import { DailyPerformanceChart, PlatformBreakdownChart, type DailyAnalyticsPoint, type PlatformAnalyticsPoint } from './PerformanceCharts'
import styles from './AnalyticsPage.module.scss'

const today = new Date().toISOString().slice(0,10)
const monthAgo = new Date(Date.now() - 30 * 86_400_000).toISOString().slice(0,10)
const summaryMetrics = ['views','likes','comments','shares','publications'] as const
const engagementRate = (views:number, likes:number, comments:number, shares:number) => views ? `${(((likes + comments + shares) / views) * 100).toFixed(1)}% ER` : '—'

export function AnalyticsPage() {
  const { locale, t } = useI18n()
  const number = new Intl.NumberFormat(locale === 'en' ? 'en-US' : 'ru-RU')
  const summaryMetricLabel = (key: typeof summaryMetrics[number]) => key === 'views' ? t('просмотров') : key === 'likes' ? t('реакций') : key === 'comments' ? t('комментариев') : key === 'shares' ? t('репостов') : t('публикаций')
  const creators = useQuery({ queryKey:['creators', locale], queryFn:api.creators })
  const companies = useQuery({ queryKey:['companies', locale], queryFn:api.companies })
  const [from,setFrom] = useState(monthAgo)
  const [to,setTo] = useState(today)
  const [selected,setSelected] = useState<string[]>([])
  const [companyFilter,setCompanyFilter] = useState('ALL')
  const selectionInitialized = useRef(false)
  const visibleCreators = creators.data?.items.filter(creator => companyFilter === 'ALL' || (companyFilter === 'NONE' ? !creator.companyId : creator.companyId === companyFilter)) ?? []
  const report = useMutation({
    mutationFn: async () => Promise.all(selected.map(id => api.creatorAnalytics(id,from,to))),
  })

  useEffect(() => {
    if (!selectionInitialized.current && creators.data) {
      selectionInitialized.current = true
      setSelected(creators.data.items.filter(creator => creator.status !== 'DISMISSED').map(creator => creator.id))
    }
  }, [creators.data])

  const reports = report.data ?? []
  const totals = useMemo(() => Object.fromEntries(summaryMetrics.map(key => [key, reports.reduce((sum, item) => sum + (item.kpis.find(kpi => kpi.key === key)?.value ?? 0), 0)])), [reports])
  const publications = useMemo(() => reports.flatMap(item => item.publications.map(publication => ({ ...publication, creatorName:item.creatorName }))).sort((a,b) => b.publishedAt.localeCompare(a.publishedAt)),[reports])
  const dailyAnalytics = useMemo<DailyAnalyticsPoint[]>(() => {
    if (!from || !to) return []
    const byDate = new Map<string, DailyAnalyticsPoint>()
    for (const publication of publications) {
      const date = publication.publishedAt.slice(0, 10)
      const point = byDate.get(date) ?? { date, views:0, likes:0, publications:0 }
      point.views += publication.views
      point.likes += publication.likes
      point.publications += 1
      byDate.set(date, point)
    }
    const points: DailyAnalyticsPoint[] = []
    const cursor = new Date(`${from}T00:00:00`)
    const end = new Date(`${to}T00:00:00`)
    while (cursor <= end) {
      const date = `${cursor.getFullYear()}-${String(cursor.getMonth() + 1).padStart(2, '0')}-${String(cursor.getDate()).padStart(2, '0')}`
      points.push(byDate.get(date) ?? { date, views:0, likes:0, publications:0 })
      cursor.setDate(cursor.getDate() + 1)
    }
    return points
  }, [from, publications, to])
  const platformAnalytics = useMemo<PlatformAnalyticsPoint[]>(() => {
    const byPlatform = new Map<string, PlatformAnalyticsPoint>()
    for (const publication of publications) {
      const point = byPlatform.get(publication.platform) ?? { platform:publication.platform, views:0, likes:0, publications:0 }
      point.views += publication.views
      point.likes += publication.likes
      point.publications += 1
      byPlatform.set(publication.platform, point)
    }
    return [...byPlatform.values()].sort((a,b) => b.views - a.views)
  }, [publications])
  const exportQuery = new URLSearchParams({ creatorIds:selected.join(','), activityFrom:from, activityTo:to, locale })

  const toggle = (id:string) => setSelected(current => current.includes(id) ? current.filter(item => item !== id) : [...current,id])
  const selectAll = () => setSelected(visibleCreators.map(item => item.id))
  const changeCompany = (next:string) => {
    setCompanyFilter(next)
    const visibleIDs = new Set(creators.data?.items
      .filter(creator => next === 'ALL' || (next === 'NONE' ? !creator.companyId : creator.companyId === next))
      .map(creator => creator.id) ?? [])
    setSelected(current => current.filter(id => visibleIDs.has(id)))
  }
  const allVisibleSelected = visibleCreators.length > 0 && visibleCreators.every(creator => selected.includes(creator.id))

  return <section className={styles.page}>
    <header><div><p>{t('ОТЧЁТЫ И СРАВНЕНИЯ')}</p><h1>{t('Аналитика')}</h1><span>{t('Соберите статистику по одному или нескольким креаторам за нужный период.')}</span></div></header>

    <section className={styles.builder}>
      <div className={styles.period}>
        <label>{t('С')}<input type="date" value={from} onChange={event => setFrom(event.target.value)}/></label>
        <label>{t('По')}<input type="date" value={to} onChange={event => setTo(event.target.value)}/></label>
      </div>
      <div className={styles.people}>
        <div className={styles.peopleHead}><div><b>{t('Креаторы')}</b><small>{t('Выбрано:')} {selected.length}</small></div><div className={styles.peopleTools}><select aria-label={t('Компания')} value={companyFilter} onChange={event => changeCompany(event.target.value)}><option value="ALL">{t('Все компании')}</option>{companies.data?.items.map(company => <option value={company.id} key={company.id}>{company.name}</option>)}<option value="NONE">{t('Без компании')}</option></select><button onClick={allVisibleSelected ? () => setSelected([]) : selectAll}>{allVisibleSelected ? t('Снять выбор') : t('Выбрать всех')}</button></div></div>
        <div className={styles.peopleList}>{creators.isPending ? <span>{t('Загружаем список…')}</span> : visibleCreators.map(creator => <label key={creator.id}><input type="checkbox" checked={selected.includes(creator.id)} onChange={() => toggle(creator.id)}/><span><b>{creator.displayName}</b><small>{creator.companyName || t('Без компании')} · {creator.status === 'ACTIVE' ? t('Активен') : creator.status === 'ON_LEAVE' ? t('В отпуске') : t('Уволен')}</small></span></label>)}</div>
      </div>
      <div className={styles.builderActions}><span>{from && to ? `${new Date(from).toLocaleDateString(locale === 'en' ? 'en-US' : 'ru-RU')} — ${new Date(to).toLocaleDateString(locale === 'en' ? 'en-US' : 'ru-RU')}` : t('Укажите период')}</span><button onClick={() => report.mutate()} disabled={!selected.length || !from || !to || report.isPending}>{report.isPending ? t('Собираем…') : t('Собрать статистику')}</button></div>
    </section>

    {report.isError ? <div className={styles.error}>{t(report.error.message)}</div> : null}
    {!report.data ? <section className={styles.startState}><h2>{t('Отчёт ещё не собран')}</h2><p>{t('Выберите период и сотрудников выше. Здесь появятся графики, ключевые показатели и таблица публикаций.')}</p></section> : <>
      <div className={styles.resultHead}><div><p>{t('РЕЗУЛЬТАТ')}</p><h2>{reports.length === 1 ? reports[0].creatorName : `${reports.length} ${t('креаторов')}`}</h2></div><a href={`/api/v1/exports?${exportQuery.toString()}`}>{t('Скачать Excel')}</a></div>
      <section className={styles.ranking}><div><h2>{t('Итоги по креаторам')}</h2><p>{t('Суммарный результат каждого креатора по всем платформам.')}</p></div><div className={styles.rankingGrid}>
        <div className={styles.rankingColumns}><span>{t('Креатор')}</span>{summaryMetrics.map(key => <span key={key}>{summaryMetricLabel(key)}</span>)}<span className={styles.erHeader} tabIndex={0}>ER <small aria-hidden="true">?</small><span className={styles.erTooltip} role="tooltip">{t('ER — коэффициент вовлечённости: (реакции + комментарии + репосты) ÷ просмотры × 100%. Чем выше показатель, тем активнее аудитория взаимодействует с контентом.')}</span></span></div>
        {reports.length > 1 ? <article className={styles.rankingTotal}>
          <span><b>{t('Все выбранные креаторы')}</b><small>{reports.length} {reports.length === 1 ? t('креатор') : t('креаторов')} · {t('все платформы')}</small></span>
          {summaryMetrics.map(key => <span className={styles.creatorMetric} key={key}><b>{number.format(totals[key] ?? 0)}</b></span>)}
          <strong>{engagementRate(totals.views ?? 0, totals.likes ?? 0, totals.comments ?? 0, totals.shares ?? 0)}</strong>
        </article> : null}{reports.map(item => {
          const metric = (key:string) => item.kpis.find(kpi => kpi.key === key)?.value ?? 0
          const views = metric('views')
          const likes = metric('likes')
          const comments = metric('comments')
          const shares = metric('shares')
          return <Link to={`/app/creators/${item.creatorId}`} key={item.creatorId}>
            <span><b>{item.creatorName}</b><small>{t('Все платформы')}</small></span>
            {summaryMetrics.map(key => <span className={styles.creatorMetric} key={key}><b>{number.format(metric(key))}</b></span>)}
            <strong>{engagementRate(views, likes, comments, shares)}</strong>
          </Link>
        })}</div></section>
      <div className={styles.analyticsGrid}>
        <section className={styles.chartPanel}><div className={styles.panelHead}><div><h2>{t('Динамика по дням')}</h2><p>{t('Результат публикаций по дате выхода.')}</p></div></div><DailyPerformanceChart data={dailyAnalytics}/></section>
        <section className={styles.platformPanel}><div className={styles.panelHead}><div><h2>{t('Платформы')}</h2><p>{t('Вклад каждой площадки в результат.')}</p></div></div>{platformAnalytics.length ? <PlatformBreakdownChart data={platformAnalytics}/> : <div className={styles.chartEmpty}>{t('За период публикаций нет')}</div>}</section>
      </div>
      <section className={styles.publications}><div><h2>{t('Публикации')}</h2><p>{t('Все ролики выбранных креаторов за период.')}</p></div>{publications.length ? <div className={styles.table}><div className={styles.tableHead}><span>{t('Публикация')}</span><span>{t('Креатор')}</span><span>{t('Платформа')}</span><span>{t('Просмотры')}</span><span>{t('Реакции')}</span></div>{publications.map(item => <article key={item.id}><div><b>{item.title || t('Без названия')}</b><small>{new Date(item.publishedAt).toLocaleDateString(locale === 'en' ? 'en-US' : 'ru-RU')}</small></div><span>{item.creatorName}</span><span>{item.platform}</span><strong>{number.format(item.views)}</strong><strong>{number.format(item.likes)}</strong></article>)}</div> : <p className={styles.empty}>{t('За выбранный период публикаций нет.')}</p>}</section>
    </>}
  </section>
}
