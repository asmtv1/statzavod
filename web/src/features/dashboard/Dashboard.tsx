import { useQuery } from '@tanstack/react-query'
import { api } from '../../shared/api/client'
import { useI18n } from '../../shared/i18n/I18nProvider'
import styles from './Dashboard.module.scss'
import { ViewsChart } from './ViewsChart'

export function Dashboard() {
  const { locale, t } = useI18n()
  const formatter = new Intl.NumberFormat(locale === 'en' ? 'en-US' : 'ru-RU')
  const summary = useQuery({ queryKey: ['summary', locale], queryFn: api.summary })
  const creators = useQuery({ queryKey: ['creators', locale], queryFn: api.creators })
  const sync = useQuery({ queryKey: ['sync-health', locale], queryFn: api.syncHealth })
  const timeseries = useQuery({ queryKey: ['timeseries', locale], queryFn: api.timeseries })

  if (summary.isPending) return <div className={styles.loading}>{t('Загружаем статистику…')}</div>
  if (summary.isError) return <div className={styles.error}>{t('Не удалось загрузить дашборд:')} {t(summary.error.message)}</div>

  return <section className={styles.dashboard}>
    <div className={styles.heading}>
      <div><p className={styles.eyebrow}>{t('ОБЗОР')}</p><h1>{t('Статистика креаторов')}</h1><p>{t('Показатели по подключённым платформам и публикациям.')}</p></div>
      <div className={styles.freshness}><b>{t('Данные частичные')}</b><span>{t(summary.data.freshness.message)}</span></div>
    </div>
    <div className={styles.kpis}>{summary.data.kpis.map(kpi => <article key={kpi.key}><span>{t(kpi.label)}</span><strong>{formatter.format(kpi.value)}</strong></article>)}</div>
    <article className={styles.card}>
      <div className={styles.cardHead}><h2>{t('Динамика просмотров')}</h2><span>{t('По снимкам')}</span></div>
      {timeseries.isPending ? <p>{t('Загрузка…')}</p> : timeseries.data ? <ViewsChart items={timeseries.data.items} /> : <p className={styles.empty}>{t('Данные временно недоступны.')}</p>}
    </article>
    <div className={styles.grid}>
      <article className={styles.card}>
        <div className={styles.cardHead}><h2>{t('Креаторы')}</h2><span>{creators.data?.items.length ?? 0}</span></div>
        {creators.isPending ? <p>{t('Загрузка…')}</p> : creators.data?.items.length ? <ul>{creators.data.items.map(creator => <li key={creator.id}><span className={styles.avatar}>{creator.displayName.charAt(0)}</span><div><b>{creator.displayName}</b><small>{creator.status === 'ACTIVE' ? t('Активен') : t(creator.status)}</small></div></li>)}</ul> : <p className={styles.empty}>{t('Создайте первого креатора, чтобы подключить аккаунты.')}</p>}
      </article>
      <article className={styles.card}>
        <div className={styles.cardHead}><h2>{t('Синхронизация')}</h2><span className={styles.success}>{sync.data?.status === 'healthy' ? t('В норме') : t('Проверка')}</span></div>
        <p className={styles.syncText}>{t('Задач ожидает:')} <b>{sync.data?.dueTargets ?? 0}</b></p>
        <p className={styles.muted}>{t('Сбор запустится автоматически после подключения платформы.')}</p>
      </article>
    </div>
  </section>
}
