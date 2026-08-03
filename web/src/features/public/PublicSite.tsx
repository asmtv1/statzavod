import { Link } from 'react-router-dom'
import styles from './PublicSite.module.scss'
import visualStyles from './BenefitVisual.module.scss'
import launchStyles from './LaunchVisual.module.scss'

type Language = 'ru' | 'en'
const copy = {
  ru: {
    product: 'ПЛАТФОРМА ДЛЯ АНАЛИТИКИ КОНТЕНТА',
    title: 'Данные, которые двигают контент вперёд.',
    lead: 'Statzavod помогает командам и креаторам подключать TikTok-аккаунты с согласия владельца и видеть профиль, статистику аккаунта и аналитику публичных видео в едином кабинете.',
    signIn: 'Войти в кабинет', requestAccess: 'Запросить доступ', features: 'Возможности', security: 'Безопасность', support: 'Поддержка',
    featureTitle: 'Единая картина эффективности контента',
    featureBody: 'Подключайте TikTok-аккаунты с разрешения владельца, собирайте данные профиля и публичных видео, сравнивайте креаторов и кампании, находите работающие форматы.',
    securityTitle: 'Надёжная основа для ваших данных',
    securityBody: 'Подключения выполняются через защищённую авторизацию TikTok. Мы запрашиваем только данные, необходимые для работы аналитики, а доступы можно отозвать.',
    supportTitle: 'Поддержка и контакты', supportBody: 'По вопросам о сервисе, доступе к данным или конфиденциальности напишите нам — мы поможем разобраться.',
  },
  en: {
    product: 'CONTENT ANALYTICS PLATFORM',
    title: 'Data that moves content forward.',
    lead: 'Statzavod helps teams and creators connect TikTok accounts with the owner’s authorization and view profile, account statistics, and public video analytics in one workspace.',
    signIn: 'Sign in', requestAccess: 'Request access', features: 'Features', security: 'Security', support: 'Support',
    featureTitle: 'One clear view of content performance',
    featureBody: 'Connect authorized TikTok accounts, bring together profile and public-video data, compare creators and campaigns, and identify winning formats.',
    securityTitle: 'A dependable foundation for your data',
    securityBody: 'Connections use TikTok’s secure authorization flow. We request only the data needed for analytics, and access can be revoked.',
    supportTitle: 'Support and contacts', supportBody: 'For questions about the service, data access, or privacy, contact us and we will help.',
  },
} as const

function Footer({ lang }: { lang: Language }) {
  const prefix = lang === 'en' ? '/en' : ''
  const labels = lang === 'ru'
    ? { terms: 'Условия', privacy: 'Конфиденциальность', security: 'Безопасность', cookies: 'Cookies', consent: 'Согласие', deletion: 'Удаление данных' }
    : { terms: 'Terms', privacy: 'Privacy', security: 'Security', cookies: 'Cookies', consent: 'Consent', deletion: 'Data deletion' }
  return <footer className={styles.footer}><div><span>© {new Date().getFullYear()} Statzavod</span><a className={styles.contact} href="mailto:asmtv1@yandex.ru">asmtv1@yandex.ru</a></div><nav><Link to={`${prefix}/terms`}>{labels.terms}</Link><Link to={`${prefix}/privacy`}>{labels.privacy}</Link><Link to={`${prefix}/security-policy`}>{labels.security}</Link><Link to={`${prefix}/cookies`}>{labels.cookies}</Link><Link to={`${prefix}/personal-data-consent`}>{labels.consent}</Link><Link to={`${prefix}/data-deletion`}>{labels.deletion}</Link></nav></footer>
}

function SupportVisual() {
  return <section className={`${visualStyles.visual} ${visualStyles.support}`} aria-label="Партнёрство и поддержка"><div className={visualStyles.header}><span>РАСТЁМ ВМЕСТЕ</span><i /></div><div className={visualStyles.path}><span /><span /><span /><span /><b /></div><div className={visualStyles.legend}><span>Запуск</span><span>Процессы</span><strong>Масштабирование</strong></div></section>
}

