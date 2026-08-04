import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { useEffect, useMemo, useState } from 'react'
import { Link, useNavigate, useParams, useSearchParams } from 'react-router-dom'
import { api, type CreatorDetail, type CreatorHistoryBlock, type CreatorHistoryChange, type CreatorStatus, type CreatorWorkStatus, type Platform, type PlatformConnection } from '../../shared/api/client'
import styles from './CreatorDetailPage.module.scss'
import statusStyles from './CreatorStatus.module.scss'

const platformOptions: { id: Platform; name: string; hint: string }[] = [
  { id: 'YOUTUBE', name: 'YouTube', hint: 'Канал и YouTube Analytics' },
  { id: 'INSTAGRAM', name: 'Instagram', hint: 'Профиль, Reels и Insights' },
  { id: 'TIKTOK', name: 'TikTok', hint: 'Профиль и опубликованные видео' },
]

type CredentialField = { key: string; label: string; secret?: boolean; channelLink?: boolean }
type CredentialSection = { id: string; name: string; fields: CredentialField[]; legacy?: boolean }

const credentialSections: CredentialSection[] = [
  { id: 'GMAIL', name: 'Gmail', fields: [{ key: 'login', label: 'Логин' }, { key: 'password', label: 'Пароль', secret: true }, { key: 'phone', label: 'Телефон' }] },
  { id: 'YOUTUBE', name: 'YouTube', fields: [{ key: 'note', label: 'Способ доступа' }, { key: 'email', label: 'Почта аккаунта YouTube' }, { key: 'channel_url', label: 'Активная ссылка на канал', channelLink: true }, { key: 'access_email', label: 'Почта креатора для доступа' }, { key: 'phone', label: 'Телефон' }] },
  { id: 'INSTAGRAM', name: 'Instagram', fields: [{ key: 'login', label: 'Логин' }, { key: 'channel_url', label: 'Активная ссылка на канал', channelLink: true }, { key: 'password', label: 'Пароль', secret: true }, { key: 'phone', label: 'Телефон' }, { key: 'email', label: 'Почта' }] },
  { id: 'TIKTOK', name: 'TikTok', fields: [{ key: 'login', label: 'Логин' }, { key: 'channel_url', label: 'Активная ссылка на канал', channelLink: true }, { key: 'password', label: 'Пароль', secret: true }, { key: 'phone', label: 'Телефон' }, { key: 'email', label: 'Почта' }] },
  { id: 'VK', name: 'VK · старые данные', legacy: true, fields: [{ key: 'login', label: 'Логин' }, { key: 'password', label: 'Пароль', secret: true }, { key: 'phone', label: 'Телефон' }] },
]

function connectionPermissions(platform: Platform, scopes: string[]) {
  const labels: Record<Platform, Record<string, string>> = {
    YOUTUBE: {
      'https://www.googleapis.com/auth/youtube.readonly': 'Канал и публикации',
      'https://www.googleapis.com/auth/yt-analytics.readonly': 'Аналитика канала',
    },
    INSTAGRAM: {
      instagram_business_basic: 'Профиль и публикации',
      instagram_business_manage_insights: 'Статистика аккаунта',
	  instagram_basic: 'Профиль и публикации',
	  instagram_manage_insights: 'Статистика аккаунта',
	  pages_read_engagement: 'Связь Instagram со страницей Facebook',
	  pages_show_list: 'Связанный Instagram-аккаунт',
	  business_management: 'Доступ к страницам бизнес-портфолио',
    },
    TIKTOK: {
      'user.info.basic': 'Профиль',
      'user.info.profile': 'Расширенные данные профиля',
      'user.info.stats': 'Статистика аккаунта',
      'video.list': 'Опубликованные видео',
    },
    VK: {
      video: 'Видео и клипы',
      stats: 'Статистика аккаунта',
      offline: 'Долгосрочный доступ',
    },
  }
  const readable = scopes.map(scope => labels[platform][scope]).filter((label): label is string => Boolean(label))
  const unknownCount = scopes.length - readable.length
  if (!readable.length) return scopes.length ? `Доступ подтверждён · разрешений: ${scopes.length}` : 'Доступ подтверждён'
  return `Доступ: ${readable.join(' · ')}${unknownCount ? ` · ещё ${unknownCount}` : ''}`
}

function connectionStatus(status: string) {
  const labels: Record<string, string> = {
    ACTIVE: 'Подключено',
    PAUSED: 'Синхронизация приостановлена',
    REAUTH_REQUIRED: 'Нужно переподключить',
    DISCONNECTED: 'Отключено',
  }
  return labels[status] ?? status
}

function credentialKey(section: string, field: string) {
  return `${section}:${field}`
}

function defaultChannelURL(section: string, login: string | undefined) {
  const username = login?.trim().replace(/^@/, '')
  if (!username) return ''
  if (section === 'INSTAGRAM') return `https://www.instagram.com/${username}/`
  if (section === 'TIKTOK') return `https://www.tiktok.com/@${username}`
  return ''
}

const historyBlockTitles: Record<CreatorHistoryBlock, string> = {
  PROFILE: 'История профиля',
  WORK: 'История работ',
  CREDENTIALS: 'История доступов и аккаунтов',
}

const profileHistoryLabels: Record<string, string> = {
  firstName: 'Имя',
  lastName: 'Фамилия',
  middleName: 'Отчество',
  displayName: 'Отображаемое имя',
  status: 'Статус',
  company: 'Компания',
  telegramUsername: 'Telegram',
  internalNote: 'Внутренний комментарий',
}

