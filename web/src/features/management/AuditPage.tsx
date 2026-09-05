import { useMemo, useState } from 'react'
import { useQuery } from '@tanstack/react-query'
import { api, type AuditLog } from '../../shared/api/client'
import { useAccess } from '../../shared/access/AccessProvider'
import { useI18n } from '../../shared/i18n/I18nProvider'
import { Modal, PageHeader, Panel } from '../../shared/ui/Primitives'
import styles from './Management.module.scss'

const sensitiveKey=/(password|hash|cookie|token|cipher|secret|request.?body|credential|authorization|api.?key|private.?key|session|nonce)/i
function safeMetadata(value:unknown):unknown{
  if(Array.isArray(value))return value.map(safeMetadata)
  if(value&&typeof value==='object')return Object.fromEntries(Object.entries(value as Record<string,unknown>).map(([key,child])=>[key,sensitiveKey.test(key)?'[REDACTED]':safeMetadata(child)]))
  return value
}

export function AuditPage(){
  const {locale,t}=useI18n()
  const {scopeKey}=useAccess()
  const [filters,setFilters]=useState({actorId:'',companyId:'',action:'',entityType:'',dateFrom:'',dateTo:''})
  const [offset,setOffset]=useState(0)
  const [selected,setSelected]=useState<AuditLog|null>(null)
  const limit=25
  const users=useQuery({queryKey:['workspace-users',scopeKey,locale],queryFn:api.workspaceUsers})
  const companies=useQuery({queryKey:['companies',scopeKey,locale],queryFn:api.companies})
  const audit=useQuery({queryKey:['audit',scopeKey,locale,filters,offset],queryFn:()=>api.auditLogs({actorId:filters.actorId,companyId:filters.companyId,action:filters.action,entityType:filters.entityType,limit,offset,dateFrom:filters.dateFrom,dateTo:filters.dateTo})})
  const actorNames=useMemo(()=>new Map(users.data?.items.map(user=>[user.id,user.email])),[users.data])
  const companyNames=useMemo(()=>new Map(companies.data?.items.map(company=>[company.id,company.name])),[companies.data])
  const formatter=new Intl.DateTimeFormat(locale==='en'?'en-US':'ru-RU',{dateStyle:'short',timeStyle:'short'})
  function change(name:keyof typeof filters,value:string){setFilters(current=>({...current,[name]:value}));setOffset(0)}
  return <section className={styles.page}>
    <PageHeader eyebrow={t('БЕЗ СЕКРЕТОВ')} title={t('Аудит')} description={t('Действия пользователей и системы. Чувствительные значения удаляются до сохранения записи.')}/>
    <Panel className={styles.filterGrid} aria-label={t('Фильтры аудита')}>
      <label>{t('Пользователь')}<select value={filters.actorId} onChange={e=>change('actorId',e.target.value)}><option value="">{t('Все')}</option>{users.data?.items.map(user=><option key={user.id} value={user.id}>{user.email}</option>)}</select></label>
      <label>{t('Компания')}<select value={filters.companyId} onChange={e=>change('companyId',e.target.value)}><option value="">{t('Все')}</option>{companies.data?.items.map(company=><option key={company.id} value={company.id}>{company.name}</option>)}</select></label>
      <label>{t('Действие')}<input value={filters.action} onChange={e=>change('action',e.target.value)} placeholder="HTTP_GET…"/></label>
      <label>{t('Ресурс')}<input value={filters.entityType} onChange={e=>change('entityType',e.target.value)} placeholder="CREATOR"/></label>
      <label>{t('С даты')}<input type="date" value={filters.dateFrom} onChange={e=>change('dateFrom',e.target.value)}/></label>
      <label>{t('По дату')}<input type="date" value={filters.dateTo} onChange={e=>change('dateTo',e.target.value)}/></label>
    </Panel>
    {audit.isPending?<div className={styles.state}>{t('Загружаем аудит…')}</div>:audit.isError?<div className={styles.error}>{t(audit.error.message)}</div>:<><div className={`${styles.table} ${styles.auditTable}`}><div className={styles.auditGrid}><div className={styles.auditHead}><span>{t('Время')}</span><span>{t('Пользователь')}</span><span>{t('Компания')}</span><span>{t('Ресурс')}</span><span>{t('Действие')}</span><span></span></div>{audit.data.items.map(item=><div className={styles.auditRow} key={item.id}><time>{formatter.format(new Date(item.createdAt))}</time><span>{item.actorId?actorNames.get(item.actorId)??t('Удалённый пользователь'):t('Система')}</span><span>{item.companyId?companyNames.get(item.companyId)??t('Архивная компания'):'—'}</span><span>{item.entityType}{item.entityId?<small className={styles.muted}> · {item.entityId.slice(0,8)}</small>:null}</span><code>{item.action}</code><button className={styles.secondary} onClick={()=>setSelected(item)}>{t('Детали')}</button></div>)}</div></div><div className={styles.pagination}><button className={styles.secondary} disabled={offset===0} onClick={()=>setOffset(Math.max(0,offset-limit))}>{t('Назад')}</button><span>{offset+1}–{offset+audit.data.items.length}</span><button className={styles.secondary} disabled={audit.data.items.length<limit} onClick={()=>setOffset(offset+limit)}>{t('Далее')}</button></div></>}
    <Modal open={Boolean(selected)} title={t('Детали события')} description={selected?.action} closeLabel={t('Закрыть')} onClose={()=>setSelected(null)}>{selected?<dl className={styles.detailList}><div><dt>ID</dt><dd>{selected.id}</dd></div><div><dt>{t('Маршрут')}</dt><dd>{selected.httpMethod} {selected.endpoint}</dd></div><div><dt>{t('Ответ')}</dt><dd>{selected.responseStatus??'—'}</dd></div><div><dt>{t('Метаданные')}</dt><dd><pre className={styles.metadata}>{JSON.stringify(safeMetadata(selected.metadata),null,2)}</pre></dd></div></dl>:null}</Modal>
  </section>
}