function DetailShowcase({ page, eyebrow, title, body, lang }: { page: 'features' | 'security'; eyebrow: string; title: string; body: string; lang: Language }) {
  const isSecurity = page === 'security'
  return (
    <section className={`${visualStyles.featureShowcase} ${isSecurity ? visualStyles.securityShowcase : ''}`} aria-labelledby={`${page}-title`}>
      <img
        className={`${visualStyles.featureArtwork} ${isSecurity ? visualStyles.securityArtwork : ''}`}
        src={isSecurity ? '/statzavod-security-flow.png' : '/statzavod-data-flow.png'}
        alt={isSecurity
          ? lang === 'ru' ? 'Защитный контур Statzavod принимает и безопасно передаёт потоки данных' : 'The Statzavod security perimeter receives and safely transmits data streams'
          : lang === 'ru' ? 'Потоки данных из социальных платформ объединяются в аналитическую систему Statzavod' : 'Data streams from social platforms converge into the Statzavod analytics system'}
      />
      <div className={visualStyles.featureCopy}>
        <div className={visualStyles.featureHeading}>
          <p>{eyebrow}</p>
          <h1 id={`${page}-title`}>{title}</h1>
        </div>
        <div className={visualStyles.featureNarrative}>
          <i aria-hidden="true" />
          <p>{body}</p>
        </div>
      </div>
    </section>
  )
}

export function PublicPage({ page = 'home', lang = 'ru' }: { page?: 'home' | 'features' | 'security' | 'support'; lang?: Language }) {
  const t = copy[lang]
  const prefix = lang === 'en' ? '/en' : ''
  const pageCopy = page === 'features' ? { eyebrow: t.features, title: t.featureTitle, body: t.featureBody } : page === 'security' ? { eyebrow: t.security, title: t.securityTitle, body: t.securityBody } : page === 'support' ? { eyebrow: t.support, title: t.supportTitle, body: t.supportBody } : { eyebrow: t.product, title: t.title, body: t.lead }
  const hero = <section className={`${styles.hero} ${page !== 'home' ? visualStyles.compactHero : ''}`}><p>{pageCopy.eyebrow}</p><h1>{pageCopy.title}</h1><p className={styles.lead}>{pageCopy.body}</p>{page === 'home' && <div className={styles.heroActions}><Link className={styles.cta} to="/login">{t.signIn}</Link><Link className={styles.secondaryCta} to={`${prefix}/request-access`}>{t.requestAccess}</Link></div>}</section>
  const hasShowcase = page === 'features' || page === 'security'
  return <main className={`${styles.page} ${page !== 'home' ? visualStyles.detailPage : ''} ${hasShowcase ? visualStyles.featurePage : ''}`}><header className={styles.header}><Link to={prefix || '/'} className={styles.brand}>STATZAVOD</Link><nav><Link to={`${prefix}/features`}>{t.features}</Link><Link to={`${prefix}/security`}>{t.security}</Link><Link to={`${prefix}/support`}>{t.support}</Link><Link to={`${prefix}/request-access`} className={styles.register}>{t.requestAccess}</Link><Link to="/login" className={styles.login}>{t.signIn}</Link><Link to={lang === 'ru' ? '/en' : '/'} className={styles.language}>{lang === 'ru' ? 'EN' : 'RU'}</Link></nav></header>{page === 'home' ? <div className={launchStyles.homeScene}>{hero}<div className={launchStyles.heroArtwork}><img src="/statzavod-launch.png" alt="Ракета Statzavod взлетает среди потоков данных" /></div></div> : hasShowcase ? <DetailShowcase page={page} eyebrow={pageCopy.eyebrow} title={pageCopy.title} body={pageCopy.body} lang={lang} /> : hero}{page === 'support' && <><SupportVisual/><a className={styles.email} href="mailto:asmtv1@yandex.ru">asmtv1@yandex.ru</a></>} {page === 'home' && <section className={styles.cards}><article><h2>{t.features}</h2><p>{t.featureBody}</p></article><article><h2>{t.security}</h2><p>{t.securityBody}</p></article><article><h2>{t.support}</h2><p>{t.supportBody}</p><Link className={styles.contactLink} to={`${prefix}/support`}>{lang === 'ru' ? 'Связаться с нами' : 'Contact us'}</Link></article></section>}<Footer lang={lang}/></main>
}