const workHistoryLabels: Record<string, string> = { status: 'Состояние', comment: 'Описание задачи' }
const creatorStatusLabels: Record<string, string> = { ACTIVE: 'Активен', ON_LEAVE: 'В отпуске', DISMISSED: 'Уволен' }
const workStatusLabels: Record<string, string> = { OK: 'Всё ок', NEEDS_ATTENTION: 'Нужны работы', IN_PROGRESS: 'В работе' }
const historyDateFormatter = new Intl.DateTimeFormat('ru-RU', { dateStyle: 'medium', timeStyle: 'short' })
const historyValueDateFormatter = new Intl.DateTimeFormat('ru-RU', { day: 'numeric', month: 'short', hour: '2-digit', minute: '2-digit' })

function historyFieldLabel(block: CreatorHistoryBlock, change: CreatorHistoryChange) {
  if (block === 'PROFILE') return profileHistoryLabels[change.fieldKey] ?? change.fieldKey
  if (block === 'WORK') return workHistoryLabels[change.fieldKey] ?? change.fieldKey
  if (change.section === 'VK_SHARED') return change.fieldKey === 'account' ? 'ВКонтакте · Корпоративный аккаунт' : change.fieldKey === 'communityUrl' ? 'ВКонтакте · Сообщество креатора' : change.fieldKey === 'recipientAccountUrl' ? 'ВКонтакте · Аккаунт с доступом' : `ВКонтакте · ${change.fieldKey}`
  if (change.section === 'VK_COMPANY') return `ВКонтакте · Общий ${{ login: 'логин', password: 'пароль', phone: 'телефон' }[change.fieldKey as 'login'|'password'|'phone'] ?? change.fieldKey}`
  if (change.section === 'VK') return `ВКонтакте · ${{ login: 'Логин', password: 'Пароль', phone: 'Телефон' }[change.fieldKey as 'login'|'password'|'phone'] ?? change.fieldKey}`
  const section = credentialSections.find(item => item.id === change.section)
  const field = section?.fields.find(item => item.key === change.fieldKey)
  return `${section?.name ?? change.section} · ${field?.label ?? change.fieldKey}`
}

function CompanyVKAccess({ creatorID }: { creatorID: string }) {
  const client = useQueryClient()
  const accounts = useQuery({ queryKey: ['company-vk-accounts'], queryFn: api.companyVkAccounts })
  const access = useQuery({ queryKey: ['creator-vk-access', creatorID], queryFn: () => api.creatorVkAccess(creatorID) })
  const [editing, setEditing] = useState(false)
  const [accountID, setAccountID] = useState('')
  const [communityURL, setCommunityURL] = useState('')
  const [recipientAccountURL, setRecipientAccountURL] = useState('')
  const [password, setPassword] = useState('')
  useEffect(() => {
    setAccountID(access.data?.accountId ?? '')
    setCommunityURL(access.data?.communityUrl ?? '')
    setRecipientAccountURL(access.data?.recipientAccountUrl ?? '')
    setPassword('')
  }, [access.data])
  const save = useMutation({
    mutationFn: () => api.saveCreatorVkAccess(creatorID, accountID, communityURL, recipientAccountURL),
    onSuccess: async () => {
      await Promise.all([
        client.invalidateQueries({ queryKey: ['creator-vk-access', creatorID] }),
        client.invalidateQueries({ queryKey: ['creator-history', creatorID, 'CREDENTIALS'] }),
      ])
      setEditing(false)
      setPassword('')
    },
  })
  const reveal = useMutation({
    mutationFn: () => api.revealCompanyVkPassword(access.data?.accountId ?? ''),
    onSuccess: ({ value }) => setPassword(value),
  })
  const cancel = () => {
    setAccountID(access.data?.accountId ?? '')
    setCommunityURL(access.data?.communityUrl ?? '')
    setRecipientAccountURL(access.data?.recipientAccountUrl ?? '')
    setEditing(false)
  }
  return <article className={`${styles.credentialSection} ${styles.companyVKSection}`}>
    <div className={styles.credentialTitle}><h3>ВКонтакте</h3><span>Общий аккаунт фирмы</span>{!editing ? <button type="button" className={styles.vkEditButton} onClick={() => setEditing(true)}>Изменить</button> : null}</div>
    {access.isPending || accounts.isPending ? <p className={styles.vkEmpty}>Загружаем корпоративный доступ…</p> : access.isError || accounts.isError ? <p className={styles.error}>Не удалось загрузить VK-доступ.</p> : editing ? <div className={styles.credentialRows}>
      <label className={styles.credentialRow}><span>Аккаунт фирмы</span><select value={accountID} onChange={event => { setAccountID(event.target.value); if (!event.target.value) { setCommunityURL(''); setRecipientAccountURL('') } }}><option value="">Не выбран</option>{accounts.data.items.map(account => <option value={account.id} key={account.id}>{account.companyName} · {account.accessMethod === 'PHONE' ? account.phone : account.login}</option>)}</select></label>
      <label className={styles.credentialRow}><span>Сообщество</span><input type="url" required={Boolean(accountID)} disabled={!accountID} value={communityURL} onChange={event => setCommunityURL(event.target.value)} placeholder="https://vk.ru/club240646151" /></label>
      <label className={styles.credentialRow}><span>Аккаунт, которому выдан доступ</span><input type="url" required={Boolean(accountID)} disabled={!accountID} value={recipientAccountURL} onChange={event => setRecipientAccountURL(event.target.value)} placeholder="https://vk.ru/id123" /></label>
      <div className={styles.vkFormActions}><button type="button" className={styles.ghostButton} onClick={cancel}>Отмена</button><button type="button" className={styles.primaryButton} onClick={() => save.mutate()} disabled={save.isPending || (Boolean(accountID) && (!communityURL.trim() || !recipientAccountURL.trim()))}>{save.isPending ? 'Сохраняем…' : 'Сохранить'}</button></div>
      {accounts.data.items.length === 0 ? <p className={styles.vkHint}>Сначала <Link to="/app/companies">настройте общий VK-аккаунт у компании</Link>.</p> : null}
      {save.isError ? <p className={styles.error}>{save.error.message}</p> : null}
    </div> : access.data.accountId ? <div className={styles.credentialRows}>
      <div className={styles.credentialRow}><span>Аккаунт фирмы</span><div className={styles.credentialValue}><strong>{access.data.companyName}</strong></div></div>
      {access.data.accessMethod === 'LOGIN' ? <><div className={styles.credentialRow}><span>Логин</span><div className={styles.credentialValue}><strong>{access.data.login}</strong></div></div><div className={styles.credentialRow}><span>Пароль</span><div className={styles.credentialValue}><strong>{password || '••••••••••••'}</strong>{password ? <button type="button" onClick={() => setPassword('')}>Скрыть</button> : <button type="button" onClick={() => reveal.mutate()} disabled={reveal.isPending}>{reveal.isPending ? 'Открываем…' : 'Показать'}</button>}</div></div></> : <div className={styles.credentialRow}><span>Способ входа</span><div className={styles.credentialValue}><strong>По номеру телефона</strong></div></div>}
      <div className={styles.credentialRow}><span>Телефон</span><div className={styles.credentialValue}><strong className={access.data.phone ? '' : styles.missing}>{access.data.phone || '—'}</strong></div></div>
      <div className={styles.credentialRow}><span>Сообщество</span><div className={styles.credentialValue}><a className={styles.vkCommunityLink} href={access.data.communityUrl} target="_blank" rel="noreferrer">{access.data.communityUrl} ↗</a></div></div>
      <div className={styles.credentialRow}><span>Аккаунт, которому выдан доступ</span><div className={styles.credentialValue}>{access.data.recipientAccountUrl ? <a className={styles.vkCommunityLink} href={access.data.recipientAccountUrl} target="_blank" rel="noreferrer">{access.data.recipientAccountUrl} ↗</a> : <strong className={styles.missing}>—</strong>}</div></div>
      {reveal.isError ? <p className={styles.error}>{reveal.error.message}</p> : null}
    </div> : <div className={styles.vkEmpty}><strong>Корпоративный VK не выбран</strong><p>Выберите общий аккаунт фирмы и вставьте ссылку на сообщество этого креатора.</p>{accounts.data.items.length === 0 ? <Link to="/app/companies">Настроить VK у компании →</Link> : null}</div>}
  </article>
}

