import { useEffect, useMemo, useState } from 'react'
import { useMutation, useQuery } from '@tanstack/react-query'
import { Link } from 'react-router-dom'
import { api, type CreatorAnalytics } from '../../shared/api/client'
import { ComparisonChart } from './ComparisonChart'
import styles from './AnalyticsPage.module.scss'

const today = new Date().toISOString().slice(0,10)
const monthAgo = new Date(Date.now() - 30 * 86_400_000).toISOString().slice(0,10)
const number = new Intl.NumberFormat('ru-RU')

export function AnalyticsPage() {
  const creators = useQuery({ queryKey:['creators'], queryFn:api.creators })
  const [from,setFrom] = useState(monthAgo)
  const [to,setTo] = useState(today)
  const [selected,setSelected] = useState<string[]>([])
  const report = useMutation({
    mutationFn: async () => Promise.all(selected.map(id => api.creatorAnalytics(id,from,to))),
  })

  useEffect(() => {
    if (!selected.length && creators.data?.items.length) setSelected(creators.data.items.filter(creator => creator.status !== 'DISMISSED').map(creator => creator.id))
  }, [creators.data, selected.length])

  const reports = report.data ?? []
  const totals = useMemo(() => {
    const keys = ['views','likes','comments','shares','publications']
    return Object.fromEntries(keys.map(key => [key,reports.reduce((sum,item) => sum + (item.kpis.find(kpi => kpi.key === key)?.value ?? 0),0)]))
  },[reports])
  const publications = useMemo(() => reports.flatMap(item => item.publications.map(publication => ({ ...publication, creatorName:item.creatorName }))).sort((a,b) => b.publishedAt.localeCompare(a.publishedAt)),[reports])
  const exportQuery = new URLSearchParams({ creatorIds:selected.join(','), activityFrom:from, activityTo:to })

  const toggle = (id:string) => setSelected(current => current.includes(id) ? current.filter(item => item !== id) : [...current,id])
  const selectAll = () => setSelected(creators.data?.items.map(item => item.id) ?? [])

  return <section className={styles.page}>
    <header><div><p>ОТЧЁТЫ И СРАВНЕНИЯ</p><h1>Аналитика</h1><span>Соберите статистику по одному или нескольким креаторам за нужный период.</span></div></header>

    <section className={styles.builder}>
      <div className={styles.period}>
        <label>С<input type="date" value={from} onChange={event => setFrom(event.target.value)}/></label>
        <label>По<input type="date" value={to} onChange={event => setTo(event.target.value)}/></label>
      </div>
      <div className={styles.people}>
        <div className={styles.peopleHead}><div><b>Креаторы</b><small>Выбрано: {selected.length}</small></div><button onClick={selected.length === creators.data?.items.length ? () => setSelected([]) : selectAll}>{selected.length === creators.data?.items.length ? 'Снять выбор' : 'Выбрать всех'}</button></div>
        <div className={styles.peopleList}>{creators.isPending ? <span>Загружаем список…</span> : creators.data?.items.map(creator => <label key={creator.id}><input type="checkbox" checked={selected.includes(creator.id)} onChange={() => toggle(creator.id)}/><span><b>{creator.displayName}</b><small>{creator.status === 'ACTIVE' ? 'Активен' : creator.status === 'ON_LEAVE' ? 'В отпуске' : 'Уволен'}</small></span></label>)}</div>
      </div>
      <div className={styles.builderActions}><span>{from && to ? `${new Date(from).toLocaleDateString('ru-RU')} — ${new Date(to).toLocaleDateString('ru-RU')}` : 'Укажите период'}</span><button onClick={() => report.mutate()} disabled={!selected.length || !from || !to || report.isPending}>{report.isPending ? 'Собираем…' : 'Собрать статистику'}</button></div>
    </section>

    {report.isError ? <div className={styles.error}>{report.error.message}</div> : null}
    {!report.data ? <section className={styles.startState}><h2>Отчёт ещё не собран</h2><p>Выберите период и сотрудников выше. Здесь появятся графики, ключевые показатели и таблица публикаций.</p></section> : <>
      <div className={styles.resultHead}><div><p>РЕЗУЛЬТАТ</p><h2>{reports.length === 1 ? reports[0].creatorName : `${reports.length} креаторов`}</h2></div><a href={`/api/v1/exports?${exportQuery.toString()}`}>Скачать Excel</a></div>
      <div className={styles.kpis}>
        {[['views','Просмотры'],['likes','Реакции'],['comments','Комментарии'],['shares','Репосты'],['publications','Публикации']].map(([key,label]) => <article key={key}><span>{label}</span><strong>{number.format(totals[key] ?? 0)}</strong></article>)}
      </div>
      <div className={styles.analyticsGrid}>
        <section className={styles.chartPanel}><div><h2>Сравнение креаторов</h2><p>Просмотры и реакции за выбранный период.</p></div><ComparisonChart reports={reports}/></section>
        <section className={styles.ranking}><div><h2>Эффективность</h2><p>Результаты по каждому креатору.</p></div>{reports.map(item => {
          const views = item.kpis.find(kpi => kpi.key === 'views')?.value ?? 0
          const likes = item.kpis.find(kpi => kpi.key === 'likes')?.value ?? 0
          return <Link to={`/app/creators/${item.creatorId}`} key={item.creatorId}><span><b>{item.creatorName}</b><small>{number.format(views)} просмотров</small></span><strong>{views ? `${((likes/views)*100).toFixed(1)}% ER` : '—'}</strong></Link>
        })}</section>
      </div>
      <section className={styles.publications}><div><h2>Публикации</h2><p>Все ролики выбранных креаторов за период.</p></div>{publications.length ? <div className={styles.table}><div className={styles.tableHead}><span>Публикация</span><span>Креатор</span><span>Платформа</span><span>Просмотры</span><span>Реакции</span></div>{publications.map(item => <article key={item.id}><div><b>{item.title || 'Без названия'}</b><small>{new Date(item.publishedAt).toLocaleDateString('ru-RU')}</small></div><span>{item.creatorName}</span><span>{item.platform}</span><strong>{number.format(item.views)}</strong><strong>{number.format(item.likes)}</strong></article>)}</div> : <p className={styles.empty}>За выбранный период публикаций нет.</p>}</section>
    </>}
  </section>
}
