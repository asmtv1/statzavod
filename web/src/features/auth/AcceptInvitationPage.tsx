import { FormEvent, useEffect, useRef, useState } from 'react'
import { Link, useLocation, useNavigate, useSearchParams } from 'react-router-dom'
import { api } from '../../shared/api/client'
import styles from '../../app/App.module.scss'
import { useI18n, type Locale } from '../../shared/i18n/I18nProvider'

export function AcceptInvitationPage() {
  const [params] = useSearchParams()
  const [password, setPassword] = useState('')
  const [message, setMessage] = useState('')
  const [error, setError] = useState('')
  const location = useLocation()
  const navigate = useNavigate()
  const { locale, t } = useI18n()
  const localeRef = useRef(locale)
  const token = params.get('token') ?? ''

  useEffect(() => {
    localeRef.current = locale
    setError('')
    setMessage('')
  }, [locale])

  function changeLocale(next: Locale) {
    const pathname = next === 'en' ? '/en/accept-invitation' : '/accept-invitation'
    navigate({ pathname, search: location.search }, { replace: true })
  }

  async function submit(event: FormEvent) {
    event.preventDefault()
    setError('')
    const requestLocale = locale
    if (!token) {
      setError(t('Ссылка-приглашение не содержит токен.'))
      return
    }
    try {
      const result = await api.acceptInvitation(token, password)
      if (localeRef.current !== requestLocale) return
      setMessage(t('Аккаунт {email} активирован. Теперь можно войти.', { email: result.email }))
    } catch (err) {
      if (localeRef.current !== requestLocale) return
      setError(err instanceof Error ? err.message : t('Не удалось принять приглашение'))
    }
  }

  return <main className={styles.login}>
    <form onSubmit={submit}>
      <div className={styles.languageToggle}><div role="group" aria-label="Language"><button type="button" aria-pressed={locale === 'ru'} onClick={() => changeLocale('ru')}>RU</button><button type="button" aria-pressed={locale === 'en'} onClick={() => changeLocale('en')}>EN</button></div></div>
      <p className={styles.brand}>{t('СТАТЗАВОД')}</p>
      <h1>{t('Принять приглашение')}</h1>
      <label>{t('Новый пароль')}<input type="password" value={password} minLength={12} onChange={event => setPassword(event.target.value)} autoComplete="new-password" required /></label>
      {error ? <p className={styles.formError}>{error}</p> : null}
      {message ? <p>{message}</p> : <button type="submit">{t('Активировать доступ')}</button>}
      <Link to="/login">{t('Ко входу')}</Link>
    </form>
  </main>
}
