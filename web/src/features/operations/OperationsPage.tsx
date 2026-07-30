import { useMutation, useQuery } from '@tanstack/react-query'
import { useMemo, useState } from 'react'
import { Link } from 'react-router-dom'
import { api, type Platform, type SyncAccount } from '../../shared/api/client'
import styles from './OperationsPage.module.scss'

export function PublicationsPage() { const result=useQuery({queryKey:['publications'],queryFn:api.publications}); return <Page title="Публикации" eyebrow="КОНТЕНТ" description="Все обнаруженные видео и их последние показатели."><DataState pending={result.isPending} error={result.isError ? result.error.message : ''} empty={!result.data?.items.length} emptyText="Публикации появятся после подключения аккаунта и первого сбора данных.">{result.data?.items.map((item)=><article className={styles.row} key={item.id}><div><b>{item.title || 'Без названия'}</b><small>{item.creatorName} · {item.platform}</small></div><strong>{new Intl.NumberFormat('ru-RU').format(item.views)} <small>просмотров</small></strong><time>{new Date(item.publishedAt).toLocaleDateString('ru-RU')}</time></article>)}</DataState></Page> }
export function ContentGroupsPage() { const groups=useQuery({queryKey:['content-groups'],queryFn:api.contentGroups});return <Page title="Креативы" eyebrow="СРАВНЕНИЕ" description="Объединяйте один ролик, опубликованный на нескольких платформах.">{groups.isPending?<div className={styles.state}>Загрузка…</div>:groups.data?.items.length?<div className={styles.list}>{groups.data.items.map(group=><article className={styles.row} key={group.id}><div><b>{group.name}</b><small>{group.creatorName}</small></div><strong>{group.publicationCount} публикаций</strong><span>{group.status}</span></article>)}</div>:<div className={styles.empty}><h2>Креативов пока нет</h2><p>После появления публикаций система покажет предложения совпадений. Объединение всегда подтверждает администратор.</p></div>}</Page> }
const healthLabels: Record<SyncAccount['health'], string> = {
  HEALTHY: 'Работает',
  WARNING: 'Внимание',
  ERROR: 'Ошибка',
  PENDING: 'Ожидает',
}

const platformNames: Record<Platform, string> = {
  YOUTUBE: 'YouTube',
  INSTAGRAM: 'Instagram',
  TIKTOK: 'TikTok',
  VK: 'VK',
}

function formatDate(value: string | null, empty = 'Ещё не запускалась') {
  if (!value) return empty
  return new Intl.DateTimeFormat('ru-RU', { day: '2-digit', month: 'short', hour: '2-digit', minute: '2-digit' }).format(new Date(value))
}

