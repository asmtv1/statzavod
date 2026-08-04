import { useQuery } from '@tanstack/react-query'
import { useParams, useSearchParams } from 'react-router-dom'
import { api } from '../../shared/api/client'
import styles from './InstagramConnectionPage.module.scss'

const resultCopy: Record<string, { mark: string; title: string; detail: string; success?: boolean }> = {
  connected: { mark: '✓', title: 'Instagram подключён', detail: 'Доступ подтверждён. Статзавод начнёт загружать доступную статистику аккаунта.', success: true },
  denied: { mark: '×', title: 'Доступ не подтверждён', detail: 'Вы отменили подключение. Если это произошло случайно, откройте исходную ссылку ещё раз.' },
  expired: { mark: '⌛', title: 'Ссылка больше не действует', detail: 'Попросите сотрудника, который отправил ссылку, выпустить новую.' },
  unavailable: { mark: '!', title: 'Instagram временно недоступен', detail: 'Настройка подключения ещё не завершена. Сообщите об этом сотруднику Статзавода.' },
  'provider-error': { mark: '!', title: 'Instagram не завершил подключение', detail: 'Откройте исходную ссылку и попробуйте ещё раз. Если ошибка повторится, сообщите сотруднику Статзавода.' },
  'save-error': { mark: '!', title: 'Не удалось сохранить подключение', detail: 'Откройте исходную ссылку и попробуйте ещё раз.' },
  'server-error': { mark: '!', title: 'Не удалось начать подключение', detail: 'Попробуйте открыть исходную ссылку позднее.' },
  'state-error': { mark: '!', title: 'Сессия подключения повреждена', detail: 'Откройте исходную ссылку и начните подключение заново.' },
  'missing-code': { mark: '!', title: 'Instagram не подтвердил доступ', detail: 'Откройте исходную ссылку и попробуйте ещё раз.' },
}

export function InstagramConnectionPage() {
  const { token = '' } = useParams()
  const invitation = useQuery({
    queryKey: ['instagram-connection-invitation', token],
    queryFn: () => api.instagramConnectionInvitationInfo(token),
    enabled: Boolean(token),
    retry: false,
  })

  return <main className={styles.page}>
    <section className={styles.card}>
      <p className={styles.brand}>СТАТЗАВОД</p>
      {invitation.isPending ? <><span className={styles.mark}>…</span><h1>Проверяем ссылку</h1><p className={styles.lead}>Это займёт несколько секунд.</p></> : invitation.isError ? <><span className={`${styles.mark} ${styles.errorMark}`}>⌛</span><h1>Ссылка недоступна</h1><p className={styles.lead}>Она истекла, была отозвана или уже использована. Попросите сотрудника выпустить новую.</p></> : <>
        <span className={styles.instagramMark}>◎</span>
        <h1>Подключение Instagram</h1>
        <p className={styles.lead}>Вы подтверждаете доступ к статистике Instagram для профиля креатора <strong>{invitation.data.creatorName}</strong>.</p>
        <div className={styles.notice}><b>Что произойдёт</b><span>Instagram покажет запрашиваемые разрешения. Статзавод не получит ваш пароль и не даст вам доступ к внутреннему кабинету компании.</span></div>
        <button type="button" onClick={() => window.location.assign(api.instagramConnectionInvitationAuthorizationUrl(token))}>Продолжить через Instagram</button>
        <small>Ссылка действует до {new Date(invitation.data.expiresAt).toLocaleString('ru-RU')} и закроется после успешного подключения.</small>
      </>}
    </section>
  </main>
}

export function InstagramConnectionResultPage() {
  const [params] = useSearchParams()
  const result = resultCopy[params.get('oauth') ?? ''] ?? resultCopy['server-error']
  return <main className={styles.page}>
    <section className={styles.card}>
      <p className={styles.brand}>СТАТЗАВОД</p>
      <span className={`${styles.mark} ${result.success ? styles.successMark : styles.errorMark}`}>{result.mark}</span>
      <h1>{result.title}</h1>
      <p className={styles.lead}>{result.detail}</p>
      {result.success ? <p className={styles.done}>Эту страницу можно закрыть.</p> : null}
    </section>
  </main>
}
