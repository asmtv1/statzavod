import { FormEvent, useEffect, useMemo, useState } from 'react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { Link, useNavigate, useParams } from 'react-router-dom'
import { api, type ManagerCompanyAssignment, type ManagerPermission, type UserStatus, type WorkspaceRole, type WorkspaceUser } from '../../shared/api/client'
import { useAccess } from '../../shared/access/AccessProvider'
import { useI18n } from '../../shared/i18n/I18nProvider'
import { Button } from '../../shared/ui/Button'
import { DangerButton, Field, Modal, PageHeader, StatusBadge } from '../../shared/ui/Primitives'
import styles from './Management.module.scss'

const permissions: ManagerPermission[] = ['STATS_VIEW','STATS_EXPORT','CREATOR_CREATE','CREATOR_EDIT','CREATOR_ARCHIVE','CREATOR_DELETE','CREATOR_ACCOUNT_MANAGE','SOCIAL_CONNECT','SYNC_MANAGE','CREDENTIAL_EDIT','SECRET_REVEAL']
const permissionLabels: Record<ManagerPermission,string> = {
  STATS_VIEW:'Просмотр статистики',STATS_EXPORT:'Экспорт статистики',CREATOR_CREATE:'Создание креаторов',CREATOR_EDIT:'Редактирование креаторов',CREATOR_ARCHIVE:'Архивирование креаторов',CREATOR_DELETE:'Удаление креаторов',CREATOR_ACCOUNT_MANAGE:'Аккаунты креаторов',SOCIAL_CONNECT:'Подключение соцсетей',SYNC_MANAGE:'Управление синхронизацией',CREDENTIAL_EDIT:'Редактирование доступов',SECRET_REVEAL:'Просмотр секретов',
}

type UserDialog = {kind:'email'|'password'|'status'|'delete';user:WorkspaceUser}|null

function roleLabel(role: WorkspaceRole, t: (key:string)=>string) {
  return role === 'OWNER' ? t('Владелец') : role === 'MANAGER' ? t('Менеджер') : t('Креатор')
}