function formatHistoryValue(block: CreatorHistoryBlock, change: CreatorHistoryChange, value: string | undefined) {
  if (change.fieldKey === 'status') {
    return block === 'PROFILE' ? creatorStatusLabels[value ?? ''] ?? value : workStatusLabels[value ?? ''] ?? value
  }
  if (block === 'PROFILE' && change.fieldKey === 'telegramUsername' && value) return `@${value}`
  return value
}

function CreatorHistory({ creatorID, block }: { creatorID: string; block: CreatorHistoryBlock }) {
  const [open, setOpen] = useState(false)
  const [revealed, setRevealed] = useState<Record<string, string>>({})
  const history = useQuery({
    queryKey: ['creator-history', creatorID, block],
    queryFn: () => api.creatorHistory(creatorID, block),
    enabled: open,
  })
  const reveal = useMutation({
    mutationFn: ({ changeID, side }: { changeID: string; side: 'old'|'new'; key: string }) => api.revealCreatorHistoryCredential(creatorID, changeID, side),
    onSuccess: ({ value }, variables) => setRevealed(current => ({ ...current, [variables.key]: value })),
  })
  const close = () => {
    setOpen(false)
    setRevealed({})
  }
  const renderValue = (change: CreatorHistoryChange, side: 'old'|'new') => {
    const present = side === 'old' ? change.oldPresent : change.newPresent
    if (!present) return <span className={styles.historyEmpty}>Не заполнено</span>
    if (!change.isSecret) {
      const value = formatHistoryValue(block, change, side === 'old' ? change.oldValue : change.newValue)
      return <strong>{value || 'Не заполнено'}</strong>
    }
    const key = `${change.id}:${side}`
    const value = revealed[key]
    return <div className={styles.historySecret}>
      <strong>{value ?? '••••••••••••'}</strong>
      {value === undefined
        ? <button type="button" onClick={() => reveal.mutate({ changeID: change.id, side, key })} disabled={reveal.isPending}>Показать</button>
        : <button type="button" onClick={() => navigator.clipboard.writeText(value)}>Скопировать</button>}
    </div>
  }

  return <>
    <button type="button" className={styles.historyButton} onClick={() => setOpen(true)}>История</button>
    {open ? <div className={styles.historyBackdrop} role="presentation" onMouseDown={close}>
      <div className={styles.historyDialog} role="dialog" aria-modal="true" aria-label={historyBlockTitles[block]} onMouseDown={event => event.stopPropagation()}>
        <header><div><span>АРХИВ ИЗМЕНЕНИЙ</span><h2>{historyBlockTitles[block]}</h2></div><button type="button" aria-label="Закрыть историю" onClick={close}>×</button></header>
        <div className={styles.historyBody}>
          {history.isPending ? <p className={styles.empty}>Загружаем историю…</p> : history.isError ? <p className={styles.error}>Не удалось загрузить историю: {history.error.message}</p> : history.data.items.length === 0 ? <div className={styles.historyBlank}><strong>Изменений пока нет</strong><p>Здесь появятся прежние и новые значения после следующего редактирования.</p></div> : history.data.items.map(event => <article className={styles.historyEvent} key={event.id}>
            <div className={styles.historyMeta}><time>{historyDateFormatter.format(new Date(event.changedAt))}</time><span>{event.changedBy}</span></div>
            <div className={styles.historyChanges}>{event.changes.map(change => <div className={styles.historyChange} key={change.id}>
              <h3>{historyFieldLabel(block, change)}</h3>
              <div><span>Было</span>{renderValue(change, 'old')}</div>
              <div><span className={styles.historyValueLabel}>Стало <time dateTime={event.changedAt}>· {historyValueDateFormatter.format(new Date(event.changedAt))}</time></span>{renderValue(change, 'new')}</div>
            </div>)}</div>
          </article>)}
        </div>
      </div>
    </div> : null}
  </>
}

