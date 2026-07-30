import { FormEvent, useState } from 'react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { api } from '../../shared/api/client'
import { Button } from '../../shared/ui/Button'
import { Link } from 'react-router-dom'
import styles from './CreatorsPage.module.scss'
import statusStyles from './CreatorStatus.module.scss'

const statusLabels = { ACTIVE:'Активен', ON_LEAVE:'В отпуске', DISMISSED:'Уволен' } as const

type CreatorForm = { firstName: string; lastName: string; middleName: string; displayName: string; telegramUsername: string; internalNote: string }
const emptyForm: CreatorForm = { firstName: '', lastName: '', middleName: '', displayName: '', telegramUsername: '', internalNote: '' }

export function CreatorsPage() {
  const [open, setOpen] = useState(false)
  const [form, setForm] = useState<CreatorForm>(emptyForm)
  const [error, setError] = useState('')
  const queryClient = useQueryClient()
  const creators = useQuery({ queryKey: ['creators'], queryFn: api.creators })
  const create = useMutation({
    mutationFn: api.createCreator,
    onSuccess: async () => { setForm(emptyForm); setError(''); setOpen(false); await queryClient.invalidateQueries({ queryKey: ['creators'] }); await queryClient.invalidateQueries({ queryKey: ['summary'] }) },
    onError: (reason) => setError(reason instanceof Error ? reason.message : 'Не удалось создать креатора'),
  })
  function submit(event: FormEvent) { event.preventDefault(); setError(''); create.mutate(form) }
  return <section className={styles.page}>
    <header className={styles.header}><div><p className={styles.eyebrow}>БАЗА КРЕАТОРОВ</p><h1>Креаторы</h1><p>Управляйте карточками креаторов и подключёнными аккаунтами.</p></div><Button onClick={() => setOpen(true)}>Добавить креатора</Button></header>
    {creators.isPending ? <div className={styles.state}>Загружаем список…</div> : creators.isError ? <div className={styles.error}>Не удалось получить креаторов: {creators.error.message}</div> : creators.data.items.length === 0 ? <div className={styles.empty}><h2>Креаторов пока нет</h2><p>Создайте первую карточку, затем добавьте контакты и подключите аккаунты платформ.</p><Button onClick={() => setOpen(true)}>Добавить первого креатора</Button></div> : <div className={styles.table}><div className={styles.tableHead}><span>Креатор</span><span>Статус</span><span>Создан</span></div>{creators.data.items.map((creator) => <Link className={styles.creatorRow} key={creator.id} to={`/app/creators/${creator.id}`}><div className={styles.creator}><span className={styles.avatar}>{creator.displayName.slice(0, 1)}</span><div><b>{creator.displayName}</b><small>{[creator.lastName, creator.firstName, creator.middleName].filter(Boolean).join(' ')}</small></div></div><span className={`${statusStyles.status} ${creator.status === 'ACTIVE' ? statusStyles.active : creator.status === 'ON_LEAVE' ? statusStyles.onLeave : statusStyles.dismissed}`}>{statusLabels[creator.status]}</span><time dateTime={creator.createdAt}>{new Intl.DateTimeFormat('ru-RU', { dateStyle: 'medium' }).format(new Date(creator.createdAt))}</time></Link>)}</div>}
    {open && <div className={styles.backdrop} role="presentation" onMouseDown={() => !create.isPending && setOpen(false)}><form className={styles.dialog} onSubmit={submit} onMouseDown={(event) => event.stopPropagation()}><div className={styles.dialogHead}><div><p className={styles.eyebrow}>НОВАЯ КАРТОЧКА</p><h2>Добавить креатора</h2></div><button type="button" className={styles.close} aria-label="Закрыть" onClick={() => setOpen(false)}>×</button></div><div className={styles.fields}><label>Имя<input required value={form.firstName} onChange={(e) => setForm({ ...form, firstName: e.target.value })} autoFocus /></label><label>Фамилия<input required value={form.lastName} onChange={(e) => setForm({ ...form, lastName: e.target.value })} /></label><label>Отчество<input value={form.middleName} onChange={(e) => setForm({ ...form, middleName: e.target.value })} /></label><label>Отображаемое имя<input placeholder="Если не указать — сформируется автоматически" value={form.displayName} onChange={(e) => setForm({ ...form, displayName: e.target.value })} /></label><label className={styles.note}>Telegram<input placeholder="@username или t.me/username" value={form.telegramUsername} onChange={(e) => setForm({ ...form, telegramUsername: e.target.value })} /></label><label className={styles.note}>Внутренний комментарий<textarea value={form.internalNote} onChange={(e) => setForm({ ...form, internalNote: e.target.value })} rows={3} /></label></div>{error && <p className={styles.error}>{error}</p>}<footer><button type="button" className={styles.cancel} onClick={() => setOpen(false)}>Отмена</button><Button type="submit" disabled={create.isPending}>{create.isPending ? 'Создаём…' : 'Создать креатора'}</Button></footer></form></div>}
  </section>
}
