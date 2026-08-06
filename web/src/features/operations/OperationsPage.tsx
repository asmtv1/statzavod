import { useMutation, useQuery } from '@tanstack/react-query'
import { useMemo, useState } from 'react'
import { Link } from 'react-router-dom'
import { api, type Platform, type SyncAccount } from '../../shared/api/client'
import { useI18n } from '../../shared/i18n/I18nProvider'
import styles from './OperationsPage.module.scss'

export function PublicationsPage() {
  const { locale, t } = useI18n()
  const [companyFilter,setCompanyFilter] = useState('ALL')
  const result=useQuery({queryKey:['publications', locale],queryFn:api.publications})
  const companies=useQuery({queryKey:['companies', locale],queryFn:api.companies})
  const items=result.data?.items.filter(item => companyFilter === 'ALL' || (companyFilter === 'NONE' ? !item.companyId : item.companyId === companyFilter)) ?? []
  const formatter = new Intl.NumberFormat(locale === 'en' ? 'en-US' : 'ru-RU')
  const dateLocale = locale === 'en' ? 'en-US' : 'ru-RU'
  return <Page title={t('Публикации')} eyebrow={t('КОНТЕНТ')} description={t('Все обнаруженные видео и их последние показатели.')}>
    <div className={styles.companyToolbar}><label>{t('Компания')}<select value={companyFilter} onChange={event => setCompanyFilter(event.target.value)}><option value="ALL">{t('Все компании')}</option>{companies.data?.items.map(company => <option value={company.id} key={company.id}>{company.name}</option>)}<option value="NONE">{t('Без компании')}</option></select></label><span>{t('Публикаций:')} {items.length}</span></div>
    <DataState pending={result.isPending} error={result.isError ? result.error.message : ''} empty={!items.length} emptyText={result.data?.items.length ? t('У выбранной компании публикаций пока нет.') : t('Публикации появятся после подключения аккаунта и первого сбора данных.')}>{items.map(item => <article className={styles.row} key={item.id}><div><b>{item.title || t('Без названия')}</b><small>{item.creatorName} · {item.companyName || t('Без компании')} · {item.platform}</small></div><strong>{formatter.format(item.views)} <small>{t('просмотров')}</small></strong><time>{new Date(item.publishedAt).toLocaleDateString(dateLocale)}</time></article>)}</DataState>
  </Page>
}

export function ContentGroupsPage() {
  const { locale, t } = useI18n()
  const groups=useQuery({queryKey:['content-groups', locale],queryFn:api.contentGroups})
  return <Page title={t('Креативы')} eyebrow={t('СРАВНЕНИЕ')} description={t('Объединяйте один ролик, опубликованный на нескольких платформах.')}>{groups.isPending?<div className={styles.state}>{t('Загрузка…')}</div>:groups.data?.items.length?<div className={styles.list}>{groups.data.items.map(group=><article className={styles.row} key={group.id}><div><b>{group.name}</b><small>{group.creatorName}</small></div><strong>{group.publicationCount} {t('публикаций')}</strong><span>{t(group.status)}</span></article>)}</div>:<div className={styles.empty}><h2>{t('Креативов пока нет')}</h2><p>{t('После появления публикаций система покажет предложения совпадений. Объединение всегда подтверждает администратор.')}</p></div>}</Page>
}

const platformNames: Record<Platform, string> = { YOUTUBE: 'YouTube', INSTAGRAM: 'Instagram', TIKTOK: 'TikTok', VK: 'VK' }

function formatDate(value: string | null, locale: 'ru'|'en', empty: string) {
  if (!value) return empty
  return new Intl.DateTimeFormat(locale === 'en' ? 'en-US' : 'ru-RU', { day: '2-digit', month: 'short', hour: '2-digit', minute: '2-digit' }).format(new Date(value))
}

