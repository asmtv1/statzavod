import { FormEvent, useState } from 'react'
import { Link, useSearchParams } from 'react-router-dom'
import { api } from '../../shared/api/client'
import styles from '../../app/App.module.scss'

export function AcceptInvitationPage() {
  const [params] = useSearchParams(); const [password,setPassword]=useState(''); const [message,setMessage]=useState(''); const [error,setError]=useState(''); const token=params.get('token') ?? ''
  async function submit(event: FormEvent) { event.preventDefault(); setError(''); if (!token) { setError('Ссылка-приглашение не содержит токен.'); return }; try { const result=await api.acceptInvitation(token,password); setMessage(`Аккаунт ${result.email} активирован. Теперь можно войти.`) } catch (err) { setError(err instanceof Error ? err.message : 'Не удалось принять приглашение') } }
  return <main className={styles.login}><form onSubmit={submit}><p className={styles.brand}>СТАТЗАВОД</p><h1>Принять приглашение</h1><label>Новый пароль<input type="password" value={password} minLength={12} onChange={event=>setPassword(event.target.value)} autoComplete="new-password" required /></label>{error ? <p className={styles.formError}>{error}</p> : null}{message ? <p>{message}</p> : <button type="submit">Активировать доступ</button>}<Link to="/login">Ко входу</Link></form></main>
}