function CreatorProfile({ creator, creatorID }: { creator: CreatorDetail; creatorID: string }) {
  const client = useQueryClient()
  const companies = useQuery({ queryKey: ['companies'], queryFn: api.companies })
  const [editing, setEditing] = useState(false)
  const [form, setForm] = useState({
    firstName: creator.firstName,
    lastName: creator.lastName,
    middleName: creator.middleName,
    displayName: creator.displayName,
    telegramUsername: creator.telegramUsername,
    internalNote: creator.internalNote,
    status: creator.status,
    companyId: creator.companyId,
  })
  useEffect(() => {
    setForm({
      firstName: creator.firstName,
      lastName: creator.lastName,
      middleName: creator.middleName,
      displayName: creator.displayName,
      telegramUsername: creator.telegramUsername,
      internalNote: creator.internalNote,
      status: creator.status,
      companyId: creator.companyId,
    })
  }, [creator])
  const update = useMutation({
    mutationFn: () => api.updateCreator(creatorID, form),
    onSuccess: async () => {
      await Promise.all([
        client.invalidateQueries({ queryKey: ['creator', creatorID] }),
        client.invalidateQueries({ queryKey: ['creators'] }),
        client.invalidateQueries({ queryKey: ['creator-analytics', creatorID] }),
      ])
      setEditing(false)
    },
  })
  const telegramURL = creator.telegramUsername ? `https://t.me/${creator.telegramUsername}` : ''

  return <section className={styles.profile}>
    <div className={styles.sectionHead}>
      <div><h2>Профиль креатора</h2><p>Основные данные и быстрые контакты.</p></div>
      <div className={styles.inlineActions}><CreatorHistory creatorID={creatorID} block="PROFILE"/>{!editing ? <button className={styles.secondaryButton} onClick={() => setEditing(true)}>Редактировать</button> : null}</div>
    </div>
    {editing ? <form className={styles.profileForm} onSubmit={(event) => { event.preventDefault(); update.mutate() }}>
      <label>Имя<input required value={form.firstName} onChange={(event) => setForm({ ...form, firstName: event.target.value })}/></label>
      <label>Фамилия<input required value={form.lastName} onChange={(event) => setForm({ ...form, lastName: event.target.value })}/></label>
      <label>Отчество<input value={form.middleName} onChange={(event) => setForm({ ...form, middleName: event.target.value })}/></label>
      <label>Отображаемое имя<input value={form.displayName} onChange={(event) => setForm({ ...form, displayName: event.target.value })}/></label>
      <label>Статус<select className={statusStyles.select} value={form.status} onChange={(event) => setForm({ ...form, status: event.target.value as CreatorStatus })}><option value="ACTIVE">Активен</option><option value="ON_LEAVE">В отпуске</option><option value="DISMISSED">Уволен</option></select></label>
      <label>Компания<select className={statusStyles.select} value={form.companyId} onChange={(event) => setForm({ ...form, companyId: event.target.value })}><option value="">Без компании</option>{companies.data?.items.map(company => <option value={company.id} key={company.id}>{company.name}</option>)}</select></label>
      <label className={styles.wideField}>Telegram<input placeholder="@username или t.me/username" value={form.telegramUsername} onChange={(event) => setForm({ ...form, telegramUsername: event.target.value })}/></label>
      <label className={styles.wideField}>Внутренний комментарий<textarea rows={3} value={form.internalNote} onChange={(event) => setForm({ ...form, internalNote: event.target.value })}/></label>
      {update.isError && <p className={styles.error}>{update.error.message}</p>}
      <div className={styles.formActions}><button type="button" className={styles.ghostButton} onClick={() => setEditing(false)}>Отмена</button><button className={styles.primaryButton} disabled={update.isPending}>{update.isPending ? 'Сохраняем…' : 'Сохранить'}</button></div>
    </form> : <div className={styles.profileSummary}>
      <div><span>Полное имя</span><strong>{[creator.lastName, creator.firstName, creator.middleName].filter(Boolean).join(' ')}</strong></div>
      <div><span>Статус</span><strong className={`${statusStyles.status} ${creator.status === 'ACTIVE' ? statusStyles.active : creator.status === 'ON_LEAVE' ? statusStyles.onLeave : statusStyles.dismissed}`}>{creator.status === 'ACTIVE' ? 'Активен' : creator.status === 'ON_LEAVE' ? 'В отпуске' : 'Уволен'}</strong></div>
      <div><span>Компания</span><strong className={creator.companyName ? '' : styles.missing}>{creator.companyName || 'Не назначена'}</strong></div>
      <div><span>Telegram</span>{telegramURL ? <a href={telegramURL} target="_blank" rel="noreferrer">@{creator.telegramUsername} →</a> : <strong className={styles.missing}>Не указан</strong>}</div>
      <div className={statusStyles.profileNote}><span>Комментарий</span><strong>{creator.internalNote || 'Нет комментария'}</strong></div>
    </div>}
  </section>
}

