import { FormEvent, useState } from 'react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { useSearchParams } from 'react-router-dom'
import { api, type Company } from '../../shared/api/client'
import { Button } from '../../shared/ui/Button'
import styles from './CompaniesPage.module.scss'

export function CompaniesPage() {
  const [name, setName] = useState('')
  const [pendingArchive, setPendingArchive] = useState<Company | null>(null)
  const [vkCompany, setVkCompany] = useState<Company | null>(null)
  const [syncQueued, setSyncQueued] = useState(false)
  const [vkForm, setVkForm] = useState<{ accessMethod: 'LOGIN'|'PHONE'; login: string; password: string; phone: string }>({ accessMethod: 'LOGIN', login: '', password: '', phone: '' })
  const [params] = useSearchParams()
  const queryClient = useQueryClient()
  const companies = useQuery({ queryKey: ['companies'], queryFn: api.companies })
  const vkAccounts = useQuery({ queryKey: ['company-vk-accounts'], queryFn: api.companyVkAccounts, refetchInterval: vkCompany ? 10_000 : false })
  const create = useMutation({
    mutationFn: () => api.createCompany(name),
    onSuccess: async () => {
      setName('')
      await queryClient.invalidateQueries({ queryKey: ['companies'] })
    },
  })
  const archive = useMutation({
    mutationFn: api.archiveCompany,
    onSuccess: async () => {
      setPendingArchive(null)
      await Promise.all([
        queryClient.invalidateQueries({ queryKey: ['companies'] }),
        queryClient.invalidateQueries({ queryKey: ['creators'] }),
      ])
    },
  })
  const saveVK = useMutation({
    mutationFn: () => api.saveCompanyVkAccount(vkCompany?.id ?? '', vkForm),
    onSuccess: async () => {
      setVkCompany(null)
      setVkForm({ accessMethod: 'LOGIN', login: '', password: '', phone: '' })
      await Promise.all([
        queryClient.invalidateQueries({ queryKey: ['companies'] }),
        queryClient.invalidateQueries({ queryKey: ['company-vk-accounts'] }),
      ])
    },
  })
  const revealVK = useMutation({
    mutationFn: (accountId: string) => api.revealCompanyVkPassword(accountId),
    onSuccess: ({ value }) => setVkForm(current => ({ ...current, password: value })),
  })
  const authorizeVK = useMutation({
    mutationFn: (companyId: string) => api.startCompanyVkAuthorization(companyId),
    onSuccess: ({ authorizationUrl }) => window.location.assign(authorizationUrl),
  })
  const syncVK = useMutation({
    mutationFn: (platformAccountId: string) => api.requestPlatformSync(platformAccountId),
    onSuccess: async () => {
      setSyncQueued(true)
      await queryClient.invalidateQueries({ queryKey: ['company-vk-accounts'] })
    },
  })
  function openVK(company: Company) {
    const account = vkAccounts.data?.items.find(item => item.companyId === company.id)
    setVkForm({ accessMethod: account?.accessMethod ?? 'LOGIN', login: account?.login ?? '', password: '', phone: account?.phone ?? '' })
    setSyncQueued(false)
    setVkCompany(company)
  }
  function submit(event: FormEvent) {
    event.preventDefault()
    if (name.trim()) create.mutate()
  }

  return <section className={styles.page}>
    <header><div><p>СТРУКТУРА КОМАНДЫ</p><h1>Компании</h1><span>Группируйте креаторов по брендам и направлениям, для которых они создают контент.</span></div></header>
    <form className={styles.create} onSubmit={submit}>
      <label><span>Новая компания</span><input value={name} onChange={event => setName(event.target.value)} placeholder="Например, Поле чудес" /></label>
      <Button type="submit" disabled={!name.trim() || create.isPending}>{create.isPending ? 'Создаём…' : 'Создать компанию'}</Button>
    </form>
    {create.isError ? <p className={styles.error}>{create.error.message}</p> : null}
    {params.get('oauth') === 'connected' ? <p className={styles.success}>Общий аккаунт VK ID подключён. Сбор сообществ поставлен в очередь.</p> : params.get('oauth') ? <p className={styles.error}>Подключение VK не завершено: {params.get('oauth')}.</p> : null}
    {companies.isPending ? <div className={styles.state}>Загружаем компании…</div> : companies.isError ? <div className={styles.error}>{companies.error.message}</div> : companies.data.items.length ? <div className={styles.grid}>{companies.data.items.map(company => { const vkAccount = vkAccounts.data?.items.find(item => item.companyId === company.id); const vkIDConnected = vkAccount?.oauthStatus === 'ACTIVE'; return <article key={company.id}>
      <div className={styles.mark}>{company.name.slice(0, 1).toUpperCase()}</div>
      <div className={styles.companyInfo}><h2>{company.name}</h2><p>{company.creatorCount ? `${company.creatorCount} ${company.creatorCount === 1 ? 'креатор' : 'креаторов'}` : 'Креаторы ещё не назначены'}</p></div>
      <div className={styles.actions}><button type="button" className={vkIDConnected ? styles.vkReady : styles.vkSetup} onClick={() => openVK(company)}>{vkIDConnected ? 'VK ID подключён' : company.hasVkAccount ? 'Подключить VK ID' : 'Настроить VK'}</button><button type="button" className={styles.archive} onClick={() => setPendingArchive(company)}>Архивировать</button></div>
    </article> })}</div> : <div className={styles.state}>Компаний пока нет. Создайте первую выше.</div>}
    {pendingArchive ? <div className={styles.backdrop} role="presentation" onMouseDown={event => { if (event.target === event.currentTarget && !archive.isPending) setPendingArchive(null) }}><div className={styles.dialog} role="dialog" aria-modal="true"><h2>Архивировать «{pendingArchive.name}»?</h2><p>Компания исчезнет из списка, а её креаторы перейдут в категорию «Без компании». Карточки и статистика сохранятся.</p>{archive.isError ? <p className={styles.error}>{archive.error.message}</p> : null}<footer><button onClick={() => setPendingArchive(null)} disabled={archive.isPending}>Отмена</button><button className={styles.confirm} onClick={() => archive.mutate(pendingArchive.id)} disabled={archive.isPending}>{archive.isPending ? 'Архивируем…' : 'Архивировать'}</button></footer></div></div> : null}
    {vkCompany ? <div className={styles.backdrop} role="presentation" onMouseDown={event => { if (event.target === event.currentTarget && !saveVK.isPending) setVkCompany(null) }}><form className={`${styles.dialog} ${styles.vkDialog}`} role="dialog" aria-modal="true" aria-labelledby="company-vk-title" onSubmit={event => { event.preventDefault(); saveVK.mutate() }}><div className={styles.dialogHead}><div><span>ОБЩИЙ ДОСТУП КОМПАНИИ</span><h2 id="company-vk-title">VK · {vkCompany.name}</h2></div><button type="button" aria-label="Закрыть" onClick={() => setVkCompany(null)}>×</button></div><p>Этот аккаунт управляет сообществами креаторов. Подключите его один раз через VK ID — статистика будет собираться из сообществ, указанных в карточках креаторов.</p>{(() => { const account = vkAccounts.data?.items.find(item => item.companyId === vkCompany.id); return account ? <div className={styles.oauthBox}><b>{account.oauthStatus === 'ACTIVE' ? `VK ID подключён${account.oauthDisplayName ? ` · ${account.oauthDisplayName}` : ''}` : 'VK ID ещё не подключён'}</b><span>Сообществ назначено: {account.communityCount}</span>{account.syncError ? <span className={styles.syncError}>Ошибка синхронизации: {account.syncError}</span> : account.lastSuccessAt ? <span className={styles.syncSuccess}>Последняя успешная синхронизация: {new Date(account.lastSuccessAt).toLocaleString('ru-RU')}</span> : account.oauthStatus === 'ACTIVE' ? <span>Первая синхронизация ещё не завершена.</span> : null}<div className={styles.oauthActions}><button type="button" onClick={() => authorizeVK.mutate(vkCompany.id)} disabled={authorizeVK.isPending}>{authorizeVK.isPending ? 'Переходим…' : account.oauthStatus === 'ACTIVE' ? 'Переподключить VK ID' : 'Подключить VK ID'}</button>{account.oauthStatus === 'ACTIVE' && account.platformAccountId ? <button type="button" onClick={() => syncVK.mutate(account.platformAccountId)} disabled={syncVK.isPending}>{syncVK.isPending ? 'Ставим в очередь…' : syncQueued ? 'Синхронизация запрошена' : 'Синхронизировать сейчас'}</button> : null}</div>{authorizeVK.isError ? <span className={styles.error}>{authorizeVK.error.message}</span> : null}{syncVK.isError ? <span className={styles.error}>{syncVK.error.message}</span> : null}</div> : <p className={styles.oauthHint}>Сначала сохраните данные общего аккаунта, затем снова откройте эту настройку и подключите VK ID.</p> })()}<fieldset className={styles.accessMethod}><legend>Данные общего доступа для команды</legend><label><input type="radio" name="vk-access-method" checked={vkForm.accessMethod === 'LOGIN'} onChange={() => setVkForm(current => ({ ...current, accessMethod: 'LOGIN' }))} />Логин и пароль</label><label><input type="radio" name="vk-access-method" checked={vkForm.accessMethod === 'PHONE'} onChange={() => setVkForm(current => ({ ...current, accessMethod: 'PHONE', login: '', password: '' }))} />Только номер телефона</label></fieldset><div className={styles.vkFields}>{vkForm.accessMethod === 'LOGIN' ? <><label>Логин<input required value={vkForm.login} onChange={event => setVkForm(current => ({ ...current, login: event.target.value }))} autoComplete="off" /></label><label>Пароль<div className={styles.passwordField}><input required={!vkCompany.hasVkAccount || vkCompany.hasVkAccount && vkAccounts.data?.items.find(item => item.companyId === vkCompany.id)?.accessMethod !== 'LOGIN'} type="password" value={vkForm.password} onChange={event => setVkForm(current => ({ ...current, password: event.target.value }))} autoComplete="new-password" placeholder={vkCompany.hasVkAccount ? 'Сохранён — введите только для замены' : 'Введите пароль'} />{vkCompany.hasVkAccount && !vkForm.password && vkAccounts.data?.items.find(item => item.companyId === vkCompany.id)?.hasPassword ? <button type="button" onClick={() => { const account = vkAccounts.data?.items.find(item => item.companyId === vkCompany.id); if (account) revealVK.mutate(account.id) }} disabled={revealVK.isPending}>{revealVK.isPending ? 'Открываем…' : 'Показать'}</button> : null}</div></label><label>Телефон<input value={vkForm.phone} onChange={event => setVkForm(current => ({ ...current, phone: event.target.value }))} autoComplete="off" placeholder="Необязательно" /></label></> : <label>Номер телефона<input required type="tel" value={vkForm.phone} onChange={event => setVkForm(current => ({ ...current, phone: event.target.value }))} autoComplete="tel" placeholder="+7 999 000-00-00" /></label>}</div>{saveVK.isError ? <p className={styles.error}>{saveVK.error.message}</p> : null}{revealVK.isError ? <p className={styles.error}>{revealVK.error.message}</p> : null}<footer><button type="button" onClick={() => setVkCompany(null)} disabled={saveVK.isPending}>Отмена</button><button type="submit" className={styles.saveVK} disabled={saveVK.isPending || (vkForm.accessMethod === 'LOGIN' ? !vkForm.login.trim() : !vkForm.phone.trim())}>{saveVK.isPending ? 'Сохраняем…' : 'Сохранить VK'}</button></footer></form></div> : null}
  </section>
}