export function IntegrationsPage() {
  const [filter, setFilter] = useState<'ALL'|'PROBLEMS'|'HEALTHY'>('ALL')
  const [bulkOpen, setBulkOpen] = useState(false)
  const [bulkSelection, setBulkSelection] = useState<string[]>([])
  const result = useQuery({ queryKey: ['integrations'], queryFn: api.integrations, refetchInterval: 60_000 })
  const authorize = useMutation({
    mutationFn: ({ creatorId, platform }: { creatorId:string; platform:Platform }) => api.startAuthorization(creatorId, platform),
    onSuccess: ({ authorizationUrl }) => window.location.assign(authorizationUrl),
  })
  const accounts = result.data?.accounts ?? []
  const problemAccounts = useMemo(() => accounts.filter(account => account.health === 'ERROR' || account.health === 'WARNING'), [accounts])
  const healthy = accounts.filter(account => account.health === 'HEALTHY').length
  const expiring = accounts.filter(account => account.health === 'WARNING').length
  const visible = accounts.filter(account => filter === 'ALL' || (filter === 'PROBLEMS' ? account.health === 'ERROR' || account.health === 'WARNING' : account.health === 'HEALTHY'))
  const selectedAccounts = problemAccounts.filter(account => bulkSelection.includes(account.id))

  const toggleBulk = () => {
    const next = problemAccounts.map(account => account.id)
    setBulkSelection(next)
    setBulkOpen(true)
  }

  return <Page title="Синхронизация" eyebrow="КОНТРОЛЬ ДАННЫХ" description="Состояние всех подключённых аккаунтов — без перехода в карточки креаторов." variant={styles.syncContent}>
    {result.isPending ? <div className={styles.state}>Проверяем подключения…</div> : result.isError ? <div className={styles.error}>{result.error.message}</div> : <>
      <div className={styles.syncToolbar}>
        <div className={styles.syncMetrics}>
          <article><span>Всего аккаунтов</span><strong>{accounts.length}</strong><small>в мониторинге</small></article>
          <article><span>Работают</span><strong className={styles.healthyMetric}>{healthy}</strong><small>без ошибок</small></article>
          <article><span>Требуют внимания</span><strong className={problemAccounts.length ? styles.errorMetric : ''}>{problemAccounts.length}</strong><small>{expiring ? `${expiring} с истекающим токеном` : 'критичных проблем нет'}</small></article>
        </div>
        <button className={styles.bulkButton} onClick={toggleBulk} disabled={!problemAccounts.length}>Переподключить проблемные</button>
      </div>

      <div className={styles.syncPanel}>
        <div className={styles.syncPanelHead}>
          <div><h2>Аккаунты креаторов</h2><p>Ошибки и истекающие токены показываются первыми.</p></div>
          <div className={styles.filters} role="group" aria-label="Фильтр подключений">
            <button className={filter === 'ALL' ? styles.activeFilter : ''} onClick={() => setFilter('ALL')}>Все <span>{accounts.length}</span></button>
            <button className={filter === 'PROBLEMS' ? styles.activeFilter : ''} onClick={() => setFilter('PROBLEMS')}>Проблемы <span>{problemAccounts.length}</span></button>
            <button className={filter === 'HEALTHY' ? styles.activeFilter : ''} onClick={() => setFilter('HEALTHY')}>Работают <span>{healthy}</span></button>
          </div>
        </div>

        {visible.length ? <div className={styles.syncTable}>
          <div className={styles.syncTableHead}><span>Креатор и аккаунт</span><span>Состояние</span><span>Последняя синхронизация</span><span></span></div>
          {visible.map(account => <article className={styles.syncRow} key={account.id}>
            <div className={styles.accountIdentity}>
              <span className={`${styles.platformMark} ${styles[account.platform.toLowerCase()]}`}>{platformNames[account.platform].slice(0, 2)}</span>
              <div><Link to={`/app/creators/${account.creatorId}`}>{account.creatorName}</Link><small>{platformNames[account.platform]} · @{account.username || account.displayName}</small></div>
            </div>
            <div className={styles.healthCell}><span className={`${styles.healthBadge} ${styles[account.health.toLowerCase()]}`}><i />{healthLabels[account.health]}</span><small>{account.message}</small></div>
            <div className={styles.syncTime}><strong>{formatDate(account.lastSyncedAt)}</strong><small>{account.tokenExpiresAt ? `Токен до ${formatDate(account.tokenExpiresAt, '—')}` : 'Без срока действия'}</small></div>
            <div className={styles.rowActions}>
              {account.health !== 'HEALTHY' ? <button onClick={() => authorize.mutate({ creatorId:account.creatorId, platform:account.platform })} disabled={authorize.isPending}>{authorize.isPending && authorize.variables?.creatorId === account.creatorId && authorize.variables.platform === account.platform ? 'Переходим…' : 'Переподключить'}</button> : <Link to={`/app/creators/${account.creatorId}`}>Открыть</Link>}
            </div>
          </article>)}
        </div> : <div className={styles.syncEmpty}><h2>{accounts.length ? 'По этому фильтру ничего нет' : 'Подключений пока нет'}</h2><p>{accounts.length ? 'Выберите другой статус подключения.' : 'Подключите платформу в карточке креатора — аккаунт сразу появится в мониторинге.'}</p>{!accounts.length ? <Link to="/app/creators">Перейти к креаторам</Link> : null}</div>}
      </div>

      <div className={styles.providerStrip}>{result.data.items.map(platform => <div key={platform.id}><span>{platform.name}</span><strong>{platform.connectedAccounts}</strong><small>{platform.configured ? 'OAuth настроен' : 'OAuth не настроен'}</small></div>)}</div>
    </>}

    {bulkOpen ? <div className={styles.bulkBackdrop} onMouseDown={() => setBulkOpen(false)}>
      <section className={styles.bulkDialog} onMouseDown={event => event.stopPropagation()} role="dialog" aria-modal="true" aria-labelledby="bulk-title">
        <header><div><p>МАССОВОЕ ДЕЙСТВИЕ</p><h2 id="bulk-title">Переподключение аккаунтов</h2></div><button aria-label="Закрыть" onClick={() => setBulkOpen(false)}>×</button></header>
        <p className={styles.bulkLead}>OAuth требует подтверждения каждой платформы. Пройдите проблемные аккаунты по очереди — завершённые подключения исчезнут из списка.</p>
        <div className={styles.bulkList}>{problemAccounts.map(account => <label key={account.id}><input type="checkbox" checked={bulkSelection.includes(account.id)} onChange={() => setBulkSelection(current => current.includes(account.id) ? current.filter(id => id !== account.id) : [...current, account.id])}/><span><b>{account.creatorName}</b><small>{platformNames[account.platform]} · {account.message}</small></span></label>)}</div>
        <footer><span>Выбрано: {selectedAccounts.length}</span><button onClick={() => { const first = selectedAccounts[0]; if (first) authorize.mutate({ creatorId:first.creatorId, platform:first.platform }) }} disabled={!selectedAccounts.length || authorize.isPending}>{authorize.isPending ? 'Открываем OAuth…' : 'Начать с первого'}</button></footer>
      </section>
    </div> : null}
  </Page>
}
export function SystemPage() { const health=useQuery({queryKey:['sync-health'],queryFn:api.syncHealth}); return <Page title="Система" eyebrow="АДМИНИСТРИРОВАНИЕ" description="Состояние синхронизации и очередей."><div className={styles.cards}><article><span>Статус scheduler</span><strong>{health.data?.status === 'healthy' ? 'В норме' : 'Проверка'}</strong></article><article><span>Задач ожидает</span><strong>{health.data?.dueTargets ?? '—'}</strong></article></div></Page> }
function Page({title,eyebrow,description,children,variant=''}:{title:string;eyebrow:string;description:string;children:React.ReactNode;variant?:string}){return <section className={styles.page}><header><p>{eyebrow}</p><h1>{title}</h1><span>{description}</span></header><div className={`${styles.content} ${variant}`}>{children}</div></section>}
function DataState({pending,error,empty,emptyText,children}:{pending:boolean;error:string;empty:boolean;emptyText:string;children:React.ReactNode}) {if(pending)return <div className={styles.state}>Загрузка…</div>;if(error)return <div className={styles.error}>{error}</div>;if(empty)return <div className={styles.empty}>{emptyText}</div>;return <div className={styles.list}>{children}</div>}
