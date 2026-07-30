import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { useEffect, useMemo, useState } from 'react'
import { Link, useParams, useSearchParams } from 'react-router-dom'
import { api, type CreatorDetail, type CreatorStatus, type Platform } from '../../shared/api/client'
import styles from './CreatorDetailPage.module.scss'
import statusStyles from './CreatorStatus.module.scss'

const platformOptions: { id: Platform; name: string; hint: string }[] = [
  { id: 'YOUTUBE', name: 'YouTube', hint: 'Канал и YouTube Analytics' },
  { id: 'INSTAGRAM', name: 'Instagram', hint: 'Профиль, Reels и Insights' },
  { id: 'TIKTOK', name: 'TikTok', hint: 'Профиль и опубликованные видео' },
  { id: 'VK', name: 'VK', hint: 'Профиль, видео и клипы' },
]

type CredentialField = { key: string; label: string; secret?: boolean }
type CredentialSection = { id: string; name: string; fields: CredentialField[] }

const credentialSections: CredentialSection[] = [
  { id: 'GMAIL', name: 'Gmail', fields: [{ key: 'login', label: 'Логин' }, { key: 'password', label: 'Пароль', secret: true }, { key: 'phone', label: 'Телефон' }] },
  { id: 'YOUTUBE', name: 'YouTube', fields: [{ key: 'note', label: 'Способ доступа' }, { key: 'login', label: 'Логин' }, { key: 'password', label: 'Пароль', secret: true }, { key: 'phone', label: 'Телефон' }, { key: 'email', label: 'Почта' }] },
  { id: 'INSTAGRAM', name: 'Instagram', fields: [{ key: 'login', label: 'Логин' }, { key: 'password', label: 'Пароль', secret: true }, { key: 'phone', label: 'Телефон' }, { key: 'email', label: 'Почта' }] },
  { id: 'TIKTOK', name: 'TikTok', fields: [{ key: 'login', label: 'Логин' }, { key: 'password', label: 'Пароль', secret: true }, { key: 'phone', label: 'Телефон' }, { key: 'email', label: 'Почта' }] },
  { id: 'VK', name: 'ВКонтакте', fields: [{ key: 'login', label: 'Логин' }, { key: 'password', label: 'Пароль', secret: true }, { key: 'phone', label: 'Телефон' }] },
]

function credentialKey(section: string, field: string) {
  return `${section}:${field}`
}

