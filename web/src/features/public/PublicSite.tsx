import { Link } from 'react-router-dom'
import styles from './PublicSite.module.scss'
import visualStyles from './BenefitVisual.module.scss'
import launchStyles from './LaunchVisual.module.scss'

type Language = 'ru' | 'en'
const copy = {
  ru: {
    product: 'ПЛАТФОРМА ДЛЯ АНАЛИТИКИ КОНТЕНТА',
    title: 'Данные, которые двигают контент вперёд.',
    lead: 'Statzavod объединяет статистику по каналам, креаторам и кампаниям в единую систему. Команды и агентства быстрее видят результат, находят точки роста и уверенно масштабируют контент на новых платформах.',
    signIn: 'Войти в кабинет', signUp: 'Зарегистрироваться', features: 'Возможности', security: 'Безопасность', support: 'Поддержка',
    featureTitle: 'Единая картина эффективности контента',
    featureBody: 'Собирайте данные из ключевых платформ, сравнивайте креаторов и кампании, находите работающие форматы и принимайте решения на основе понятных показателей.',
    securityTitle: 'Надёжная основа для ваших данных',
    securityBody: 'Защищённые интеграции, разграничение доступов и прозрачное управление данными помогают командам работать уверенно на любом масштабе.',
    supportTitle: 'Партнёрство на каждом этапе', supportBody: 'Помогаем запускать аналитику, выстраивать процессы и получать больше пользы от данных — от первой команды до большой сети креаторов.',
  },
  en: {
    product: 'CONTENT ANALYTICS PLATFORM',
    title: 'Data that moves content forward.',
    lead: 'Statzavod brings channel, creator, and campaign data into one system. Teams and agencies can see performance sooner, find growth opportunities, and scale content confidently across new platforms.',
    signIn: 'Sign in', signUp: 'Register', features: 'Features', security: 'Security', support: 'Support',
    featureTitle: 'One clear view of content performance',
    featureBody: 'Bring together data from key platforms, compare creators and campaigns, identify winning formats, and make decisions with clear performance signals.',
    securityTitle: 'A dependable foundation for your data',
    securityBody: 'Secure integrations, granular access controls, and transparent data management help teams operate confidently at any scale.',
    supportTitle: 'A partner at every stage', supportBody: 'We help teams launch analytics, shape reliable workflows, and get more value from data — from the first creator to a global network.',
  },
} as const

function Footer({ lang }: { lang: Language }) {
  const prefix = lang === 'en' ? '/en' : ''
  const labels = lang === 'ru'
    ? { terms: 'Условия', privacy: 'Конфиденциальность', security: 'Безопасность', cookies: 'Cookies', consent: 'Согласие', deletion: 'Удаление данных' }
    : { terms: 'Terms', privacy: 'Privacy', security: 'Security', cookies: 'Cookies', consent: 'Consent', deletion: 'Data deletion' }
  return <footer className={styles.footer}><span>© {new Date().getFullYear()} Statzavod</span><nav><Link to={`${prefix}/terms`}>{labels.terms}</Link><Link to={`${prefix}/privacy`}>{labels.privacy}</Link><Link to={`${prefix}/security-policy`}>{labels.security}</Link><Link to={`${prefix}/cookies`}>{labels.cookies}</Link><Link to={`${prefix}/personal-data-consent`}>{labels.consent}</Link><Link to={`${prefix}/data-deletion`}>{labels.deletion}</Link></nav></footer>
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
  const hero = <section className={`${styles.hero} ${page !== 'home' ? visualStyles.compactHero : ''}`}><p>{pageCopy.eyebrow}</p><h1>{pageCopy.title}</h1><p className={styles.lead}>{pageCopy.body}</p>{page === 'home' && <div className={styles.heroActions}><Link className={styles.cta} to="/login">{t.signIn}</Link><Link className={styles.secondaryCta} to="/register">{t.signUp}</Link></div>}</section>
  const hasShowcase = page === 'features' || page === 'security'
  return <main className={`${styles.page} ${page !== 'home' ? visualStyles.detailPage : ''} ${hasShowcase ? visualStyles.featurePage : ''}`}><header className={styles.header}><Link to={prefix || '/'} className={styles.brand}>STATZAVOD</Link><nav><Link to={`${prefix}/features`}>{t.features}</Link><Link to={`${prefix}/security`}>{t.security}</Link><Link to="/register" className={styles.register}>{t.signUp}</Link><Link to="/login" className={styles.login}>{t.signIn}</Link><Link to={lang === 'ru' ? '/en' : '/'} className={styles.language}>{lang === 'ru' ? 'EN' : 'RU'}</Link></nav></header>{page === 'home' ? <div className={launchStyles.homeScene}>{hero}<div className={launchStyles.heroArtwork}><img src="/statzavod-launch.png" alt="Ракета Statzavod взлетает среди потоков данных" /></div></div> : hasShowcase ? <DetailShowcase page={page} eyebrow={pageCopy.eyebrow} title={pageCopy.title} body={pageCopy.body} lang={lang} /> : hero}{page === 'support' && <SupportVisual/>} {page === 'home' && <section className={styles.cards}><article><h2>{t.features}</h2><p>{t.featureBody}</p></article><article><h2>{t.security}</h2><p>{t.securityBody}</p></article><article><h2>{t.support}</h2><p>{t.supportBody}</p></article></section>}{page === 'support' && <a className={styles.email} href="mailto:asmtv1@yandex.ru">asmtv1@yandex.ru</a>}<Footer lang={lang}/></main>
}
