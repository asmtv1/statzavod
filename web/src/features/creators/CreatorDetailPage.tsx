import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { useEffect, useMemo, useState } from 'react'
import { Link, useNavigate, useParams, useSearchParams } from 'react-router-dom'
import { api, type CreatorDetail, type CreatorHistoryBlock, type CreatorHistoryChange, type CreatorStatus, type CreatorWorkStatus, type Platform, type PlatformConnection } from '../../shared/api/client'
import { useI18n } from '../../shared/i18n/I18nProvider'
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

function connectionPermissions(platform: Platform, scopes: string[], t: (value:string) => string) {
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
  const readable = scopes.map(scope => labels[platform][scope]).filter((label): label is string => Boolean(label)).map(t)
  const unknownCount = scopes.length - readable.length
  if (!readable.length) return scopes.length ? `${t('Доступ подтверждён')} · ${t('разрешений:')} ${scopes.length}` : t('Доступ подтверждён')
  return `${t('Доступ:')} ${readable.join(' · ')}${unknownCount ? ` · ${t('ещё')} ${unknownCount}` : ''}`
}

function connectionStatus(status: string, t: (value:string) => string) {
  const labels: Record<string, string> = {
    ACTIVE: 'Подключено',
    PAUSED: 'Синхронизация приостановлена',
    REAUTH_REQUIRED: 'Нужно переподключить',
    DISCONNECTED: 'Отключено',
  }
  return t(labels[status] ?? status)
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
function historyFieldLabel(block: CreatorHistoryBlock, change: CreatorHistoryChange, t: (value:string) => string) {
  if (block === 'PROFILE') return t(profileHistoryLabels[change.fieldKey] ?? change.fieldKey)
  if (block === 'WORK') return t(workHistoryLabels[change.fieldKey] ?? change.fieldKey)
  if (change.section === 'VK_SHARED') return `VK · ${t(change.fieldKey === 'account' ? 'Корпоративный аккаунт' : change.fieldKey === 'communityUrl' ? 'Сообщество креатора' : change.fieldKey === 'recipientAccountUrl' ? 'Аккаунт с доступом' : change.fieldKey)}`
  if (change.section === 'VK_COMPANY') return `VK · ${t('Общий')} ${t({ login: 'логин', password: 'пароль', phone: 'телефон' }[change.fieldKey as 'login'|'password'|'phone'] ?? change.fieldKey)}`
  if (change.section === 'VK') return `VK · ${t({ login: 'Логин', password: 'Пароль', phone: 'Телефон' }[change.fieldKey as 'login'|'password'|'phone'] ?? change.fieldKey)}`
  const section = credentialSections.find(item => item.id === change.section)
  const field = section?.fields.find(item => item.key === change.fieldKey)
  return `${t(section?.name ?? change.section)} · ${t(field?.label ?? change.fieldKey)}`
}

function CompanyVKAccess({ creatorID }: { creatorID: string }) {
  const { locale, t } = useI18n()
  const client = useQueryClient()
  const accounts = useQuery({ queryKey: ['company-vk-accounts', locale], queryFn: api.companyVkAccounts })
  const access = useQuery({ queryKey: ['creator-vk-access', creatorID, locale], queryFn: () => api.creatorVkAccess(creatorID) })
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
    <div className={styles.credentialTitle}><h3>VK</h3><span>{t('Общий аккаунт фирмы')}</span>{!editing ? <button type="button" className={styles.vkEditButton} onClick={() => setEditing(true)}>{t('Изменить')}</button> : null}</div>
    {access.isPending || accounts.isPending ? <p className={styles.vkEmpty}>{t('Загружаем корпоративный доступ…')}</p> : access.isError || accounts.isError ? <p className={styles.error}>{t('Не удалось загрузить VK-доступ.')}</p> : editing ? <div className={styles.credentialRows}>
      <label className={styles.credentialRow}><span>{t('Аккаунт фирмы')}</span><select value={accountID} onChange={event => { setAccountID(event.target.value); if (!event.target.value) { setCommunityURL(''); setRecipientAccountURL('') } }}><option value="">{t('Не выбран')}</option>{accounts.data.items.map(account => <option value={account.id} key={account.id}>{account.companyName} · {account.accessMethod === 'PHONE' ? account.phone : account.login}</option>)}</select></label>
      <label className={styles.credentialRow}><span>{t('Сообщество')}</span><input type="url" required={Boolean(accountID)} disabled={!accountID} value={communityURL} onChange={event => setCommunityURL(event.target.value)} placeholder="https://vk.ru/club240646151" /></label>
      <label className={styles.credentialRow}><span>{t('Аккаунт, которому выдан доступ')}</span><input type="url" required={Boolean(accountID)} disabled={!accountID} value={recipientAccountURL} onChange={event => setRecipientAccountURL(event.target.value)} placeholder="https://vk.ru/id123" /></label>
      <div className={styles.vkFormActions}><button type="button" className={styles.ghostButton} onClick={cancel}>{t('Отмена')}</button><button type="button" className={styles.primaryButton} onClick={() => save.mutate()} disabled={save.isPending || (Boolean(accountID) && (!communityURL.trim() || !recipientAccountURL.trim()))}>{save.isPending ? t('Сохраняем…') : t('Сохранить')}</button></div>
      {accounts.data.items.length === 0 ? <p className={styles.vkHint}>{t('Сначала')} <Link to="/app/companies">{t('настройте общий VK-аккаунт у компании')}</Link>.</p> : null}
      {save.isError ? <p className={styles.error}>{t(save.error.message)}</p> : null}
    </div> : access.data.accountId ? <div className={styles.credentialRows}>
      <div className={styles.credentialRow}><span>{t('Аккаунт фирмы')}</span><div className={styles.credentialValue}><strong>{access.data.companyName}</strong></div></div>
      {access.data.accessMethod === 'LOGIN' ? <><div className={styles.credentialRow}><span>{t('Логин')}</span><div className={styles.credentialValue}><strong>{access.data.login}</strong></div></div><div className={styles.credentialRow}><span>{t('Пароль')}</span><div className={styles.credentialValue}><strong>{password || '••••••••••••'}</strong>{password ? <button type="button" onClick={() => setPassword('')}>{t('Скрыть')}</button> : <button type="button" onClick={() => reveal.mutate()} disabled={reveal.isPending}>{reveal.isPending ? t('Открываем…') : t('Показать')}</button>}</div></div></> : <div className={styles.credentialRow}><span>{t('Способ входа')}</span><div className={styles.credentialValue}><strong>{t('По номеру телефона')}</strong></div></div>}
      <div className={styles.credentialRow}><span>{t('Телефон')}</span><div className={styles.credentialValue}><strong className={access.data.phone ? '' : styles.missing}>{access.data.phone || '—'}</strong></div></div>
      <div className={styles.credentialRow}><span>{t('Сообщество')}</span><div className={styles.credentialValue}><a className={styles.vkCommunityLink} href={access.data.communityUrl} target="_blank" rel="noreferrer">{access.data.communityUrl} ↗</a></div></div>
      <div className={styles.credentialRow}><span>{t('Аккаунт, которому выдан доступ')}</span><div className={styles.credentialValue}>{access.data.recipientAccountUrl ? <a className={styles.vkCommunityLink} href={access.data.recipientAccountUrl} target="_blank" rel="noreferrer">{access.data.recipientAccountUrl} ↗</a> : <strong className={styles.missing}>—</strong>}</div></div>
      {reveal.isError ? <p className={styles.error}>{t(reveal.error.message)}</p> : null}
    </div> : <div className={styles.vkEmpty}><strong>{t('Корпоративный VK не выбран')}</strong><p>{t('Выберите общий аккаунт фирмы и вставьте ссылку на сообщество этого креатора.')}</p>{accounts.data.items.length === 0 ? <Link to="/app/companies">{t('Настроить VK у компании →')}</Link> : null}</div>}
  </article>
}

function formatHistoryValue(block: CreatorHistoryBlock, change: CreatorHistoryChange, value: string | undefined, t: (value:string) => string) {
  if (change.fieldKey === 'status') {
    return t(block === 'PROFILE' ? creatorStatusLabels[value ?? ''] ?? value ?? '' : workStatusLabels[value ?? ''] ?? value ?? '')
  }
  if (block === 'PROFILE' && change.fieldKey === 'telegramUsername' && value) return `@${value}`
  return value
}

function CreatorHistory({ creatorID, block }: { creatorID: string; block: CreatorHistoryBlock }) {
  const { locale, t } = useI18n()
  const historyDateFormatter = new Intl.DateTimeFormat(locale === 'en' ? 'en-US' : 'ru-RU', { dateStyle: 'medium', timeStyle: 'short' })
  const historyValueDateFormatter = new Intl.DateTimeFormat(locale === 'en' ? 'en-US' : 'ru-RU', { day: 'numeric', month: 'short', hour: '2-digit', minute: '2-digit' })
  const [open, setOpen] = useState(false)
  const [revealed, setRevealed] = useState<Record<string, string>>({})
  const history = useQuery({
    queryKey: ['creator-history', creatorID, block, locale],
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
    if (!present) return <span className={styles.historyEmpty}>{t('Не заполнено')}</span>
    if (!change.isSecret) {
      const value = formatHistoryValue(block, change, side === 'old' ? change.oldValue : change.newValue, t)
      return <strong>{value || t('Не заполнено')}</strong>
    }
    const key = `${change.id}:${side}`
    const value = revealed[key]
    return <div className={styles.historySecret}>
      <strong>{value ?? '••••••••••••'}</strong>
      {value === undefined
        ? <button type="button" onClick={() => reveal.mutate({ changeID: change.id, side, key })} disabled={reveal.isPending}>{t('Показать')}</button>
        : <button type="button" onClick={() => navigator.clipboard.writeText(value)}>{t('Скопировать')}</button>}
    </div>
  }

  return <>
    <button type="button" className={styles.historyButton} onClick={() => setOpen(true)}>{t('История')}</button>
    {open ? <div className={styles.historyBackdrop} role="presentation" onMouseDown={close}>
      <div className={styles.historyDialog} role="dialog" aria-modal="true" aria-label={t(historyBlockTitles[block])} onMouseDown={event => event.stopPropagation()}>
        <header><div><span>{t('АРХИВ ИЗМЕНЕНИЙ')}</span><h2>{t(historyBlockTitles[block])}</h2></div><button type="button" aria-label={t('Закрыть историю')} onClick={close}>×</button></header>
        <div className={styles.historyBody}>
          {history.isPending ? <p className={styles.empty}>{t('Загружаем историю…')}</p> : history.isError ? <p className={styles.error}>{t('Не удалось загрузить историю:')} {t(history.error.message)}</p> : history.data.items.length === 0 ? <div className={styles.historyBlank}><strong>{t('Изменений пока нет')}</strong><p>{t('Здесь появятся прежние и новые значения после следующего редактирования.')}</p></div> : history.data.items.map(event => <article className={styles.historyEvent} key={event.id}>
            <div className={styles.historyMeta}><time>{historyDateFormatter.format(new Date(event.changedAt))}</time><span>{t(event.changedBy)}</span></div>
            <div className={styles.historyChanges}>{event.changes.map(change => <div className={styles.historyChange} key={change.id}>
              <h3>{historyFieldLabel(block, change, t)}</h3>
              <div><span>{t('Было')}</span>{renderValue(change, 'old')}</div>
              <div><span className={styles.historyValueLabel}>{t('Стало')} <time dateTime={event.changedAt}>· {historyValueDateFormatter.format(new Date(event.changedAt))}</time></span>{renderValue(change, 'new')}</div>
            </div>)}</div>
          </article>)}
        </div>
      </div>
    </div> : null}
  </>
}

function CreatorProfile({ creator, creatorID }: { creator: CreatorDetail; creatorID: string }) {
  const { locale, t } = useI18n()
  const client = useQueryClient()
  const companies = useQuery({ queryKey: ['companies', locale], queryFn: api.companies })
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
      <div><h2>{t('Профиль креатора')}</h2><p>{t('Основные данные и быстрые контакты.')}</p></div>
      <div className={styles.inlineActions}><CreatorHistory creatorID={creatorID} block="PROFILE"/>{!editing ? <button className={styles.secondaryButton} onClick={() => setEditing(true)}>{t('Редактировать')}</button> : null}</div>
    </div>
    {editing ? <form className={styles.profileForm} onSubmit={(event) => { event.preventDefault(); update.mutate() }}>
      <label>{t('Имя')}<input required value={form.firstName} onChange={(event) => setForm({ ...form, firstName: event.target.value })}/></label>
      <label>{t('Фамилия')}<input required value={form.lastName} onChange={(event) => setForm({ ...form, lastName: event.target.value })}/></label>
      <label>{t('Отчество')}<input value={form.middleName} onChange={(event) => setForm({ ...form, middleName: event.target.value })}/></label>
      <label>{t('Отображаемое имя')}<input value={form.displayName} onChange={(event) => setForm({ ...form, displayName: event.target.value })}/></label>
      <label>{t('Статус')}<select className={statusStyles.select} value={form.status} onChange={(event) => setForm({ ...form, status: event.target.value as CreatorStatus })}><option value="ACTIVE">{t('Активен')}</option><option value="ON_LEAVE">{t('В отпуске')}</option><option value="DISMISSED">{t('Уволен')}</option></select></label>
      <label>{t('Компания')}<select className={statusStyles.select} value={form.companyId} onChange={(event) => setForm({ ...form, companyId: event.target.value })}><option value="">{t('Без компании')}</option>{companies.data?.items.map(company => <option value={company.id} key={company.id}>{company.name}</option>)}</select></label>
      <label className={styles.wideField}>Telegram<input placeholder="@username or t.me/username" value={form.telegramUsername} onChange={(event) => setForm({ ...form, telegramUsername: event.target.value })}/></label>
      <label className={styles.wideField}>{t('Внутренний комментарий')}<textarea rows={3} value={form.internalNote} onChange={(event) => setForm({ ...form, internalNote: event.target.value })}/></label>
      {update.isError && <p className={styles.error}>{t(update.error.message)}</p>}
      <div className={styles.formActions}><button type="button" className={styles.ghostButton} onClick={() => setEditing(false)}>{t('Отмена')}</button><button className={styles.primaryButton} disabled={update.isPending}>{update.isPending ? t('Сохраняем…') : t('Сохранить')}</button></div>
    </form> : <div className={styles.profileSummary}>
      <div><span>{t('Полное имя')}</span><strong>{[creator.lastName, creator.firstName, creator.middleName].filter(Boolean).join(' ')}</strong></div>
      <div><span>{t('Статус')}</span><strong className={`${statusStyles.status} ${creator.status === 'ACTIVE' ? statusStyles.active : creator.status === 'ON_LEAVE' ? statusStyles.onLeave : statusStyles.dismissed}`}>{creator.status === 'ACTIVE' ? t('Активен') : creator.status === 'ON_LEAVE' ? t('В отпуске') : t('Уволен')}</strong></div>
      <div><span>{t('Компания')}</span><strong className={creator.companyName ? '' : styles.missing}>{creator.companyName || t('Не назначена')}</strong></div>
      <div><span>Telegram</span>{telegramURL ? <a href={telegramURL} target="_blank" rel="noreferrer">@{creator.telegramUsername} →</a> : <strong className={styles.missing}>{t('Не указан')}</strong>}</div>
      <div className={statusStyles.profileNote}><span>{t('Комментарий')}</span><strong>{creator.internalNote || t('Нет комментария')}</strong></div>
    </div>}
  </section>
}

function CreatorWork({ creator, creatorID }: { creator: CreatorDetail; creatorID: string }) {
  const { t } = useI18n()
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
      <div><h2>{t('Работы по креатору')}</h2><p>{t('Текущее состояние карточки и задачи, которые требуют внимания.')}</p></div>
      <div className={styles.inlineActions}><CreatorHistory creatorID={creatorID} block="WORK"/>{!editing ? <button className={styles.secondaryButton} onClick={() => setEditing(true)}>{t('Редактировать')}</button> : null}</div>
    </div>
    {editing ? <form className={styles.workForm} onSubmit={event => { event.preventDefault(); update.mutate() }}>
      <label>{t('Состояние')}<select className={statusStyles.select} value={status} onChange={event => { const next = event.target.value as CreatorWorkStatus; setStatus(next); if (next === 'OK') setComment('') }}><option value="OK">{t('Всё ок')}</option><option value="NEEDS_ATTENTION">{t('Нужны работы')}</option><option value="IN_PROGRESS">{t('В работе')}</option></select></label>
      {status !== 'OK' ? <label>{status === 'IN_PROGRESS' ? t('Что сейчас в работе') : t('Что нужно исправить')}<textarea required rows={4} placeholder={status === 'IN_PROGRESS' ? t('Опишите задачу, которую вы взяли в работу') : t('Опишите, что не так и какие работы нужны')} value={comment} onChange={event => setComment(event.target.value)}/></label> : null}
      {update.isError ? <p className={styles.error}>{t(update.error.message)}</p> : null}
      <div className={styles.formActions}><button type="button" className={styles.ghostButton} onClick={cancel}>{t('Отмена')}</button><button className={styles.primaryButton} disabled={update.isPending}>{update.isPending ? t('Сохраняем…') : t('Сохранить')}</button></div>
    </form> : <div className={styles.workSummary}>
      <strong className={creator.workStatus === 'OK' ? styles.workOk : creator.workStatus === 'IN_PROGRESS' ? styles.workInProgress : styles.workAttention}>{t(workStatusLabels[creator.workStatus])}</strong>
      {creator.workStatus !== 'OK' ? <p>{creator.workComment}</p> : <p>{t('По креатору сейчас ничего делать не нужно.')}</p>}
    </div>}
  </section>
}

function CredentialVault({ creatorID }: { creatorID: string }) {
  const { locale, t } = useI18n()
  const client = useQueryClient()
  const credentials = useQuery({ queryKey: ['creator-credentials', creatorID, locale], queryFn: () => api.creatorCredentials(creatorID) })
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
      <div><h2>{t('Доступы и аккаунты')}</h2><p>{t('Данные из рабочей таблицы. Секреты зашифрованы и раскрываются только администратору.')}</p></div>
      <div className={styles.inlineActions}><CreatorHistory creatorID={creatorID} block="CREDENTIALS"/>{!editing ? <button className={styles.secondaryButton} onClick={() => setEditing(true)}>{t('Редактировать доступы')}</button> : <><button className={styles.ghostButton} onClick={() => setEditing(false)}>{t('Отмена')}</button><button className={styles.primaryButton} onClick={() => save.mutate()} disabled={save.isPending}>{save.isPending ? t('Сохраняем…') : t('Сохранить')}</button></>}</div>
    </div>
    {credentials.isPending ? <p className={styles.empty}>{t('Загружаем доступы…')}</p> : credentials.isError ? <p className={styles.error}>{t(credentials.error.message)}</p> : <div className={styles.credentialSections}>
      {credentialSections.filter(section => !section.legacy || section.fields.some(field => itemMap.get(credentialKey(section.id, field.key))?.hasValue)).map(section => <article className={`${styles.credentialSection} ${section.legacy ? styles.legacyCredentialSection : ''}`} key={section.id}>
        <div className={styles.credentialTitle}><h3>{t(section.name)}</h3><span>{section.legacy ? t('Перенесите в общий аккаунт фирмы') : `${section.fields.filter(field => itemMap.get(credentialKey(section.id, field.key))?.hasValue).length}/${section.fields.length}`}</span></div>
        <div className={styles.credentialRows}>{section.fields.map(field => {
          const key = credentialKey(section.id, field.key)
          const item = itemMap.get(key)
          const shownValue = field.secret ? revealed[key] : item?.value
          const channelURL = field.channelLink ? shownValue || defaultChannelURL(section.id, values[credentialKey(section.id, 'login')]) : ''
          return <div className={styles.credentialRow} key={field.key}>
            <span>{t(field.label)}</span>
            {editing ? <input type={field.secret ? 'password' : field.channelLink ? 'url' : 'text'} name={`creator-credential-${section.id.toLowerCase()}-${field.key}`} autoComplete={field.secret ? 'new-password' : 'off'} data-lpignore="true" data-1p-ignore="true" spellCheck={false} value={values[key] ?? ''} placeholder={field.channelLink ? defaultChannelURL(section.id, values[credentialKey(section.id, 'login')]) || 'https://...' : field.secret && item?.hasValue ? t('Сохранено — введите для замены') : t('Не заполнено')} onChange={(event) => setValues(current => ({ ...current, [key]: event.target.value }))}/> : <div className={styles.credentialValue}>{field.channelLink && channelURL ? <a className={styles.channelLink} href={channelURL} target="_blank" rel="noreferrer">{channelURL} ↗</a> : <strong className={!item?.hasValue ? styles.missing : ''}>{shownValue || (item?.hasValue ? '••••••••••••' : '—')}</strong>}{field.secret && item?.id && !revealed[key] && <button onClick={() => reveal.mutate({ id: item.id, key })} disabled={reveal.isPending}>{t('Показать')}</button>}{field.secret && revealed[key] && <button onClick={() => setRevealed(current => { const next = { ...current }; delete next[key]; return next })}>{t('Скрыть')}</button>}</div>}
          </div>
        })}</div>
      </article>)}
      <CompanyVKAccess creatorID={creatorID}/>
    </div>}
    {save.isError && <p className={styles.error}>{t(save.error.message)}</p>}
  </section>
}

function PlatformConnections({ creatorID }: { creatorID: string }) {
  const { locale, t } = useI18n()
  const queryClient = useQueryClient()
  const [params] = useSearchParams()
  const [showConnectedToast, setShowConnectedToast] = useState(false)
  const [disconnectedPlatform, setDisconnectedPlatform] = useState<Platform | null>(null)
  const [pendingDisconnect, setPendingDisconnect] = useState<PlatformConnection | null>(null)
  const [disconnectError, setDisconnectError] = useState('')
  const me = useQuery({ queryKey: ['me'], queryFn: api.me })
  const connections = useQuery({ queryKey: ['platform-connections', creatorID, locale], queryFn: () => api.connections(creatorID) })
  const integrations = useQuery({ queryKey: ['integrations', locale], queryFn: api.integrations })
  const authorize = useMutation({
    mutationFn: (platform: string) => api.startAuthorization(creatorID, platform),
    onSuccess: ({ authorizationUrl }) => window.location.assign(authorizationUrl),
  })
  const refresh = async () => {
    await Promise.all([
      queryClient.invalidateQueries({ queryKey: ['platform-connections', creatorID] }),
      queryClient.invalidateQueries({ queryKey: ['integrations'] }),
      queryClient.invalidateQueries({ queryKey: ['creator-analytics', creatorID] }),
      queryClient.invalidateQueries({ queryKey: ['creator', creatorID] }),
      queryClient.invalidateQueries({ queryKey: ['creators'] }),
      queryClient.invalidateQueries({ queryKey: ['summary'] }),
      queryClient.invalidateQueries({ queryKey: ['publications'] }),
    ])
  }
  const disconnect = useMutation({
    mutationFn: (connection: PlatformConnection) => api.disconnectPlatformAccount(connection.id),
    onMutate: () => setDisconnectError(''),
    onSuccess: async (_, connection) => {
      setPendingDisconnect(null)
      await refresh()
      setDisconnectedPlatform(connection.platform)
    },
    onError: (error) => setDisconnectError(error instanceof Error ? error.message : t('Не удалось отвязать аккаунт.')),
  })
  const callbackPlatform = params.get('platform')?.toUpperCase()
  const result = params.get('oauth')
  const configured = new Map(integrations.data?.items.map(item => [item.id, item.configured]))
  const canDisconnect = me.data?.role === 'ADMIN' || me.data?.role === 'ANALYST'

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

  useEffect(() => {
    if (!disconnectedPlatform) return
    const timer = window.setTimeout(() => setDisconnectedPlatform(null), 6000)
    return () => window.clearTimeout(timer)
  }, [disconnectedPlatform])

  return <section className={styles.connections}>
    <div className={styles.connectionHead}><div><h2>{t('Подключения платформ')}</h2><p>{t('Официальные OAuth-подключения для автоматического сбора статистики.')}</p></div></div>
    {result && result !== 'connected' ? <p className={styles.error}>{t('Подключение')} {callbackPlatform ?? t('аккаунта')} {t('не завершено:')} {result}.</p> : null}
    {authorize.isError ? <p className={styles.error}>{t(authorize.error.message)}</p> : null}
    {disconnectError && !pendingDisconnect ? <p className={styles.error}>{t(disconnectError)}</p> : null}
    <div className={styles.platformGrid}>{platformOptions.map(platform => {
      const platformConnections = connections.data?.items.filter(item => item.platform === platform.id) ?? []
      const isConfigured = configured.get(platform.id)
      return <article className={styles.platformCard} key={platform.id}>
        <div><b>{platform.name}</b><span>{t(platform.hint)}</span><small>{platformConnections.length ? `${t('Аккаунтов:')} ${platformConnections.length}` : isConfigured === false ? t('Нужны OAuth-реквизиты') : t('Нет подключений')}</small></div>
        <div><button onClick={() => authorize.mutate(platform.id)} disabled={authorize.isPending || integrations.isPending || isConfigured === false}>{authorize.isPending && authorize.variables === platform.id ? t('Переходим…') : t('Подключить')}</button>{platform.id === 'INSTAGRAM' ? <button onClick={() => authorize.mutate('instagram-facebook')} disabled={authorize.isPending || integrations.isPending} title={t('Нужна Facebook Page, связанная с профессиональным Instagram')}>{t('Через Facebook · коллаборации')}</button> : null}</div>
      </article>
    })}</div>
    {connections.isPending ? <p>{t('Загружаем подключения…')}</p> : connections.data?.items.length ? <div className={styles.connectionList}>{connections.data.items.map(connection => <article key={connection.id}>
      <div>{connection.avatarUrl ? <img src={connection.avatarUrl} alt="" /> : null}<div><b>{connection.displayName}{connection.isVerified ? ' ✓' : ''}</b><span>{connection.platform} · @{connection.username} · {connectionStatus(connection.status, t)}</span>{connection.bioDescription ? <small>{connection.bioDescription}</small> : null}<small>{connectionPermissions(connection.platform, connection.scopes, t)}{connection.lastSyncedAt ? ` · ${t('синхронизация')} ${new Date(connection.lastSyncedAt).toLocaleString(locale === 'en' ? 'en-US' : 'ru-RU')}` : ''}</small></div></div>
      <div className={styles.connectionActions}>{connection.profileUrl ? <a href={connection.profileUrl} target="_blank" rel="noreferrer">{t('Открыть')}</a> : null}{canDisconnect ? <button type="button" className={styles.danger} onClick={() => { setDisconnectError(''); setPendingDisconnect(connection) }} disabled={disconnect.isPending}>{t('Отвязать аккаунт')}</button> : null}</div>
    </article>)}</div> : <p className={styles.empty}>{t('Аккаунты ещё не подключены.')}</p>}
    {pendingDisconnect ? <div className={styles.dialogBackdrop} role="presentation" onMouseDown={(event) => { if (event.target === event.currentTarget && !disconnect.isPending) { setPendingDisconnect(null); setDisconnectError('') } }}>
      <div className={styles.confirmDialog} role="dialog" aria-modal="true" aria-labelledby="disconnect-account-title">
        <div><span className={styles.dialogMark}>!</span><div><h3 id="disconnect-account-title">{t('Отвязать аккаунт?')}</h3><p><b>{pendingDisconnect.displayName}</b> · {pendingDisconnect.platform}</p></div></div>
        <p>{t('Доступ платформы будет отозван, а новые синхронизации остановятся. Уже собранные публикации и метрики сохранятся.')}</p>
        {disconnectError ? <p className={styles.error}>{t(disconnectError)}</p> : null}
        <div className={styles.dialogActions}><button type="button" onClick={() => { setPendingDisconnect(null); setDisconnectError('') }} disabled={disconnect.isPending}>{t('Отмена')}</button><button type="button" className={styles.confirmDanger} onClick={() => disconnect.mutate(pendingDisconnect)} disabled={disconnect.isPending}>{disconnect.isPending ? t('Отвязываем…') : t('Отвязать')}</button></div>
      </div>
    </div> : null}
    {showConnectedToast ? <div className={styles.connectionToast} role="status"><span className={styles.toastMark}>✓</span><div><b>{callbackPlatform ?? t('Аккаунт')} {t('подключён')}</b><span>{t('Доступ к данным добавлен')}</span></div><button type="button" aria-label={t('Закрыть уведомление')} onClick={() => setShowConnectedToast(false)}>×</button></div> : null}
    {disconnectedPlatform ? <div className={styles.connectionToast} role="status"><span className={styles.toastMark}>✓</span><div><b>{disconnectedPlatform} {t('отвязан')}</b><span>{t('Подключение отозвано; собранные данные сохранены')}</span></div><button type="button" aria-label={t('Закрыть уведомление')} onClick={() => setDisconnectedPlatform(null)}>×</button></div> : null}
  </section>
}

export function CreatorDetailPage() {
  const { locale, t } = useI18n()
  const historyDateFormatter = new Intl.DateTimeFormat(locale === 'en' ? 'en-US' : 'ru-RU', { dateStyle: 'medium', timeStyle: 'short' })
  const { id = '' } = useParams()
  const navigate = useNavigate()
  const [contact, setContact] = useState('')
  const [archiveConfirmation, setArchiveConfirmation] = useState(false)
  const [deleteConfirmation, setDeleteConfirmation] = useState(false)
  const client = useQueryClient()
  const creator = useQuery({ queryKey: ['creator', id, locale], queryFn: () => api.creator(id), enabled: Boolean(id) })
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
  const removeCreator = useMutation({
    mutationFn: () => api.deleteCreator(id),
    onSuccess: async () => {
      setDeleteConfirmation(false)
      await Promise.all([
        client.cancelQueries({ queryKey: ['creator', id] }),
        client.cancelQueries({ queryKey: ['creator-history', id] }),
        client.cancelQueries({ queryKey: ['creator-credentials', id] }),
        client.cancelQueries({ queryKey: ['creator-vk-access', id] }),
        client.cancelQueries({ queryKey: ['creator-analytics', id] }),
        client.cancelQueries({ queryKey: ['platform-connections', id] }),
      ])
      navigate('/app/creators')
      client.removeQueries({ queryKey: ['creator', id] })
      client.removeQueries({ queryKey: ['creator-history', id] })
      client.removeQueries({ queryKey: ['creator-credentials', id] })
      client.removeQueries({ queryKey: ['creator-vk-access', id] })
      client.removeQueries({ queryKey: ['creator-analytics', id] })
      client.removeQueries({ queryKey: ['platform-connections', id] })
      await Promise.all([
        client.invalidateQueries({ queryKey: ['creators'] }),
        client.invalidateQueries({ queryKey: ['companies'] }),
        client.invalidateQueries({ queryKey: ['summary'] }),
        client.invalidateQueries({ queryKey: ['timeseries'] }),
        client.invalidateQueries({ queryKey: ['integrations'] }),
        client.invalidateQueries({ queryKey: ['publications'] }),
        client.invalidateQueries({ queryKey: ['content-groups'] }),
        client.invalidateQueries({ queryKey: ['sync-health'] }),
      ])
    },
  })
  const openDeleteConfirmation = () => {
    removeCreator.reset()
    setDeleteConfirmation(true)
  }
  const closeDeleteConfirmation = () => {
    if (removeCreator.isPending) return
    removeCreator.reset()
    setDeleteConfirmation(false)
  }
  if (creator.isPending) return <p>{t('Загружаем карточку креатора…')}</p>
  if (creator.isError) return <p className={styles.error}>{t(creator.error.message)}</p>
  return <section className={styles.page}>
    <Link to="/app/creators" className={styles.back}>← {t('Креаторы')}</Link>
    <header><div><p>{t('КАРТОЧКА КРЕАТОРА')}</p><h1>{creator.data.displayName}</h1></div><div className={styles.headerActions}>{creator.data.telegramUsername && <a className={styles.telegram} href={`https://t.me/${creator.data.telegramUsername}`} target="_blank" rel="noreferrer">{t('Открыть Telegram')}</a>}{creator.data.archivedAt ? <button type="button" className={styles.restoreButton} onClick={() => restore.mutate()} disabled={restore.isPending}>{restore.isPending ? t('Восстанавливаем…') : t('Восстановить')}</button> : <button type="button" className={styles.archiveButton} onClick={() => setArchiveConfirmation(true)}>{t('В архив')}</button>}{creator.data.canDelete ? <button type="button" className={styles.deleteButton} onClick={openDeleteConfirmation}>{t('Удалить')}</button> : null}</div></header>
    {creator.data.archivedAt ? <div className={styles.archiveNotice}><span>{t('АРХИВ')}</span><div><b>{t('Карточка не показывается в рабочих списках и на дашборде')}</b><small>{t('Перенесена')} {historyDateFormatter.format(new Date(creator.data.archivedAt))}. {t('Все данные, доступы и история сохранены.')}</small></div></div> : null}
    {restore.isError ? <p className={styles.error}>{t('Не удалось восстановить карточку:')} {t(restore.error.message)}</p> : null}
    <CreatorProfile creator={creator.data} creatorID={id}/>
    <CreatorWork creator={creator.data} creatorID={id}/>
    <CredentialVault creatorID={id}/>
    <PlatformConnections creatorID={id}/>
    <section className={styles.contacts}><div className={styles.sectionHead}><div><h2>{t('Контакты')}</h2><p>{t('Дополнительные способы связи.')}</p></div></div>{creator.data.contacts.map(c => <p key={c.id}>{c.kind}: {c.value}</p>)}<form onSubmit={event => { event.preventDefault(); if (contact) addContact.mutate() }}><input value={contact} onChange={event => setContact(event.target.value)} placeholder="Email"/><button>{t('Добавить')}</button></form></section>
    {archiveConfirmation ? <div className={styles.dialogBackdrop} role="presentation" onMouseDown={(event) => { if (event.target === event.currentTarget && !archive.isPending) setArchiveConfirmation(false) }}><div className={`${styles.confirmDialog} ${styles.archiveDialog}`} role="dialog" aria-modal="true" aria-labelledby="archive-creator-title"><div><span className={styles.archiveDialogMark}>↘</span><div><h3 id="archive-creator-title">{t('Перенести креатора в архив?')}</h3><p>{creator.data.displayName}</p></div></div><p>{t('Карточка исчезнет из рабочих списков и дашборда. Профиль, доступы, статистика и история изменений сохранятся — креатора можно восстановить в любой момент.')}</p>{archive.isError ? <p className={styles.error}>{t('Не удалось перенести карточку:')} {t(archive.error.message)}</p> : null}<div className={styles.dialogActions}><button type="button" onClick={() => setArchiveConfirmation(false)} disabled={archive.isPending}>{t('Отмена')}</button><button type="button" className={styles.confirmArchive} onClick={() => archive.mutate()} disabled={archive.isPending}>{archive.isPending ? t('Переносим…') : t('Перенести в архив')}</button></div></div></div> : null}
    {deleteConfirmation ? <div className={styles.dialogBackdrop} role="presentation" onMouseDown={(event) => { if (event.target === event.currentTarget) closeDeleteConfirmation() }}><div className={styles.confirmDialog} role="dialog" aria-modal="true" aria-labelledby="delete-creator-title"><div><span className={styles.dialogMark}>!</span><div><h3 id="delete-creator-title">{t('Удалить креатора навсегда?')}</h3><p>{creator.data.displayName}</p></div></div><p>{t('Это действие необратимо. Карточка креатора, история изменений и все связанные данные будут удалены навсегда. Все привязанные аккаунты будут отвязаны.')}</p>{removeCreator.isError ? <p className={styles.error}>{t('Не удалось удалить креатора:')} {t(removeCreator.error.message)}</p> : null}<div className={styles.dialogActions}><button type="button" onClick={closeDeleteConfirmation} disabled={removeCreator.isPending}>{t('Отмена')}</button><button type="button" className={styles.confirmDanger} onClick={() => removeCreator.mutate()} disabled={removeCreator.isPending}>{removeCreator.isPending ? t('Удаляем…') : t('Удалить навсегда')}</button></div></div></div> : null}
  </section>
}
