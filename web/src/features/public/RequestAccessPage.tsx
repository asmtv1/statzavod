import { FormEvent, useState } from 'react'
import { Link } from 'react-router-dom'
import publicStyles from './PublicSite.module.scss'
import styles from './RequestAccessPage.module.scss'

type FormValues = {
  name: string
  email: string
  company: string
  role: string
  message: string
}

const initialValues: FormValues = { name: '', email: '', company: '', role: '', message: '' }

export function RequestAccessPage({ lang = 'ru' }: { lang?: 'ru' | 'en' }) {
  const [values, setValues] = useState<FormValues>(initialValues)
  const isRussian = lang === 'ru'
  const prefix = isRussian ? '' : '/en'
  const text = isRussian
    ? {
        title: 'Запросить доступ',
        lead: 'Оставьте рабочие контакты — мы уточним задачу и вернёмся с доступом для вашей команды.',
        formTitle: 'Расскажите о команде',
        formHint: 'Это займёт меньше минуты. Пароль и данные аккаунтов не понадобятся.',
        name: 'Ваше имя', email: 'Рабочий email', company: 'Компания или команда', role: 'Ваша роль', message: 'Что хотите анализировать?',
        namePlaceholder: 'Иван Петров', emailPlaceholder: 'name@company.ru', companyPlaceholder: 'Название компании', rolePlaceholder: 'Например, маркетинг или агентство', messagePlaceholder: 'Креаторов, кампании, контент…',
        submit: 'Отправить заявку',
        consent: 'Нажимая «Отправить заявку», вы соглашаетесь с обработкой данных для ответа на запрос.',
        processTitle: 'Что будет дальше',
        steps: ['Получим вашу заявку', 'Уточним сценарий работы', 'Откроем доступ команде'],
        support: 'Есть вопрос?', supportLink: 'Напишите нам',
        features: 'Возможности', security: 'Безопасность', supportNav: 'Поддержка', signIn: 'Войти', request: 'Запросить доступ',
      }
    : {
        title: 'Request access',
        lead: 'Leave your work contact details. We will clarify your needs and get your team set up.',
        formTitle: 'Tell us about your team',
        formHint: 'It takes less than a minute. No passwords or account credentials are needed.',
        name: 'Your name', email: 'Work email', company: 'Company or team', role: 'Your role', message: 'What would you like to analyse?',
        namePlaceholder: 'Alex Morgan', emailPlaceholder: 'name@company.com', companyPlaceholder: 'Company name', rolePlaceholder: 'For example, marketing or agency', messagePlaceholder: 'Creators, campaigns, content…',
        submit: 'Send request',
        consent: 'By sending the request, you agree to the processing of your details to respond to it.',
        processTitle: 'What happens next',
        steps: ['We receive your request', 'We clarify your workflow', 'We enable your team'],
        support: 'Have a question?', supportLink: 'Write to us',
        features: 'Features', security: 'Security', supportNav: 'Support', signIn: 'Sign in', request: 'Request access',
      }

  const legal = isRussian
    ? { terms: 'Условия', privacy: 'Конфиденциальность', security: 'Безопасность', cookies: 'Cookies', consent: 'Согласие', deletion: 'Удаление данных' }
    : { terms: 'Terms', privacy: 'Privacy', security: 'Security', cookies: 'Cookies', consent: 'Consent', deletion: 'Data deletion' }

  function update(field: keyof FormValues, value: string) {
    setValues(current => ({ ...current, [field]: value }))
  }

  function submit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault()
    const subject = isRussian ? 'Запрос доступа Statzavod' : 'Statzavod access request'
    const body = [
      `${text.name}: ${values.name}`,
      `${text.email}: ${values.email}`,
      `${text.company}: ${values.company}`,
      `${text.role}: ${values.role}`,
      '',
      `${text.message}:`,
      values.message,
    ].join('\n')
    window.location.href = `mailto:asmtv1@yandex.ru?subject=${encodeURIComponent(subject)}&body=${encodeURIComponent(body)}`
  }

  return <main className={styles.page}>
    <header className={publicStyles.header}>
      <Link to={prefix || '/'} className={publicStyles.brand}>STATZAVOD</Link>
      <nav>
        <Link to={`${prefix}/features`}>{text.features}</Link>
        <Link to={`${prefix}/security`}>{text.security}</Link>
        <Link to={`${prefix}/support`}>{text.supportNav}</Link>
        <Link to="/login" className={publicStyles.login}>{text.signIn}</Link>
        <Link to={isRussian ? '/en/request-access' : '/request-access'} className={publicStyles.language}>{isRussian ? 'EN' : 'RU'}</Link>
      </nav>
    </header>

    <section className={styles.content} aria-labelledby="request-access-title">
      <div className={styles.intro}>
        <p className={styles.overline}>STATZAVOD</p>
        <h1 id="request-access-title">{text.title}</h1>
        <p>{text.lead}</p>
        <section className={styles.process} aria-labelledby="process-title">
          <h2 id="process-title">{text.processTitle}</h2>
          <ol>{text.steps.map((step, index) => <li key={step}><span>{String(index + 1).padStart(2, '0')}</span>{step}</li>)}</ol>
        </section>
      </div>

      <form className={styles.form} onSubmit={submit}>
        <div className={styles.formHeading}><h2>{text.formTitle}</h2><p>{text.formHint}</p></div>
        <div className={styles.fields}>
          <label>{text.name}<input value={values.name} onChange={event => update('name', event.target.value)} placeholder={text.namePlaceholder} autoComplete="name" required /></label>
          <label>{text.email}<input type="email" value={values.email} onChange={event => update('email', event.target.value)} placeholder={text.emailPlaceholder} autoComplete="email" required /></label>
          <label>{text.company}<input value={values.company} onChange={event => update('company', event.target.value)} placeholder={text.companyPlaceholder} autoComplete="organization" required /></label>
          <label>{text.role}<input value={values.role} onChange={event => update('role', event.target.value)} placeholder={text.rolePlaceholder} /></label>
          <label className={styles.message}>{text.message}<textarea value={values.message} onChange={event => update('message', event.target.value)} placeholder={text.messagePlaceholder} rows={4} /></label>
        </div>
        <button type="submit">{text.submit}<span aria-hidden="true">↗</span></button>
        <p className={styles.consent}>{text.consent} <Link to={`${prefix}/personal-data-consent`}>{isRussian ? 'Подробнее' : 'Learn more'}</Link></p>
      </form>
    </section>

    <p className={styles.support}>{text.support} <Link to="mailto:asmtv1@yandex.ru">{text.supportLink}</Link></p>
    <footer className={publicStyles.footer}><div><span>© {new Date().getFullYear()} Statzavod</span><a className={publicStyles.contact} href="mailto:asmtv1@yandex.ru">asmtv1@yandex.ru</a></div><nav><Link to={`${prefix}/terms`}>{legal.terms}</Link><Link to={`${prefix}/privacy`}>{legal.privacy}</Link><Link to={`${prefix}/security-policy`}>{legal.security}</Link><Link to={`${prefix}/cookies`}>{legal.cookies}</Link><Link to={`${prefix}/personal-data-consent`}>{legal.consent}</Link><Link to={`${prefix}/data-deletion`}>{legal.deletion}</Link></nav></footer>
  </main>
}