export function TeamPage() {
  const { locale, t } = useI18n()
  const { scopeKey, principal } = useAccess()
  const client = useQueryClient()
  const [createOpen,setCreateOpen] = useState(false)
  const [dialog,setDialog] = useState<UserDialog>(null)
  const [email,setEmail] = useState('')
  const [password,setPassword] = useState('')
  const [role,setRole] = useState<'OWNER'|'MANAGER'>('MANAGER')
  const users = useQuery({queryKey:['workspace-users',scopeKey,locale],queryFn:api.workspaceUsers})
  const companies = useQuery({queryKey:['companies',scopeKey,locale],queryFn:api.companies})
  const companyNames = useMemo(()=>new Map(companies.data?.items.map(company=>[company.id,company.name])),[companies.data])
  const invalidate = () => client.invalidateQueries({queryKey:['workspace-users']})
  const create = useMutation({mutationFn:()=>api.createWorkspaceUser({email,password,role}),onSuccess:async()=>{setCreateOpen(false);setEmail('');setPassword('');setRole('MANAGER');await invalidate()}})
  const update = useMutation({mutationFn:({id,payload}:{id:string;payload:{email?:string;status?:UserStatus}})=>api.updateWorkspaceUser(id,payload),onSuccess:async()=>{setDialog(null);setEmail('');await invalidate()}})
  const reset = useMutation({mutationFn:({id,password}:{id:string;password:string})=>api.resetWorkspaceUserPassword(id,password),onSuccess:()=>{setDialog(null);setPassword('')}})
  const remove = useMutation({mutationFn:api.deleteWorkspaceUser,onSuccess:async()=>{setDialog(null);await invalidate()}})
  const operationError = create.error ?? update.error ?? reset.error ?? remove.error

  function submitCreate(event:FormEvent){event.preventDefault();create.mutate()}
  function openDialog(kind:NonNullable<UserDialog>['kind'],user:WorkspaceUser){create.reset();update.reset();reset.reset();remove.reset();setEmail(user.email);setPassword('');setDialog({kind,user})}
  function submitDialog(event:FormEvent){event.preventDefault();if(!dialog)return;if(dialog.kind==='email')update.mutate({id:dialog.user.id,payload:{email}});if(dialog.kind==='password')reset.mutate({id:dialog.user.id,password});if(dialog.kind==='status')update.mutate({id:dialog.user.id,payload:{status:dialog.user.status==='ACTIVE'?'SUSPENDED':'ACTIVE'}});if(dialog.kind==='delete')remove.mutate(dialog.user.id)}

  return <section className={styles.page}>
    <PageHeader eyebrow={t('УПРАВЛЕНИЕ ДОСТУПОМ')} title={t('Команда')} description={t('Владельцы и менеджеры рабочей зоны, их статус и доступ к компаниям.')} actions={<Button onClick={()=>{create.reset();setCreateOpen(true)}}>{t('Создать пользователя')}</Button>}/>
    {users.isPending||companies.isPending?<div className={styles.state}>{t('Загружаем команду…')}</div>:users.isError||companies.isError?<div className={styles.error}>{(users.error??companies.error)?.message?t((users.error??companies.error)!.message):t('Не удалось загрузить команду')}</div>:<div className={styles.table}>
      <div className={styles.tableHead}><span>Email</span><span>{t('Роль')}</span><span>{t('Статус')}</span><span>{t('Компании')}</span><span>{t('Действия')}</span></div>
      {users.data.items.map(user=><div className={styles.row} key={user.id}>
        <div className={styles.identity}><b>{user.email}</b><small>{user.id}</small></div>
        <span>{roleLabel(user.role,t)}</span>
        <StatusBadge tone={user.status==='ACTIVE'?'success':'danger'}>{user.status==='ACTIVE'?t('Активен'):t('Заблокирован')}</StatusBadge>
        <div className={styles.assignments}>{user.role==='MANAGER'?user.companyAssignments.length?user.companyAssignments.map(item=><span className={styles.tag} key={item.companyId}>{companyNames.get(item.companyId)??t('Недоступная компания')}</span>):<span className={styles.muted}>{t('Нет назначений')}</span>:<span className={styles.muted}>{t('Все компании')}</span>}</div>
        <div className={styles.actions}>{user.role==='MANAGER'?<Link className={styles.linkButton} to={`/app/team/${user.id}`}>{t('Права')}</Link>:null}<button className={styles.secondary} onClick={()=>openDialog('email',user)}>{t('Email')}</button><button className={styles.secondary} onClick={()=>openDialog('password',user)}>{t('Пароль')}</button><button className={styles.secondary} onClick={()=>openDialog('status',user)}>{user.status==='ACTIVE'?t('Блокировать'):t('Разблокировать')}</button>{user.id===principal?.id?<Link className={styles.linkButton} to="/app/settings/account">{t('Настройки аккаунта')}</Link>:<button className={styles.iconButton} aria-label={t('Удалить пользователя')} onClick={()=>openDialog('delete',user)}>×</button>}</div>
      </div>)}
    </div>}
    <Modal open={createOpen} title={t('Новый пользователь')} description={t('Аккаунт создаётся сразу. Менеджеру можно выдать права после создания.')} closeLabel={t('Закрыть')} onClose={()=>!create.isPending&&setCreateOpen(false)}><form className={styles.form} onSubmit={submitCreate}><div className={styles.formGrid}><Field label={t('Роль')}><select value={role} onChange={event=>setRole(event.target.value as 'OWNER'|'MANAGER')}><option value="MANAGER">{t('Менеджер')}</option><option value="OWNER">{t('Владелец')}</option></select></Field><Field label="Email"><input required type="email" autoComplete="email" value={email} onChange={event=>setEmail(event.target.value)}/></Field><Field label={t('Пароль')} hint={t('Не менее 12 символов')}><input required minLength={12} type="password" autoComplete="new-password" value={password} onChange={event=>setPassword(event.target.value)}/></Field></div>{create.isError?<p className={styles.error}>{t(create.error.message)}</p>:null}<div className={styles.formActions}><button className={styles.secondary} type="button" onClick={()=>setCreateOpen(false)}>{t('Отмена')}</button><Button type="submit" disabled={create.isPending}>{create.isPending?t('Создаём…'):t('Создать')}</Button></div></form></Modal>
    <Modal open={Boolean(dialog)} title={dialog?.kind==='email'?t('Изменить email'):dialog?.kind==='password'?t('Сбросить пароль'):dialog?.kind==='status'?(dialog.user.status==='ACTIVE'?t('Заблокировать пользователя'):t('Разблокировать пользователя')):t('Удалить пользователя')} description={dialog?.kind==='delete'?t('Бизнес-данные сохранятся, а все сессии пользователя будут отозваны.'):dialog?.kind==='status'?t('Изменение статуса немедленно отзывает все сессии.'):undefined} closeLabel={t('Закрыть')} onClose={()=>setDialog(null)}>{dialog?<form className={styles.form} onSubmit={submitDialog}>{dialog.kind==='email'?<Field label="Email"><input required type="email" value={email} onChange={event=>setEmail(event.target.value)}/></Field>:dialog.kind==='password'?<Field label={t('Новый пароль')} hint={t('Не менее 12 символов')}><input required minLength={12} type="password" autoComplete="new-password" value={password} onChange={event=>setPassword(event.target.value)}/></Field>:<p className={dialog.kind==='delete'?styles.error:styles.notice}>{dialog.user.email}</p>}{operationError?<p className={styles.error}>{t(operationError.message)}</p>:null}<div className={styles.formActions}><button className={styles.secondary} type="button" onClick={()=>setDialog(null)}>{t('Отмена')}</button>{dialog.kind==='delete'?<DangerButton type="submit" disabled={remove.isPending}>{remove.isPending?t('Удаляем…'):t('Удалить')}</DangerButton>:<Button type="submit" disabled={update.isPending||reset.isPending}>{update.isPending||reset.isPending?t('Сохраняем…'):t('Подтвердить')}</Button>}</div></form>:null}</Modal>
  </section>
}