function CreatorWork({ creator, creatorID }: { creator: CreatorDetail; creatorID: string }) {
  const client = useQueryClient()
  const [editing, setEditing] = useState(false)
  const [status, setStatus] = useState<CreatorWorkStatus>(creator.workStatus)
  const [comment, setComment] = useState(creator.workComment)
  useEffect(() => {
    setStatus(creator.workStatus)
    setComment(creator.workComment)
  }, [creator.workStatus, creator.workComment])
  const update = useMutation({
    mutationFn: () => api.updateCreatorWorkStatus(creatorID, status, comment),
    onSuccess: async () => {
      await Promise.all([
        client.invalidateQueries({ queryKey: ['creator', creatorID] }),
        client.invalidateQueries({ queryKey: ['creators'] }),
      ])
      setEditing(false)
    },
  })
  const cancel = () => {
    setStatus(creator.workStatus)
    setComment(creator.workComment)
    setEditing(false)
  }

  return <section className={styles.workPanel}>
    <div className={styles.sectionHead}>
      <div><h2>Работы по креатору</h2><p>Текущее состояние карточки и задачи, которые требуют внимания.</p></div>
      <div className={styles.inlineActions}><CreatorHistory creatorID={creatorID} block="WORK"/>{!editing ? <button className={styles.secondaryButton} onClick={() => setEditing(true)}>Редактировать</button> : null}</div>
    </div>
    {editing ? <form className={styles.workForm} onSubmit={event => { event.preventDefault(); update.mutate() }}>
      <label>Состояние<select className={statusStyles.select} value={status} onChange={event => { const next = event.target.value as CreatorWorkStatus; setStatus(next); if (next === 'OK') setComment('') }}><option value="OK">Всё ок</option><option value="NEEDS_ATTENTION">Нужны работы</option><option value="IN_PROGRESS">В работе</option></select></label>
      {status !== 'OK' ? <label>{status === 'IN_PROGRESS' ? 'Что сейчас в работе' : 'Что нужно исправить'}<textarea required rows={4} placeholder={status === 'IN_PROGRESS' ? 'Опишите задачу, которую вы взяли в работу' : 'Опишите, что не так и какие работы нужны'} value={comment} onChange={event => setComment(event.target.value)}/></label> : null}
      {update.isError ? <p className={styles.error}>{update.error.message}</p> : null}
      <div className={styles.formActions}><button type="button" className={styles.ghostButton} onClick={cancel}>Отмена</button><button className={styles.primaryButton} disabled={update.isPending}>{update.isPending ? 'Сохраняем…' : 'Сохранить'}</button></div>
    </form> : <div className={styles.workSummary}>
      <strong className={creator.workStatus === 'OK' ? styles.workOk : creator.workStatus === 'IN_PROGRESS' ? styles.workInProgress : styles.workAttention}>{workStatusLabels[creator.workStatus]}</strong>
      {creator.workStatus !== 'OK' ? <p>{creator.workComment}</p> : <p>По креатору сейчас ничего делать не нужно.</p>}
    </div>}
  </section>
}

function CredentialVault({ creatorID }: { creatorID: string }) {
  const client = useQueryClient()
  const credentials = useQuery({ queryKey: ['creator-credentials', creatorID], queryFn: () => api.creatorCredentials(creatorID) })
  const [editing, setEditing] = useState(false)
  const [values, setValues] = useState<Record<string, string>>({})
  const [revealed, setRevealed] = useState<Record<string, string>>({})
  const itemMap = useMemo(() => new Map(credentials.data?.items.map(item => [credentialKey(item.section, item.fieldKey), item])), [credentials.data])

  useEffect(() => {
    const next: Record<string, string> = {}
    for (const item of credentials.data?.items ?? []) {
      if (!item.isSecret) next[credentialKey(item.section, item.fieldKey)] = item.value ?? ''
    }
    setValues(next)
  }, [credentials.data])

  const save = useMutation({
    mutationFn: () => {
      const items = credentialSections.flatMap(section => section.fields.flatMap(field => {
        const key = credentialKey(section.id, field.key)
        const value = values[key] ?? ''
        if (field.secret && value === '') return []
        return [{ section: section.id, fieldKey: field.key, value }]
      }))
      return api.saveCreatorCredentials(creatorID, items)
    },
    onSuccess: async () => {
      await client.invalidateQueries({ queryKey: ['creator-credentials', creatorID] })
      setRevealed({})
      setEditing(false)
    },
  })
  const reveal = useMutation({
    mutationFn: ({ id }: { id: string; key: string }) => api.revealCreatorCredential(creatorID, id),
    onSuccess: ({ value }, variables) => setRevealed(current => ({ ...current, [variables.key]: value })),
  })

  return <section className={styles.credentials}>
    <div className={styles.sectionHead}>
      <div><h2>Доступы и аккаунты</h2><p>Данные из рабочей таблицы. Секреты зашифрованы и раскрываются только администратору.</p></div>
      <div className={styles.inlineActions}><CreatorHistory creatorID={creatorID} block="CREDENTIALS"/>{!editing ? <button className={styles.secondaryButton} onClick={() => setEditing(true)}>Редактировать доступы</button> : <><button className={styles.ghostButton} onClick={() => setEditing(false)}>Отмена</button><button className={styles.primaryButton} onClick={() => save.mutate()} disabled={save.isPending}>{save.isPending ? 'Сохраняем…' : 'Сохранить'}</button></>}</div>
    </div>
    {credentials.isPending ? <p className={styles.empty}>Загружаем доступы…</p> : credentials.isError ? <p className={styles.error}>{credentials.error.message}</p> : <div className={styles.credentialSections}>
      {credentialSections.filter(section => !section.legacy || section.fields.some(field => itemMap.get(credentialKey(section.id, field.key))?.hasValue)).map(section => <article className={`${styles.credentialSection} ${section.legacy ? styles.legacyCredentialSection : ''}`} key={section.id}>
        <div className={styles.credentialTitle}><h3>{section.name}</h3><span>{section.legacy ? 'Перенесите в общий аккаунт фирмы' : `${section.fields.filter(field => itemMap.get(credentialKey(section.id, field.key))?.hasValue).length}/${section.fields.length}`}</span></div>
        <div className={styles.credentialRows}>{section.fields.map(field => {
          const key = credentialKey(section.id, field.key)
          const item = itemMap.get(key)
          const shownValue = field.secret ? revealed[key] : item?.value
          const channelURL = field.channelLink ? shownValue || defaultChannelURL(section.id, values[credentialKey(section.id, 'login')]) : ''
          return <div className={styles.credentialRow} key={field.key}>
            <span>{field.label}</span>
            {editing ? <input type={field.secret ? 'password' : field.channelLink ? 'url' : 'text'} name={`creator-credential-${section.id.toLowerCase()}-${field.key}`} autoComplete={field.secret ? 'new-password' : 'off'} data-lpignore="true" data-1p-ignore="true" spellCheck={false} value={values[key] ?? ''} placeholder={field.channelLink ? defaultChannelURL(section.id, values[credentialKey(section.id, 'login')]) || 'https://...' : field.secret && item?.hasValue ? 'Сохранено — введите для замены' : 'Не заполнено'} onChange={(event) => setValues(current => ({ ...current, [key]: event.target.value }))}/> : <div className={styles.credentialValue}>{field.channelLink && channelURL ? <a className={styles.channelLink} href={channelURL} target="_blank" rel="noreferrer">{channelURL} ↗</a> : <strong className={!item?.hasValue ? styles.missing : ''}>{shownValue || (item?.hasValue ? '••••••••••••' : '—')}</strong>}{field.secret && item?.id && !revealed[key] && <button onClick={() => reveal.mutate({ id: item.id, key })} disabled={reveal.isPending}>Показать</button>}{field.secret && revealed[key] && <button onClick={() => setRevealed(current => { const next = { ...current }; delete next[key]; return next })}>Скрыть</button>}</div>}
          </div>
        })}</div>
      </article>)}
      <CompanyVKAccess creatorID={creatorID}/>
    </div>}
    {save.isError && <p className={styles.error}>{save.error.message}</p>}
  </section>
}

