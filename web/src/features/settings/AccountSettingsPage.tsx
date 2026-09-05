import { FormEvent, useState } from 'react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { Link, useLocation, useNavigate } from 'react-router-dom'
import { api } from '../../shared/api/client'
import { useAccess } from '../../shared/access/AccessProvider'
import { useI18n } from '../../shared/i18n/I18nProvider'
import { DangerButton, Field, PageHeader, Panel } from '../../shared/ui/Primitives'
import styles from '../management/Management.module.scss'

export function AccountSettingsPage(){
  const {locale,t}=useI18n()
  const {principal,scopeKey}=useAccess()
  const client=useQueryClient()
  const navigate=useNavigate()
  const [password,setPassword]=useState('')
  const [confirmEmail,setConfirmEmail]=useState('')
  const impact=useQuery({queryKey:['self-deletion-impact',scopeKey,locale],queryFn:api.selfDeletionImpact})
  const remove=useMutation({mutationFn:()=>api.selfDelete(password,confirmEmail),onSuccess:result=>{client.clear();navigate('/account-deleted',{replace:true,state:{receiptId:result?.receiptId??null,workspace:Boolean(result)}})}})
  function submit(event:FormEvent){event.preventDefault();remove.mutate()}
  return <section className={styles.page}>
    <PageHeader eyebrow={t('БЕЗОПАСНОСТЬ АККАУНТА')} title={t('Настройки аккаунта')} description={principal?.email}/>
    <Panel className={styles.dangerPanel}><h2>{t('Удаление аккаунта')}</h2>{impact.isPending?<p>{t('Рассчитываем последствия…')}</p>:impact.isError?<p className={styles.error}>{t(impact.error.message)}</p>:<>
      <p className={impact.data.workspaceWillBeDeleted?styles.error:styles.notice}>{impact.data.workspaceWillBeDeleted?t('Вы — единственный владелец. Аккаунт, рабочая зона и все данные будут удалены без 90-дневного ожидания. Доступ всех пользователей прекратится сразу.'):t('В рабочей зоне останется другой владелец. Будет удалён только ваш аккаунт и его сессии; компании и бизнес-данные сохранятся.')}</p>
      <div className={styles.impact}><article><span>{t('Пользователи')}</span><strong>{impact.data.counts.users}</strong></article><article><span>{t('Компании')}</span><strong>{impact.data.counts.companies}</strong></article><article><span>{t('Креаторы')}</span><strong>{impact.data.counts.creators}</strong></article></div>
      <form className={styles.form} onSubmit={submit}><div className={styles.formGrid}><Field label={t('Текущий пароль')}><input required minLength={12} type="password" autoComplete="current-password" value={password} onChange={e=>setPassword(e.target.value)}/></Field><Field label={t('Подтвердите текущий email')} hint={t('Введите адрес полностью')}><input required type="email" autoComplete="email" value={confirmEmail} onChange={e=>setConfirmEmail(e.target.value)}/></Field></div>{remove.isError?<p className={styles.error}>{t(remove.error.message)}</p>:null}<div className={styles.formActions}><DangerButton type="submit" disabled={remove.isPending||confirmEmail.trim().toLowerCase()!==principal?.email.toLowerCase()}>{remove.isPending?t('Удаляем…'):impact.data.workspaceWillBeDeleted?t('Удалить рабочую зону и аккаунт'):t('Удалить мой аккаунт')}</DangerButton></div></form>
    </>}</Panel>
  </section>
}

export function AccountDeletedPage(){
  const {t}=useI18n()
  const {state}=useLocation() as {state:{receiptId?:string|null;workspace?:boolean}|null}
  return <main className={styles.confirmation}><section><p className={styles.muted}>{t('УДАЛЕНИЕ ПОДТВЕРЖДЕНО')}</p><h1>{state?.workspace?t('Рабочая зона удаляется'):t('Аккаунт удалён')}</h1><p>{state?.workspace?t('Доступ уже закрыт. Система завершает локальное удаление данных и отзыв подключений.'):t('Ваши сессии закрыты. Данные рабочей зоны остаются у других владельцев.')}</p>{state?.receiptId?<code className={styles.receipt}>{t('Номер операции')}: {state.receiptId}</code>:null}<Link className={styles.linkButton} to="/">{t('На главную')}</Link></section></main>
}
