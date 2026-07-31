import { FormEvent, useState } from 'react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { api, type Company } from '../../shared/api/client'
import { Button } from '../../shared/ui/Button'
import styles from './CompaniesPage.module.scss'

export function CompaniesPage() {
  const [name, setName] = useState('')
  const [pendingArchive, setPendingArchive] = useState<Company | null>(null)
  const queryClient = useQueryClient()
  const companies = useQuery({ queryKey: ['companies'], queryFn: api.companies })
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
    {companies.isPending ? <div className={styles.state}>Загружаем компании…</div> : companies.isError ? <div className={styles.error}>{companies.error.message}</div> : companies.data.items.length ? <div className={styles.grid}>{companies.data.items.map(company => <article key={company.id}>
      <div className={styles.mark}>{company.name.slice(0, 1).toUpperCase()}</div>
      <div><h2>{company.name}</h2><p>{company.creatorCount ? `${company.creatorCount} ${company.creatorCount === 1 ? 'креатор' : 'креаторов'}` : 'Креаторы ещё не назначены'}</p></div>
      <button className={styles.archive} onClick={() => setPendingArchive(company)}>Архивировать</button>
    </article>)}</div> : <div className={styles.state}>Компаний пока нет. Создайте первую выше.</div>}
    {pendingArchive ? <div className={styles.backdrop} role="presentation" onMouseDown={event => { if (event.target === event.currentTarget && !archive.isPending) setPendingArchive(null) }}><div className={styles.dialog} role="dialog" aria-modal="true"><h2>Архивировать «{pendingArchive.name}»?</h2><p>Компания исчезнет из списка, а её креаторы перейдут в категорию «Без компании». Карточки и статистика сохранятся.</p>{archive.isError ? <p className={styles.error}>{archive.error.message}</p> : null}<footer><button onClick={() => setPendingArchive(null)} disabled={archive.isPending}>Отмена</button><button className={styles.confirm} onClick={() => archive.mutate(pendingArchive.id)} disabled={archive.isPending}>{archive.isPending ? 'Архивируем…' : 'Архивировать'}</button></footer></div></div> : null}
  </section>
}