export function TeamPermissionsPage(){
  const {userId=''}=useParams()
  const navigate=useNavigate()
  const {locale,t}=useI18n()
  const {scopeKey}=useAccess()
  const client=useQueryClient()
  const users=useQuery({queryKey:['workspace-users',scopeKey,locale],queryFn:api.workspaceUsers})
  const companies=useQuery({queryKey:['companies',scopeKey,locale],queryFn:api.companies})
  const user=users.data?.items.find(item=>item.id===userId)
  const [assignments,setAssignments]=useState<ManagerCompanyAssignment[]>([])
  useEffect(()=>{if(user)setAssignments(user.companyAssignments.map(item=>({...item,permissions:[...item.permissions]})))},[user])
  const save=useMutation({mutationFn:()=>api.replaceManagerCompanyAssignments(userId,assignments),onSuccess:async()=>{await client.invalidateQueries({queryKey:['workspace-users']});navigate('/app/team')}})
  const assignmentMap=useMemo(()=>new Map(assignments.map(item=>[item.companyId,item.permissions])),[assignments])
  function toggleCompany(companyId:string,enabled:boolean){setAssignments(current=>enabled?[...current,{companyId,permissions:[]}]:current.filter(item=>item.companyId!==companyId))}
  function togglePermission(companyId:string,permission:ManagerPermission,enabled:boolean){setAssignments(current=>current.map(item=>item.companyId===companyId?{...item,permissions:enabled?[...item.permissions,permission]:item.permissions.filter(value=>value!==permission)}:item))}
  if(users.isPending||companies.isPending)return <p>{t('Загружаем права…')}</p>
  if(users.isError||companies.isError)return <p className={styles.error}>{(users.error??companies.error)?.message?t((users.error??companies.error)!.message):t('Не удалось загрузить права')}</p>
  if(!user||user.role!=='MANAGER')return <section className={styles.page}><PageHeader title={t('Пользователь не найден')}/><Link className={styles.linkButton} to="/app/team">← {t('Команда')}</Link></section>
  return <section className={styles.page}>
    <PageHeader eyebrow={t('АТОМАРНАЯ МАТРИЦА')} title={t('Права менеджера')} description={user.email} actions={<><Link className={styles.linkButton} to="/app/team">{t('Отмена')}</Link><Button onClick={()=>save.mutate()} disabled={save.isPending}>{save.isPending?t('Сохраняем…'):t('Сохранить права')}</Button></>}/>
    <p className={styles.notice}>{t('Назначение компании даёт просмотр компании и карточек. Каждое дополнительное действие включается отдельно.')}</p>
    <div className={styles.permissionTable}><div className={styles.permissionGrid}><div className={styles.permissionHeader}><span>{t('Компания')}</span>{permissions.map(permission=><span key={permission}>{t(permissionLabels[permission])}</span>)}</div>{companies.data.items.map(company=>{const current=assignmentMap.get(company.id);return <div className={styles.permissionRow} key={company.id}><label className={styles.companyToggle}><input type="checkbox" checked={Boolean(current)} onChange={event=>toggleCompany(company.id,event.target.checked)}/><span>{company.name}</span></label>{permissions.map(permission=><label key={permission} title={t(permissionLabels[permission])}><input type="checkbox" aria-label={`${company.name}: ${t(permissionLabels[permission])}`} disabled={!current} checked={current?.includes(permission)??false} onChange={event=>togglePermission(company.id,permission,event.target.checked)}/></label>)}</div>})}</div></div>
    {save.isError?<p className={styles.error}>{t(save.error.message)}</p>:null}
  </section>
}