function PlatformConnections({ creatorID }: { creatorID: string }) {
  const queryClient = useQueryClient()
  const [params] = useSearchParams()
  const [showConnectedToast, setShowConnectedToast] = useState(false)
  const [deletedPlatform, setDeletedPlatform] = useState<Platform | null>(null)
  const [pendingDeletion, setPendingDeletion] = useState<PlatformConnection | null>(null)
  const [deleteError, setDeleteError] = useState('')
  const [sharedInvitation, setSharedInvitation] = useState<{ id: string; connectionUrl: string; expiresAt: string } | null>(null)
  const [invitationCopied, setInvitationCopied] = useState(false)
  const connections = useQuery({ queryKey: ['platform-connections', creatorID], queryFn: () => api.connections(creatorID) })
  const integrations = useQuery({ queryKey: ['integrations'], queryFn: api.integrations })
  const instagramInvitation = useQuery({ queryKey: ['instagram-connection-invitation', creatorID], queryFn: () => api.instagramConnectionInvitation(creatorID) })
  const authorize = useMutation({
    mutationFn: (platform: string) => api.startAuthorization(creatorID, platform),
    onSuccess: ({ authorizationUrl }) => window.location.assign(authorizationUrl),
  })
  const createInvitation = useMutation({
    mutationFn: () => api.createInstagramConnectionInvitation(creatorID),
    onSuccess: async (invitation) => {
      setInvitationCopied(false)
      setSharedInvitation(invitation)
      await queryClient.invalidateQueries({ queryKey: ['instagram-connection-invitation', creatorID] })
    },
  })
  const revokeInvitation = useMutation({
    mutationFn: (invitationID: string) => api.revokeInstagramConnectionInvitation(creatorID, invitationID),
    onSuccess: async () => {
      setSharedInvitation(null)
      setInvitationCopied(false)
      await queryClient.invalidateQueries({ queryKey: ['instagram-connection-invitation', creatorID] })
    },
  })
  const copyInvitation = async () => {
    if (!sharedInvitation) return
    try {
      await navigator.clipboard.writeText(sharedInvitation.connectionUrl)
      setInvitationCopied(true)
    } catch {
      setInvitationCopied(false)
    }
  }
  const refresh = async () => {
    await Promise.all([
      queryClient.invalidateQueries({ queryKey: ['platform-connections', creatorID] }),
      queryClient.invalidateQueries({ queryKey: ['integrations'] }),
      queryClient.invalidateQueries({ queryKey: ['creator-analytics', creatorID] }),
      queryClient.invalidateQueries({ queryKey: ['analytics-summary'] }),
      queryClient.invalidateQueries({ queryKey: ['publications'] }),
    ])
  }
  const purge = useMutation({
    mutationFn: (connection: PlatformConnection) => api.purgePlatformData(connection.id),
    onMutate: () => setDeleteError(''),
    onSuccess: async (_, connection) => {
      setPendingDeletion(null)
      await refresh()
      setDeletedPlatform(connection.platform)
      window.setTimeout(() => setDeletedPlatform(null), 6000)
    },
    onError: (error) => setDeleteError(error instanceof Error ? error.message : 'Не удалось удалить данные платформы.'),
  })
  const callbackPlatform = params.get('platform')?.toUpperCase()
  const result = params.get('oauth')
  const configured = new Map(integrations.data?.items.map(item => [item.id, item.configured]))

  useEffect(() => {
    if (result !== 'connected') return
    setShowConnectedToast(true)
    const timer = window.setTimeout(() => setShowConnectedToast(false), 6000)
    const url = new URL(window.location.href)
    url.searchParams.delete('platform')
    url.searchParams.delete('oauth')
    window.history.replaceState({}, '', url)
    return () => window.clearTimeout(timer)
  }, [result])

  return <section className={styles.connections}>
    <div className={styles.connectionHead}><div><h2>Подключения платформ</h2><p>Официальные OAuth-подключения для автоматического сбора статистики.</p></div></div>
    {result && result !== 'connected' ? <p className={styles.error}>Подключение {callbackPlatform ?? 'аккаунта'} не завершено: {result}.</p> : null}
    {authorize.isError ? <p className={styles.error}>{authorize.error.message}</p> : null}
    {createInvitation.isError ? <p className={styles.error}>{createInvitation.error.message}</p> : null}
    {revokeInvitation.isError ? <p className={styles.error}>{revokeInvitation.error.message}</p> : null}
    {deleteError ? <p className={styles.error}>{deleteError}</p> : null}
    <div className={styles.platformGrid}>{platformOptions.map(platform => {
      const platformConnections = connections.data?.items.filter(item => item.platform === platform.id) ?? []
      const isConfigured = configured.get(platform.id)
      return <article className={styles.platformCard} key={platform.id}>
        <div><b>{platform.name}</b><span>{platform.hint}</span><small>{platformConnections.length ? `Аккаунтов: ${platformConnections.length}` : isConfigured === false ? 'Нужны OAuth-реквизиты' : 'Нет подключений'}</small></div>
        <div><button onClick={() => authorize.mutate(platform.id)} disabled={authorize.isPending || integrations.isPending || isConfigured === false}>{authorize.isPending && authorize.variables === platform.id ? 'Переходим…' : 'Подключить'}</button>{platform.id === 'INSTAGRAM' ? <><button onClick={() => createInvitation.mutate()} disabled={createInvitation.isPending || integrations.isPending || isConfigured === false}>{createInvitation.isPending ? 'Создаём…' : instagramInvitation.data?.invitation ? 'Обновить ссылку' : 'Ссылка креатору'}</button><button onClick={() => authorize.mutate('instagram-facebook')} disabled={authorize.isPending || integrations.isPending} title="Нужна Facebook Page, связанная с профессиональным Instagram">Через Facebook · коллаборации</button></> : null}</div>
      </article>
    })}</div>
    {instagramInvitation.data?.invitation ? <div className={styles.invitationStatus}><div><b>Ссылка для креатора активна</b><span>До {new Date(instagramInvitation.data.invitation.expiresAt).toLocaleString('ru-RU')}. Выпуск новой ссылки автоматически отзовёт эту.</span></div><button type="button" onClick={() => revokeInvitation.mutate(instagramInvitation.data.invitation!.id)} disabled={revokeInvitation.isPending}>Отозвать</button></div> : null}
    {connections.isPending ? <p>Загружаем подключения…</p> : connections.data?.items.length ? <div className={styles.connectionList}>{connections.data.items.map(connection => <article key={connection.id}>
      <div>{connection.avatarUrl ? <img src={connection.avatarUrl} alt="" /> : null}<div><b>{connection.displayName}</b><span>{connection.platform} · @{connection.username} · {connectionStatus(connection.status)}</span><small>{connectionPermissions(connection.platform, connection.scopes)}{connection.lastSyncedAt ? ` · синхронизация ${new Date(connection.lastSyncedAt).toLocaleString('ru-RU')}` : ''}</small></div></div>
      <div className={styles.connectionActions}>{connection.profileUrl ? <a href={connection.profileUrl} target="_blank" rel="noreferrer">Открыть</a> : null}<button className={styles.danger} onClick={() => { setDeleteError(''); setPendingDeletion(connection) }} disabled={purge.isPending}>Удалить подключение</button></div>
    </article>)}</div> : <p className={styles.empty}>Аккаунты ещё не подключены.</p>}
    {pendingDeletion ? <div className={styles.dialogBackdrop} role="presentation" onMouseDown={(event) => { if (event.target === event.currentTarget && !purge.isPending) setPendingDeletion(null) }}>
      <div className={styles.confirmDialog} role="dialog" aria-modal="true" aria-labelledby="delete-connection-title">
        <div><span className={styles.dialogMark}>!</span><div><h3 id="delete-connection-title">Удалить подключение?</h3><p><b>{pendingDeletion.displayName}</b> · {pendingDeletion.platform}</p></div></div>
        <p>Доступ платформы будет отозван. Аккаунт, публикации, метрики и задания синхронизации будут удалены без возможности восстановления.</p>
        {deleteError ? <p className={styles.error}>{deleteError}</p> : null}
        <div className={styles.dialogActions}><button type="button" onClick={() => setPendingDeletion(null)} disabled={purge.isPending}>Отмена</button><button type="button" className={styles.confirmDanger} onClick={() => purge.mutate(pendingDeletion)} disabled={purge.isPending}>{purge.isPending ? 'Удаляем…' : 'Удалить всё'}</button></div>
      </div>
    </div> : null}
    {sharedInvitation ? <div className={styles.dialogBackdrop} role="presentation" onMouseDown={(event) => { if (event.target === event.currentTarget) setSharedInvitation(null) }}>
      <div className={styles.shareDialog} role="dialog" aria-modal="true" aria-labelledby="share-instagram-title">
        <div><span className={styles.shareMark}>↗</span><div><h3 id="share-instagram-title">Ссылка для подключения Instagram</h3><p>Отправьте её креатору любым удобным способом.</p></div></div>
        <p>Креатор подтвердит доступ на своём телефоне и не получит доступ к кабинету Статзавода.</p>
        <div className={styles.shareField}><input value={sharedInvitation.connectionUrl} readOnly onFocus={(event) => event.currentTarget.select()} aria-label="Ссылка для креатора"/><button type="button" onClick={copyInvitation}>{invitationCopied ? 'Скопировано' : 'Копировать'}</button></div>
        <small>Действует до {new Date(sharedInvitation.expiresAt).toLocaleString('ru-RU')} и станет недоступна после успешного подключения.</small>
        <div className={styles.dialogActions}><button type="button" onClick={() => setSharedInvitation(null)}>Готово</button></div>
      </div>
    </div> : null}
    {showConnectedToast ? <div className={styles.connectionToast} role="status"><span className={styles.toastMark}>✓</span><div><b>{callbackPlatform ?? 'Аккаунт'} подключён</b><span>Доступ к данным добавлен</span></div><button type="button" aria-label="Закрыть уведомление" onClick={() => setShowConnectedToast(false)}>×</button></div> : null}
    {deletedPlatform ? <div className={styles.connectionToast} role="status"><span className={styles.toastMark}>✓</span><div><b>{deletedPlatform} удалён</b><span>Подключение и собранные данные удалены</span></div><button type="button" aria-label="Закрыть уведомление" onClick={() => setDeletedPlatform(null)}>×</button></div> : null}
  </section>
}

