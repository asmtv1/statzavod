import { FormEvent, useState } from 'react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { api, type Creator, type Platform } from '../../shared/api/client'
import { Button } from '../../shared/ui/Button'
import { Link } from 'react-router-dom'
import styles from './CreatorsPage.module.scss'
import statusStyles from './CreatorStatus.module.scss'

const statusLabels = { ACTIVE:'Активен', ON_LEAVE:'В отпуске', DISMISSED:'Уволен', ARCHIVED:'Архив' } as const
const archiveDateFormatter = new Intl.DateTimeFormat('ru-RU', { day: 'numeric', month: 'short', year: 'numeric' })
type WorkSort = 'DEFAULT' | Creator['workStatus']
const platformMeta: Record<Platform, { label: string; icon: string }> = {
  YOUTUBE: { label: 'YouTube', icon: '/platforms/youtube.svg' },
  INSTAGRAM: { label: 'Instagram', icon: '/platforms/instagram.svg' },
  TIKTOK: { label: 'TikTok', icon: '/platforms/tiktok.svg' },
  VK: { label: 'VK', icon: '/platforms/vk.svg' },
}

type CreatorForm = { firstName: string; lastName: string; middleName: string; displayName: string; telegramUsername: string; internalNote: string; companyId: string }
const emptyForm: CreatorForm = { firstName: '', lastName: '', middleName: '', displayName: '', telegramUsername: '', internalNote: '', companyId: '' }

function CreatorWorkCell({ workStatus, workComment }: Pick<Creator, 'workStatus'|'workComment'>) {
  if (workStatus === 'OK') return <span className={styles.workOk}>Всё ок</span>
  if (workStatus === 'NEEDS_ATTENTION') return <span className={styles.workAttention}>{workComment}</span>
  return <div className={styles.workCell}><span className={styles.workInProgress}>В работе</span><span className={styles.workComment}>{workComment}</span></div>
}

function CreatorPlatforms({ platforms }: { platforms: Platform[] }) {
  if (!platforms?.length) return <span className={styles.noPlatforms}>—</span>
  return <span className={styles.platforms} aria-label={`Подключены: ${platforms.map(platform => platformMeta[platform].label).join(', ')}`}>
    {platforms.map(platform => <span className={styles.platformIcon} key={platform} title={platformMeta[platform].label}><img src={platformMeta[platform].icon} alt="" /></span>)}
  </span>
}