function CreatorProfile({ creator, creatorID }: { creator: CreatorDetail; creatorID: string }) {
  const client = useQueryClient()
  const [editing, setEditing] = useState(false)
  const [form, setForm] = useState({
    firstName: creator.firstName,
    lastName: creator.lastName,
    middleName: creator.middleName,
    displayName: creator.displayName,
    telegramUsername: creator.telegramUsername,
    internalNote: creator.internalNote,
    status: creator.status,
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
      {!editing && <button className={styles.secondaryButton} onClick={() => setEditing(true)}>Редактировать</button>}
    </div>
    {editing ? <form className={styles.profileForm} onSubmit={(event) => { event.preventDefault(); update.mutate() }}>
      <label>Имя<input required value={form.firstName} onChange={(event) => setForm({ ...form, firstName: event.target.value })}/></label>
      <label>Фамилия<input required value={form.lastName} onChange={(event) => setForm({ ...form, lastName: event.target.value })}/></label>
      <label>Отчество<input value={form.middleName} onChange={(event) => setForm({ ...form, middleName: event.target.value })}/></label>
      <label>Отображаемое имя<input value={form.displayName} onChange={(event) => setForm({ ...form, displayName: event.target.value })}/></label>
      <label>Статус<select className={statusStyles.select} value={form.status} onChange={(event) => setForm({ ...form, status: event.target.value as CreatorStatus })}><option value="ACTIVE">Активен</option><option value="ON_LEAVE">В отпуске</option><option value="DISMISSED">Уволен</option></select></label>
      <label className={styles.wideField}>Telegram<input placeholder="@username или t.me/username" value={form.telegramUsername} onChange={(event) => setForm({ ...form, telegramUsername: event.target.value })}/></label>
      <label className={styles.wideField}>Внутренний комментарий<textarea rows={3} value={form.internalNote} onChange={(event) => setForm({ ...form, internalNote: event.target.value })}/></label>
      {update.isError && <p className={styles.error}>{update.error.message}</p>}
      <div className={styles.formActions}><button type="button" className={styles.ghostButton} onClick={() => setEditing(false)}>Отмена</button><button className={styles.primaryButton} disabled={update.isPending}>{update.isPending ? 'Сохраняем…' : 'Сохранить'}</button></div>
    </form> : <div className={styles.profileSummary}>
      <div><span>Полное имя</span><strong>{[creator.lastName, creator.firstName, creator.middleName].filter(Boolean).join(' ')}</strong></div>
      <div><span>Статус</span><strong className={`${statusStyles.status} ${creator.status === 'ACTIVE' ? statusStyles.active : creator.status === 'ON_LEAVE' ? statusStyles.onLeave : statusStyles.dismissed}`}>{creator.status === 'ACTIVE' ? 'Активен' : creator.status === 'ON_LEAVE' ? 'В отпуске' : 'Уволен'}</strong></div>
      <div><span>Telegram</span>{telegramURL ? <a href={telegramURL} target="_blank" rel="noreferrer">@{creator.telegramUsername} →</a> : <strong className={styles.missing}>Не указан</strong>}</div>
      <div className={statusStyles.profileNote}><span>Комментарий</span><strong>{creator.internalNote || 'Нет комментария'}</strong></div>
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
      {!editing ? <button className={styles.secondaryButton} onClick={() => setEditing(true)}>Редактировать доступы</button> : <div className={styles.inlineActions}><button className={styles.ghostButton} onClick={() => setEditing(false)}>Отмена</button><button className={styles.primaryButton} onClick={() => save.mutate()} disabled={save.isPending}>{save.isPending ? 'Сохраняем…' : 'Сохранить'}</button></div>}
    </div>
    {credentials.isPending ? <p className={styles.empty}>Загружаем доступы…</p> : credentials.isError ? <p className={styles.error}>{credentials.error.message}</p> : <div className={styles.credentialSections}>
      {credentialSections.map(section => <article className={styles.credentialSection} key={section.id}>
        <div className={styles.credentialTitle}><h3>{section.name}</h3><span>{section.fields.filter(field => itemMap.get(credentialKey(section.id, field.key))?.hasValue).length}/{section.fields.length}</span></div>
        <div className={styles.credentialRows}>{section.fields.map(field => {
          const key = credentialKey(section.id, field.key)
          const item = itemMap.get(key)
          const shownValue = field.secret ? revealed[key] : item?.value
          return <div className={styles.credentialRow} key={field.key}>
            <span>{field.label}</span>
            {editing ? <input type={field.secret ? 'password' : 'text'} name={`creator-credential-${section.id.toLowerCase()}-${field.key}`} autoComplete={field.secret ? 'new-password' : 'off'} data-lpignore="true" data-1p-ignore="true" spellCheck={false} value={values[key] ?? ''} placeholder={field.secret && item?.hasValue ? 'Сохранено — введите для замены' : 'Не заполнено'} onChange={(event) => setValues(current => ({ ...current, [key]: event.target.value }))}/> : <div className={styles.credentialValue}><strong className={!item?.hasValue ? styles.missing : ''}>{shownValue || (item?.hasValue ? '••••••••••••' : '—')}</strong>{field.secret && item?.id && !revealed[key] && <button onClick={() => reveal.mutate({ id: item.id, key })} disabled={reveal.isPending}>Показать</button>}{field.secret && revealed[key] && <button onClick={() => setRevealed(current => { const next = { ...current }; delete next[key]; return next })}>Скрыть</button>}</div>}
          </div>
        })}</div>
      </article>)}
    </div>}
    {save.isError && <p className={styles.error}>{save.error.message}</p>}
  </section>
}

function PlatformConnections({ creatorID }: { creatorID: string }) {
  const queryClient = useQueryClient()
  const [params] = useSearchParams()
  const connections = useQuery({ queryKey: ['platform-connections', creatorID], queryFn: () => api.connections(creatorID) })
  const integrations = useQuery({ queryKey: ['integrations'], queryFn: api.integrations })
  const authorize = useMutation({
    mutationFn: (platform: Platform) => api.startAuthorization(creatorID, platform),
    onSuccess: ({ authorizationUrl }) => window.location.assign(authorizationUrl),
  })
  const refresh = () => {
    queryClient.invalidateQueries({ queryKey: ['platform-connections', creatorID] })
    queryClient.invalidateQueries({ queryKey: ['integrations'] })
  }
  const disconnect = useMutation({ mutationFn: api.disconnectPlatform, onSuccess: refresh })
  const purge = useMutation({ mutationFn: api.purgePlatformData, onSuccess: refresh })
  const callbackPlatform = params.get('platform')?.toUpperCase()
  const result = params.get('oauth')
  const configured = new Map(integrations.data?.items.map(item => [item.id, item.configured]))

  return <section className={styles.connections}>
    <div className={styles.connectionHead}><div><h2>Подключения платформ</h2><p>Официальные OAuth-подключения для автоматического сбора статистики.</p></div></div>
    {result === 'connected' ? <p className={styles.success}>{callbackPlatform ?? 'Аккаунт'} подключён.</p> : null}
    {result && result !== 'connected' ? <p className={styles.error}>Подключение {callbackPlatform ?? 'аккаунта'} не завершено: {result}.</p> : null}
    {authorize.isError ? <p className={styles.error}>{authorize.error.message}</p> : null}
    <div className={styles.platformGrid}>{platformOptions.map(platform => {
      const platformConnections = connections.data?.items.filter(item => item.platform === platform.id) ?? []
      const isConfigured = configured.get(platform.id)
      return <article className={styles.platformCard} key={platform.id}>
        <div><b>{platform.name}</b><span>{platform.hint}</span><small>{platformConnections.length ? `Подключено: ${platformConnections.length}` : isConfigured === false ? 'Нужны OAuth-реквизиты' : 'Нет подключений'}</small></div>
        <button onClick={() => authorize.mutate(platform.id)} disabled={authorize.isPending || integrations.isPending || isConfigured === false}>{authorize.isPending && authorize.variables === platform.id ? 'Переходим…' : 'Подключить'}</button>
      </article>
    })}</div>
    {connections.isPending ? <p>Загружаем подключения…</p> : connections.data?.items.length ? <div className={styles.connectionList}>{connections.data.items.map(connection => <article key={connection.id}>
      <div>{connection.avatarUrl ? <img src={connection.avatarUrl} alt="" /> : null}<div><b>{connection.displayName}</b><span>{connection.platform} · @{connection.username} · {connection.status}</span><small>{connection.scopes.join(', ') || 'Разрешения уточняются'}{connection.lastSyncedAt ? ` · синхронизация ${new Date(connection.lastSyncedAt).toLocaleString('ru-RU')}` : ''}</small></div></div>
      <div className={styles.connectionActions}>{connection.profileUrl ? <a href={connection.profileUrl} target="_blank" rel="noreferrer">Открыть</a> : null}<button onClick={() => disconnect.mutate(connection.id)} disabled={disconnect.isPending}>Отключить</button><button className={styles.danger} onClick={() => { if (window.confirm(`Удалить подключение ${connection.platform}, публикации и метрики?`)) purge.mutate(connection.id) }} disabled={purge.isPending}>Удалить данные</button></div>
    </article>)}</div> : <p className={styles.empty}>Аккаунты ещё не подключены.</p>}
  </section>
}

export function CreatorDetailPage() {
  const { id = '' } = useParams()
  const [contact, setContact] = useState('')
  const client = useQueryClient()
  const creator = useQuery({ queryKey: ['creator', id], queryFn: () => api.creator(id), enabled: Boolean(id) })
  const addContact = useMutation({ mutationFn: () => api.createContact(id, { kind: 'EMAIL', value: contact, isPrimary: !creator.data?.contacts.length }), onSuccess: () => { setContact(''); client.invalidateQueries({ queryKey: ['creator', id] }) } })
  if (creator.isPending) return <p>Загружаем карточку креатора…</p>
  if (creator.isError) return <p className={styles.error}>{creator.error.message}</p>
  return <section className={styles.page}>
    <Link to="/app/creators" className={styles.back}>← Креаторы</Link>
    <header><div><p>КАРТОЧКА КРЕАТОРА</p><h1>{creator.data.displayName}</h1></div><div className={styles.headerActions}>{creator.data.telegramUsername && <a className={styles.telegram} href={`https://t.me/${creator.data.telegramUsername}`} target="_blank" rel="noreferrer">Открыть Telegram</a>}</div></header>
    <CreatorProfile creator={creator.data} creatorID={id}/>
    <CredentialVault creatorID={id}/>
    <PlatformConnections creatorID={id}/>
    <section className={styles.contacts}><div className={styles.sectionHead}><div><h2>Контакты</h2><p>Дополнительные способы связи.</p></div></div>{creator.data.contacts.map(c => <p key={c.id}>{c.kind}: {c.value}</p>)}<form onSubmit={event => { event.preventDefault(); if (contact) addContact.mutate() }}><input value={contact} onChange={event => setContact(event.target.value)} placeholder="Email"/><button>Добавить</button></form></section>
  </section>
}