export function IntegrationsPage() {
  const { locale, t } = useI18n()
  const [filter, setFilter] = useState<'ALL'|'PROBLEMS'|'HEALTHY'>('ALL')
  const [bulkOpen, setBulkOpen] = useState(false)
  const [bulkSelection, setBulkSelection] = useState<string[]>([])
  const result = useQuery({ queryKey: ['integrations', locale], queryFn: api.integrations, refetchInterval: 60_000 })
  const authorize = useMutation({ mutationFn: ({ creatorId, platform }: { creatorId:string; platform:Platform }) => api.startAuthorization(creatorId, platform), onSuccess: ({ authorizationUrl }) => window.location.assign(authorizationUrl) })
  const accounts = result.data?.accounts ?? []
  const problemAccounts = useMemo(() => accounts.filter(account => account.health === 'ERROR' || account.health === 'WARNING'), [accounts])
  const healthy = accounts.filter(account => account.health === 'HEALTHY').length
  const expiring = accounts.filter(account => account.health === 'WARNING').length
  const visible = accounts.filter(account => filter === 'ALL' || (filter === 'PROBLEMS' ? account.health === 'ERROR' || account.health === 'WARNING' : account.health === 'HEALTHY'))
  const selectedAccounts = problemAccounts.filter(account => bulkSelection.includes(account.id))
  const toggleBulk = () => { setBulkSelection(problemAccounts.map(account => account.id)); setBulkOpen(true) }
  const date = (value:string|null, empty = t('Ещё не запускалась')) => formatDate(value, locale, empty)
  const healthLabel = (health: SyncAccount['health']) => health === 'HEALTHY' ? t('Работает') : health === 'WARNING' ? t('Внимание') : health === 'ERROR' ? t('Ошибка') : t('Ожидает')

  return <Page title={t('Синхронизация')} eyebrow={t('КОНТРОЛЬ ДАННЫХ')} description={t('Состояние всех подключённых аккаунтов — без перехода в карточки креаторов.')} variant={styles.syncContent}>
    {result.isPending ? <div className={styles.state}>{t('Проверяем подключения…')}</div> : result.isError ? <div className={styles.error}>{t(result.error.message)}</div> : <>
      <div className={styles.syncToolbar}>
        <div className={styles.syncMetrics}>
          <article><span>{t('Всего аккаунтов')}</span><strong>{accounts.length}</strong><small>{t('в мониторинге')}</small></article>
          <article><span>{t('Работают')}</span><strong className={styles.healthyMetric}>{healthy}</strong><small>{t('без ошибок')}</small></article>
          <article><span>{t('Требуют внимания')}</span><strong className={problemAccounts.length ? styles.errorMetric : ''}>{problemAccounts.length}</strong><small>{expiring ? `${expiring} ${t('с истекающим токеном')}` : t('критичных проблем нет')}</small></article>
        </div>
        <button className={styles.bulkButton} onClick={toggleBulk} disabled={!problemAccounts.length}>{t('Переподключить проблемные')}</button>
      </div>
      <div className={styles.syncPanel}>
        <div className={styles.syncPanelHead}>
          <div><h2>{t('Аккаунты креаторов')}</h2><p>{t('Ошибки и истекающие токены показываются первыми.')}</p></div>
          <div className={styles.filters} role="group" aria-label={t('Фильтр подключений')}>
            <button className={filter === 'ALL' ? styles.activeFilter : ''} onClick={() => setFilter('ALL')}>{t('Все')} <span>{accounts.length}</span></button>
            <button className={filter === 'PROBLEMS' ? styles.activeFilter : ''} onClick={() => setFilter('PROBLEMS')}>{t('Проблемы')} <span>{problemAccounts.length}</span></button>
            <button className={filter === 'HEALTHY' ? styles.activeFilter : ''} onClick={() => setFilter('HEALTHY')}>{t('Работают')} <span>{healthy}</span></button>
          </div>
        </div>
        {visible.length ? <div className={styles.syncTable}>
          <div className={styles.syncTableHead}><span>{t('Креатор и аккаунт')}</span><span>{t('Состояние')}</span><span>{t('Последняя синхронизация')}</span><span /></div>
          {visible.map(account => <article className={styles.syncRow} key={account.id}>
            <div className={styles.accountIdentity}><span className={`${styles.platformMark} ${styles[account.platform.toLowerCase()]}`}>{platformNames[account.platform].slice(0, 2)}</span><div><Link to={`/app/creators/${account.creatorId}`}>{account.creatorName}</Link><small>{platformNames[account.platform]} · @{account.username || account.displayName}</small></div></div>
            <div className={styles.healthCell}><span className={`${styles.healthBadge} ${styles[account.health.toLowerCase()]}`}><i />{healthLabel(account.health)}</span><small>{t(account.message)}</small></div>
            <div className={styles.syncTime}><strong>{date(account.lastSyncedAt)}</strong><small>{account.tokenExpiresAt ? `${t('Токен до')} ${date(account.tokenExpiresAt, '—')}` : t('Без срока действия')}</small></div>
            <div className={styles.rowActions}>{account.health !== 'HEALTHY' ? <button onClick={() => authorize.mutate({ creatorId:account.creatorId, platform:account.platform })} disabled={authorize.isPending}>{authorize.isPending && authorize.variables?.creatorId === account.creatorId && authorize.variables.platform === account.platform ? t('Переходим…') : t('Переподключить')}</button> : <Link to={`/app/creators/${account.creatorId}`}>{t('Открыть')}</Link>}</div>
          </article>)}
        </div> : <div className={styles.syncEmpty}><h2>{accounts.length ? t('По этому фильтру ничего нет') : t('Подключений пока нет')}</h2><p>{accounts.length ? t('Выберите другой статус подключения.') : t('Подключите платформу в карточке креатора — аккаунт сразу появится в мониторинге.')}</p>{!accounts.length ? <Link to="/app/creators">{t('Перейти к креаторам')}</Link> : null}</div>}
      </div>
      <div className={styles.providerStrip}>{result.data.items.map(platform => <div key={platform.id}><span>{t(platform.name)}</span><strong>{platform.connectedAccounts}</strong><small>{platform.configured ? t('OAuth настроен') : t('OAuth не настроен')}</small></div>)}</div>
    </>}
    {bulkOpen ? <div className={styles.bulkBackdrop} onMouseDown={() => setBulkOpen(false)}>
      <section className={styles.bulkDialog} onMouseDown={event => event.stopPropagation()} role="dialog" aria-modal="true" aria-labelledby="bulk-title">
        <header><div><p>{t('МАССОВОЕ ДЕЙСТВИЕ')}</p><h2 id="bulk-title">{t('Переподключение аккаунтов')}</h2></div><button aria-label={t('Закрыть')} onClick={() => setBulkOpen(false)}>×</button></header>
        <p className={styles.bulkLead}>{t('OAuth требует подтверждения каждой платформы. Пройдите проблемные аккаунты по очереди — завершённые подключения исчезнут из списка.')}</p>
        <div className={styles.bulkList}>{problemAccounts.map(account => <label key={account.id}><input type="checkbox" checked={bulkSelection.includes(account.id)} onChange={() => setBulkSelection(current => current.includes(account.id) ? current.filter(id => id !== account.id) : [...current, account.id])}/><span><b>{account.creatorName}</b><small>{platformNames[account.platform]} · {t(account.message)}</small></span></label>)}</div>
        <footer><span>{t('Выбрано:')} {selectedAccounts.length}</span><button onClick={() => { const first = selectedAccounts[0]; if (first) authorize.mutate({ creatorId:first.creatorId, platform:first.platform }) }} disabled={!selectedAccounts.length || authorize.isPending}>{authorize.isPending ? t('Открываем OAuth…') : t('Начать с первого')}</button></footer>
      </section>
    </div> : null}
  </Page>
}