export function CreatorsPage() {
  const [open, setOpen] = useState(false)
  const [form, setForm] = useState<CreatorForm>(emptyForm)
  const [companyFilter, setCompanyFilter] = useState('ALL')
  const [workSort, setWorkSort] = useState<WorkSort>('DEFAULT')
  const [scope, setScope] = useState<'active'|'archived'>('active')
  const [error, setError] = useState('')
  const queryClient = useQueryClient()
  const creators = useQuery({ queryKey: ['creators', scope], queryFn: scope === 'archived' ? api.archivedCreators : api.creators })
  const companies = useQuery({ queryKey: ['companies'], queryFn: api.companies })
  const visibleCreators = (creators.data?.items.filter(creator => companyFilter === 'ALL' || (companyFilter === 'NONE' ? !creator.companyId : creator.companyId === companyFilter)) ?? []).sort((left, right) => {
    if (workSort === 'DEFAULT') return 0
    return Number(right.workStatus === workSort) - Number(left.workStatus === workSort)
  })
  const create = useMutation({
    mutationFn: api.createCreator,
    onSuccess: async () => { setForm(emptyForm); setError(''); setOpen(false); await queryClient.invalidateQueries({ queryKey: ['creators'] }); await queryClient.invalidateQueries({ queryKey: ['summary'] }) },
    onError: (reason) => setError(reason instanceof Error ? reason.message : 'Не удалось создать креатора'),
  })
  function submit(event: FormEvent) { event.preventDefault(); setError(''); create.mutate(form) }
  return <section className={styles.page}>
    <header className={styles.header}><div><p className={styles.eyebrow}>БАЗА КРЕАТОРОВ</p><h1>Креаторы</h1><p>Управляйте карточками креаторов и подключёнными аккаунтами.</p></div>{scope === 'active' ? <Button onClick={() => setOpen(true)}>Добавить креатора</Button> : null}</header>
    <div className={styles.filters}><div className={styles.scopeTabs} aria-label="Раздел креаторов"><button type="button" className={scope === 'active' ? styles.scopeActive : ''} onClick={() => { setScope('active'); setCompanyFilter('ALL') }}>Рабочие</button><button type="button" className={scope === 'archived' ? styles.scopeActive : ''} onClick={() => { setScope('archived'); setCompanyFilter('ALL') }}>Архив</button></div>{creators.data?.items.length ? <><label>Компания<select value={companyFilter} onChange={event => setCompanyFilter(event.target.value)}><option value="ALL">Все компании</option>{companies.data?.items.map(company => <option value={company.id} key={company.id}>{company.name}</option>)}<option value="NONE">Без компании</option></select></label><label>Сортировка работ<select value={workSort} onChange={event => setWorkSort(event.target.value as WorkSort)}><option value="DEFAULT">Без сортировки</option><option value="OK">Сверху: всё ок</option><option value="NEEDS_ATTENTION">Сверху: нужны работы</option><option value="IN_PROGRESS">Сверху: в работе</option></select></label></> : null}<span>Найдено: {visibleCreators.length}</span></div>
    {creators.isPending ? <div className={styles.state}>Загружаем список…</div> : creators.isError ? <div className={styles.error}>Не удалось получить креаторов: {creators.error.message}</div> : creators.data.items.length === 0 ? scope === 'archived' ? <div className={styles.empty}><h2>Архив пуст</h2><p>Сюда попадут карточки, убранные из рабочих списков. Их можно будет восстановить в любой момент.</p></div> : <div className={styles.empty}><h2>Креаторов пока нет</h2><p>Создайте первую карточку, затем добавьте контакты и подключите аккаунты платформ.</p><Button onClick={() => setOpen(true)}>Добавить первого креатора</Button></div> : visibleCreators.length === 0 ? <div className={styles.state}>В этой компании креаторов пока нет.</div> : <div className={styles.table}><div className={styles.tableHead}><span>Креатор</span><span>Telegram</span><span>Компания</span><span>Платформы</span><span>Статус</span><span>Работы</span></div>{visibleCreators.map((creator) => <Link className={styles.creatorRow} key={creator.id} to={`/app/creators/${creator.id}`}><div className={styles.creator}><span className={styles.avatar}>{creator.displayName.slice(0, 1)}</span><div><b>{creator.displayName}</b><small>{creator.archivedAt ? `В архиве с ${archiveDateFormatter.format(new Date(creator.archivedAt))}` : [creator.lastName, creator.firstName, creator.middleName].filter(Boolean).join(' ')}</small></div></div><span className={creator.telegramUsername ? styles.telegram : styles.unassigned}>{creator.telegramUsername ? `@${creator.telegramUsername}` : 'Не указан'}</span><span className={creator.companyName ? styles.company : styles.unassigned}>{creator.companyName || 'Без компании'}</span><CreatorPlatforms platforms={creator.connectedPlatforms}/><span className={`${statusStyles.status} ${creator.status === 'ACTIVE' ? statusStyles.active : creator.status === 'ON_LEAVE' ? statusStyles.onLeave : statusStyles.dismissed}`}>{statusLabels[creator.status]}</span><CreatorWorkCell workStatus={creator.workStatus} workComment={creator.workComment}/></Link>)}</div>}
    {open && <div className={styles.backdrop} role="presentation" onMouseDown={() => !create.isPending && setOpen(false)}><form className={styles.dialog} onSubmit={submit} onMouseDown={(event) => event.stopPropagation()}><div className={styles.dialogHead}><div><p className={styles.eyebrow}>НОВАЯ КАРТОЧКА</p><h2>Добавить креатора</h2></div><button type="button" className={styles.close} aria-label="Закрыть" onClick={() => setOpen(false)}>×</button></div><div className={styles.fields}><label>Имя<input required value={form.firstName} onChange={(e) => setForm({ ...form, firstName: e.target.value })} autoFocus /></label><label>Фамилия<input required value={form.lastName} onChange={(e) => setForm({ ...form, lastName: e.target.value })} /></label><label>Отчество<input value={form.middleName} onChange={(e) => setForm({ ...form, middleName: e.target.value })} /></label><label>Отображаемое имя<input placeholder="Если не указать — сформируется автоматически" value={form.displayName} onChange={(e) => setForm({ ...form, displayName: e.target.value })} /></label><label className={styles.note}>Компания<select value={form.companyId} onChange={(e) => setForm({ ...form, companyId: e.target.value })}><option value="">Без компании</option>{companies.data?.items.map(company => <option value={company.id} key={company.id}>{company.name}</option>)}</select></label><label className={styles.note}>Telegram<input placeholder="@username или t.me/username" value={form.telegramUsername} onChange={(e) => setForm({ ...form, telegramUsername: e.target.value })} /></label><label className={styles.note}>Внутренний комментарий<textarea value={form.internalNote} onChange={(e) => setForm({ ...form, internalNote: e.target.value })} rows={3} /></label></div>{error && <p className={styles.error}>{error}</p>}<footer><button type="button" className={styles.cancel} onClick={() => setOpen(false)}>Отмена</button><Button type="submit" disabled={create.isPending}>{create.isPending ? 'Создаём…' : 'Создать креатора'}</Button></footer></form></div>}
  </section>
}