export function CreatorDetailPage() {
  const { id = '' } = useParams()
  const navigate = useNavigate()
  const [contact, setContact] = useState('')
  const [archiveConfirmation, setArchiveConfirmation] = useState(false)
  const client = useQueryClient()
  const creator = useQuery({ queryKey: ['creator', id], queryFn: () => api.creator(id), enabled: Boolean(id) })
  const addContact = useMutation({ mutationFn: () => api.createContact(id, { kind: 'EMAIL', value: contact, isPrimary: !creator.data?.contacts.length }), onSuccess: () => { setContact(''); client.invalidateQueries({ queryKey: ['creator', id] }) } })
  const refreshCreatorLists = () => Promise.all([
    client.invalidateQueries({ queryKey: ['creator', id] }),
    client.invalidateQueries({ queryKey: ['creators'] }),
    client.invalidateQueries({ queryKey: ['companies'] }),
    client.invalidateQueries({ queryKey: ['summary'] }),
  ])
  const archive = useMutation({
    mutationFn: () => api.archiveCreator(id),
    onSuccess: async () => {
      setArchiveConfirmation(false)
      await refreshCreatorLists()
      navigate('/app/creators')
    },
  })
  const restore = useMutation({ mutationFn: () => api.restoreCreator(id), onSuccess: refreshCreatorLists })
  if (creator.isPending) return <p>Загружаем карточку креатора…</p>
  if (creator.isError) return <p className={styles.error}>{creator.error.message}</p>
  return <section className={styles.page}>
    <Link to="/app/creators" className={styles.back}>← Креаторы</Link>
    <header><div><p>КАРТОЧКА КРЕАТОРА</p><h1>{creator.data.displayName}</h1></div><div className={styles.headerActions}>{creator.data.telegramUsername && <a className={styles.telegram} href={`https://t.me/${creator.data.telegramUsername}`} target="_blank" rel="noreferrer">Открыть Telegram</a>}{creator.data.archivedAt ? <button type="button" className={styles.restoreButton} onClick={() => restore.mutate()} disabled={restore.isPending}>{restore.isPending ? 'Восстанавливаем…' : 'Восстановить'}</button> : <button type="button" className={styles.archiveButton} onClick={() => setArchiveConfirmation(true)}>В архив</button>}</div></header>
    {creator.data.archivedAt ? <div className={styles.archiveNotice}><span>АРХИВ</span><div><b>Карточка не показывается в рабочих списках и на дашборде</b><small>Перенесена {historyDateFormatter.format(new Date(creator.data.archivedAt))}. Все данные, доступы и история сохранены.</small></div></div> : null}
    {restore.isError ? <p className={styles.error}>Не удалось восстановить карточку: {restore.error.message}</p> : null}
    <CreatorProfile creator={creator.data} creatorID={id}/>
    <CreatorWork creator={creator.data} creatorID={id}/>
    <CredentialVault creatorID={id}/>
    <PlatformConnections creatorID={id}/>
    <section className={styles.contacts}><div className={styles.sectionHead}><div><h2>Контакты</h2><p>Дополнительные способы связи.</p></div></div>{creator.data.contacts.map(c => <p key={c.id}>{c.kind}: {c.value}</p>)}<form onSubmit={event => { event.preventDefault(); if (contact) addContact.mutate() }}><input value={contact} onChange={event => setContact(event.target.value)} placeholder="Email"/><button>Добавить</button></form></section>
    {archiveConfirmation ? <div className={styles.dialogBackdrop} role="presentation" onMouseDown={(event) => { if (event.target === event.currentTarget && !archive.isPending) setArchiveConfirmation(false) }}><div className={`${styles.confirmDialog} ${styles.archiveDialog}`} role="dialog" aria-modal="true" aria-labelledby="archive-creator-title"><div><span className={styles.archiveDialogMark}>↘</span><div><h3 id="archive-creator-title">Перенести креатора в архив?</h3><p>{creator.data.displayName}</p></div></div><p>Карточка исчезнет из рабочих списков и дашборда. Профиль, доступы, статистика и история изменений сохранятся — креатора можно восстановить в любой момент.</p>{archive.isError ? <p className={styles.error}>Не удалось перенести карточку: {archive.error.message}</p> : null}<div className={styles.dialogActions}><button type="button" onClick={() => setArchiveConfirmation(false)} disabled={archive.isPending}>Отмена</button><button type="button" className={styles.confirmArchive} onClick={() => archive.mutate()} disabled={archive.isPending}>{archive.isPending ? 'Переносим…' : 'Перенести в архив'}</button></div></div></div> : null}
  </section>
}