export function SystemPage() {
  const { locale, t } = useI18n()
  const health=useQuery({queryKey:['sync-health', locale],queryFn:api.syncHealth})
  return <Page title={t('Система')} eyebrow={t('АДМИНИСТРИРОВАНИЕ')} description={t('Состояние синхронизации и очередей.')}><div className={styles.cards}><article><span>{t('Статус scheduler')}</span><strong>{health.data?.status === 'healthy' ? t('В норме') : t('Проверка')}</strong></article><article><span>{t('Задач ожидает')}</span><strong>{health.data?.dueTargets ?? '—'}</strong></article></div></Page>
}

function Page({title,eyebrow,description,children,variant=''}:{title:string;eyebrow:string;description:string;children:React.ReactNode;variant?:string}) {
  return <section className={styles.page}><header><p>{eyebrow}</p><h1>{title}</h1><span>{description}</span></header><div className={`${styles.content} ${variant}`}>{children}</div></section>
}

function DataState({pending,error,empty,emptyText,children}:{pending:boolean;error:string;empty:boolean;emptyText:string;children:React.ReactNode}) {
  const { t } = useI18n()
  if(pending)return <div className={styles.state}>{t('Загрузка…')}</div>
  if(error)return <div className={styles.error}>{t(error)}</div>
  if(empty)return <div className={styles.empty}>{t(emptyText)}</div>
  return <div className={styles.list}>{children}</div>
}
