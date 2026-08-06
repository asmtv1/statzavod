import { createContext, useContext, useEffect, useMemo, useState, type ReactNode } from 'react'

export type Locale = 'ru' | 'en'

type I18n = { locale: Locale; setLocale: (locale: Locale) => void; toggleLocale: () => void }

const I18nContext = createContext<I18n | null>(null)
const storageKey = 'statzavod.locale.v1'

// These translations cover text that is currently rendered by the Russian-first
// application. New interface strings should use the locale-aware components below.
const en: Record<string, string> = {
  'Загрузка…': 'Loading…', 'Загружаем статистику…': 'Loading statistics…', 'Загружаем список…': 'Loading list…',
  'Проверяем сессию…': 'Checking session…', 'Проверяем подключения…': 'Checking connections…',
  'ОБЗОР': 'OVERVIEW', 'Статистика креаторов': 'Creator analytics', 'Показатели по подключённым платформам и публикациям.': 'Metrics for connected platforms and publications.',
  'Данные частичные': 'Partial data', 'Динамика просмотров': 'Views trend', 'По снимкам': 'By snapshots',
  'Данные временно недоступны.': 'Data is temporarily unavailable.', 'Креаторы': 'Creators', 'Активен': 'Active',
  'Создайте первого креатора, чтобы подключить аккаунты.': 'Create your first creator to connect accounts.',
  'Синхронизация': 'Synchronization', 'В норме': 'Healthy', 'Проверка': 'Checking', 'Задач ожидает:': 'Pending tasks:',
  'Сбор запустится автоматически после подключения платформы.': 'Collection starts automatically after a platform is connected.',
  'Линейный график появится, когда будут собраны минимум две точки данных.': 'A line chart will appear after at least two data points are collected.',
  'Вход в систему': 'Sign in', 'Email': 'Email', 'Пароль': 'Password', 'Войти': 'Sign in',
	'Обзор': 'Overview', 'Компании': 'Companies', 'Креативы': 'Creative assets', 'Выйти': 'Sign out', 'В РАБОТЕ': 'IN PROGRESS',
  'Регистрация': 'Registration', 'Создайте рабочий аккаунт для команды.': 'Create a workspace account for your team.',
  'Рабочий email': 'Work email', 'Повторите пароль': 'Confirm password', 'Зарегистрироваться': 'Register',
  'Сервис находится в стадии beta-версии. Регистрация сейчас доступна только по приглашению.': 'The service is in beta. Registration is currently invitation-only.',
  'Отчёты и сравнения': 'Reports and comparisons', 'Аналитика': 'Analytics',
  'Соберите статистику по одному или нескольким креаторам за нужный период.': 'Collect metrics for one or more creators for the selected period.',
  'С': 'From', 'По': 'To', 'Выбрано:': 'Selected:', 'Компания': 'Company', 'Все компании': 'All companies', 'Без компании': 'No company',
  'Снять выбор': 'Clear selection', 'Выбрать всех': 'Select all', 'В отпуске': 'On leave', 'Уволен': 'Dismissed',
  'Укажите период': 'Select a period', 'Собираем…': 'Collecting…', 'Собрать статистику': 'Collect metrics',
  'Отчёт ещё не собран': 'Report has not been generated', 'Выберите период и сотрудников выше. Здесь появятся графики, ключевые показатели и таблица публикаций.': 'Select a period and creators above. Charts, key metrics, and a publications table will appear here.',
  'РЕЗУЛЬТАТ': 'RESULT', 'Скачать Excel': 'Download Excel', 'Итоги по креаторам': 'Creator summary', 'Суммарный результат каждого креатора по всем платформам.': 'Total results for each creator across all platforms.',
  'Креатор': 'Creator', 'просмотров': 'views', 'реакций': 'reactions', 'комментариев': 'comments', 'репостов': 'shares', 'публикаций': 'publications',
  'Все выбранные креаторы': 'All selected creators', 'все платформы': 'all platforms', 'Все платформы': 'All platforms',
  'Динамика по дням': 'Daily trend', 'Результат публикаций по дате выхода.': 'Publication results by publish date.', 'Платформы': 'Platforms', 'Вклад каждой площадки в результат.': 'Each platform’s contribution to the result.',
  'За период публикаций нет': 'There are no publications for this period', 'Публикации': 'Publications', 'Все ролики выбранных креаторов за период.': 'All videos by selected creators for the period.',
  'Публикация': 'Publication', 'Просмотры': 'Views', 'Реакции': 'Reactions', 'Без названия': 'Untitled', 'За выбранный период публикаций нет.': 'There are no publications for the selected period.',
  'БАЗА КРЕАТОРОВ': 'CREATOR DATABASE', 'Управляйте карточками креаторов и подключёнными аккаунтами.': 'Manage creator profiles and connected accounts.',
  'Добавить креатора': 'Add creator', 'Рабочие': 'Active', 'Архив': 'Archive', 'Раздел креаторов': 'Creator section', 'Сортировка работ': 'Work sort',
  'Без сортировки': 'No sorting', 'Сверху: всё ок': 'First: all good', 'Сверху: нужны работы': 'First: needs attention', 'Сверху: в работе': 'First: in progress', 'Найдено:': 'Found:',
  'Архив пуст': 'Archive is empty', 'Сюда попадут карточки, убранные из рабочих списков. Их можно будет восстановить в любой момент.': 'Profiles removed from active lists appear here. They can be restored at any time.',
  'Креаторов пока нет': 'No creators yet', 'Создайте первую карточку, затем добавьте контакты и подключите аккаунты платформ.': 'Create the first profile, then add contacts and connect platform accounts.',
  'Добавить первого креатора': 'Add first creator', 'В этой компании креаторов пока нет.': 'There are no creators in this company yet.',
  'Telegram': 'Telegram', 'Статус': 'Status', 'Работы': 'Work', 'В архиве с': 'Archived since', 'Не указан': 'Not specified',
  'НОВАЯ КАРТОЧКА': 'NEW PROFILE', 'Закрыть': 'Close', 'Имя': 'First name', 'Фамилия': 'Last name', 'Отчество': 'Middle name', 'Отображаемое имя': 'Display name',
  'Если не указать — сформируется автоматически': 'Generated automatically when left empty', 'Внутренний комментарий': 'Internal note', 'Отмена': 'Cancel', 'Создаём…': 'Creating…', 'Создать креатора': 'Create creator',
  'Принять приглашение': 'Accept invitation', 'Новый пароль': 'New password', 'Активировать доступ': 'Activate access', 'Ко входу': 'Back to sign in',
  'Система': 'System', 'АДМИНИСТРИРОВАНИЕ': 'ADMINISTRATION', 'Состояние синхронизации и очередей.': 'Synchronization and queue status.', 'Статус scheduler': 'Scheduler status',
  'КОНТЕНТ': 'CONTENT', 'Все обнаруженные видео и их последние показатели.': 'All discovered videos and their latest metrics.', 'Публикаций:': 'Publications:',
  'У выбранной компании публикаций пока нет.': 'The selected company has no publications yet.', 'Публикации появятся после подключения аккаунта и первого сбора данных.': 'Publications will appear after an account is connected and data is collected for the first time.',
  'СРАВНЕНИЕ': 'COMPARISON', 'Объединяйте один ролик, опубликованный на нескольких платформах.': 'Group one video published across multiple platforms.',
  'Креативов пока нет': 'No creative assets yet', 'После появления публикаций система покажет предложения совпадений. Объединение всегда подтверждает администратор.': 'When publications are available, the system will suggest matches. Grouping is always confirmed by an administrator.',
  'Работает': 'Healthy', 'Внимание': 'Warning', 'Ошибка': 'Error', 'Ожидает': 'Pending', 'Ещё не запускалась': 'Not run yet',
  'КОНТРОЛЬ ДАННЫХ': 'DATA CONTROL', 'Состояние всех подключённых аккаунтов — без перехода в карточки креаторов.': 'The status of every connected account, without opening creator profiles.',
  'Всего аккаунтов': 'Total accounts', 'в мониторинге': 'monitored', 'Работают': 'Healthy', 'без ошибок': 'without errors', 'Требуют внимания': 'Need attention', 'критичных проблем нет': 'no critical issues',
  'Переподключить проблемные': 'Reconnect problematic accounts', 'Аккаунты креаторов': 'Creator accounts', 'Ошибки и истекающие токены показываются первыми.': 'Errors and expiring tokens are shown first.',
  'Фильтр подключений': 'Connection filter', 'Все': 'All', 'Проблемы': 'Issues', 'Креатор и аккаунт': 'Creator and account', 'Состояние': 'Status', 'Последняя синхронизация': 'Last synchronization',
  'Без срока действия': 'No expiry date', 'Переходим…': 'Opening…', 'Переподключить': 'Reconnect', 'Открыть': 'Open', 'По этому фильтру ничего нет': 'There is nothing for this filter', 'Подключений пока нет': 'No connections yet',
  'Выберите другой статус подключения.': 'Choose another connection status.', 'Подключите платформу в карточке креатора — аккаунт сразу появится в мониторинге.': 'Connect a platform in a creator profile and the account will immediately appear in monitoring.',
  'Перейти к креаторам': 'Go to creators', 'OAuth настроен': 'OAuth configured', 'OAuth не настроен': 'OAuth not configured', 'МАССОВОЕ ДЕЙСТВИЕ': 'BULK ACTION',
  'Переподключение аккаунтов': 'Reconnect accounts', 'OAuth требует подтверждения каждой платформы. Пройдите проблемные аккаунты по очереди — завершённые подключения исчезнут из списка.': 'OAuth requires confirmation for each platform. Reconnect problematic accounts one at a time; completed connections will disappear from the list.',
  'Открываем OAuth…': 'Opening OAuth…', 'Начать с первого': 'Start with the first',
  'СТРУКТУРА КОМАНДЫ': 'TEAM STRUCTURE', 'Группируйте креаторов по брендам и направлениям, для которых они создают контент.': 'Group creators by brands and areas for which they create content.',
  'Новая компания': 'New company', 'Например, Поле чудес': 'For example, Wonder Field', 'Создать компанию': 'Create company',
  'Общий аккаунт VK ID подключён. Сбор сообществ поставлен в очередь.': 'The shared VK ID account is connected. Community collection has been queued.', 'Подключение VK не завершено:': 'VK connection was not completed:',
  'Загружаем компании…': 'Loading companies…', 'Креаторы ещё не назначены': 'No creators have been assigned yet', 'VK ID подключён': 'VK ID connected', 'Подключить VK ID': 'Connect VK ID', 'Настроить VK': 'Set up VK',
  'Архивировать': 'Archive', 'Компаний пока нет. Создайте первую выше.': 'No companies yet. Create the first one above.', 'Компания исчезнет из списка, а её креаторы перейдут в категорию «Без компании». Карточки и статистика сохранятся.': 'The company will disappear from the list and its creators will move to “No company”. Profiles and statistics will be kept.',
  'ОБЩИЙ ДОСТУП КОМПАНИИ': 'SHARED COMPANY ACCESS', 'Этот аккаунт управляет сообществами креаторов. Подключите его один раз через VK ID — статистика будет собираться из сообществ, указанных в карточках креаторов.': 'This account manages creator communities. Connect it once through VK ID and statistics will be collected from the communities listed in creator profiles.',
  'VK ID ещё не подключён': 'VK ID not connected yet', 'Сообществ назначено:': 'Assigned communities:', 'Ошибка синхронизации:': 'Synchronization error:', 'Последняя успешная синхронизация:': 'Last successful synchronization:', 'Первая синхронизация ещё не завершена.': 'The first synchronization has not completed yet.',
  'Ставим в очередь…': 'Queuing…', 'Синхронизация запрошена': 'Synchronization requested', 'Синхронизировать сейчас': 'Synchronize now', 'Данные общего доступа для команды': 'Shared access details for the team', 'Логин и пароль': 'Login and password', 'Только номер телефона': 'Phone number only',
  'Логин': 'Login', 'Телефон': 'Phone', 'Сохранён — введите только для замены': 'Saved — enter only to replace', 'Введите пароль': 'Enter password', 'Показать': 'Show', 'Необязательно': 'Optional', 'Номер телефона': 'Phone number', 'Сохраняем…': 'Saving…', 'Сохранить VK': 'Save VK',
  'Карточка креатора': 'Creator profile', 'КАРТОЧКА КРЕАТОРА': 'CREATOR PROFILE', 'Контакты': 'Contacts', 'Дополнительные способы связи.': 'Additional ways to get in touch.', 'Добавить': 'Add',
  'Канал и YouTube Analytics': 'Channel and YouTube Analytics', 'Профиль, Reels и Insights': 'Profile, Reels, and Insights', 'Профиль и опубликованные видео': 'Profile and published videos',
  'Способ доступа': 'Access method', 'Почта аккаунта YouTube': 'YouTube account email', 'Активная ссылка на канал': 'Active channel link', 'Почта креатора для доступа': 'Creator access email', 'Почта': 'Email',
  'Канал и публикации': 'Channel and publications', 'Аналитика канала': 'Channel analytics', 'Профиль и публикации': 'Profile and publications', 'Статистика аккаунта': 'Account statistics', 'Расширенные данные профиля': 'Extended profile data', 'Опубликованные видео': 'Published videos', 'Видео и клипы': 'Videos and clips', 'Долгосрочный доступ': 'Long-term access',
  'Подключено': 'Connected', 'Синхронизация приостановлена': 'Synchronization paused', 'Нужно переподключить': 'Reconnect required', 'Отключено': 'Disconnected',
  'История профиля': 'Profile history', 'История работ': 'Work history', 'История доступов и аккаунтов': 'Access and account history', 'Описание задачи': 'Task description', 'Нужны работы': 'Needs attention',
  'Общий аккаунт фирмы': 'Shared company account', 'Изменить': 'Edit', 'Загружаем корпоративный доступ…': 'Loading shared company access…', 'Не удалось загрузить VK-доступ.': 'Could not load VK access.', 'Аккаунт фирмы': 'Company account', 'Не выбран': 'Not selected',
  'Перенесите в общий аккаунт фирмы': 'Move to the shared company account', 'Сохранено — введите для замены': 'Saved — enter to replace', 'Не заполнено': 'Not filled in', 'Скрыть': 'Hide',
  'Официальные OAuth-подключения для автоматического сбора статистики.': 'Official OAuth connections for automatic statistics collection.', 'Нужны OAuth-реквизиты': 'OAuth credentials are required', 'Нет подключений': 'No connections', 'Через Facebook · коллаборации': 'Via Facebook · collaborations',
  'Аккаунтов:': 'Accounts:', 'синхронизация': 'synchronized', 'Удалить подключение': 'Delete connection', 'Карточка не показывается в рабочих списках и на дашборде': 'This profile is hidden from active lists and the dashboard',
  'Восстанавливаем…': 'Restoring…', 'Восстановить': 'Restore', 'В архив': 'Archive', 'Открыть Telegram': 'Open Telegram', 'Перенести креатора в архив?': 'Move creator to archive?',
  'Карточка исчезнет из рабочих списков и дашборда. Профиль, доступы, статистика и история изменений сохранятся — креатора можно восстановить в любой момент.': 'The profile will disappear from active lists and the dashboard. The profile, access, statistics, and change history will be kept and can be restored at any time.',
  'Переносим…': 'Moving…', 'Перенести в архив': 'Move to archive',
  'Подключения платформ': 'Platform connections', 'Подключить': 'Connect', 'Аккаунты ещё не подключены.': 'No accounts are connected yet.',
  'Удалить подключение?': 'Delete connection?', 'Доступ платформы будет отозван. Аккаунт, публикации, метрики и задания синхронизации будут удалены без возможности восстановления.': 'Platform access will be revoked. The account, publications, metrics, and synchronization tasks will be permanently deleted.',
  'Удаляем…': 'Deleting…', 'Удалить всё': 'Delete all', 'подключён': 'connected', 'Доступ к данным добавлен': 'Data access added', 'удалён': 'deleted', 'Подключение и собранные данные удалены': 'The connection and collected data were deleted',
}

function translateText(value: string): string {
  const direct = en[value.trim()]
  if (direct) return value.replace(value.trim(), direct)
  return value
    .replace(/^Подключены: /, 'Connected: ')
    .replace(/^В архиве с /, 'Archived since ')
    .replace(/^Найдено: /, 'Found: ')
    .replace(/^Выбрано: /, 'Selected: ')
    .replace(/^Задач ожидает: /, 'Pending tasks: ')
    .replace(/^Токен до /, 'Token until ')
}

function localizeNode(node: Node) {
  if (node.nodeType === Node.TEXT_NODE) {
    const next = translateText(node.textContent ?? '')
    if (next !== node.textContent) node.textContent = next
    return
  }
  if (!(node instanceof HTMLElement)) return
  for (const attribute of ['placeholder', 'aria-label', 'title']) {
    const value = node.getAttribute(attribute)
    if (value) node.setAttribute(attribute, translateText(value))
  }
  node.childNodes.forEach(localizeNode)
}

function DomLocalizer({ locale }: { locale: Locale }) {
  useEffect(() => {
    document.documentElement.lang = locale
    if (locale === 'ru') return
    localizeNode(document.body)
    const observer = new MutationObserver(records => records.forEach(record => record.addedNodes.forEach(localizeNode)))
    observer.observe(document.body, { childList: true, subtree: true })
    return () => observer.disconnect()
  }, [locale])
  return null
}

export function I18nProvider({ children }: { children: ReactNode }) {
  const [locale, setLocaleState] = useState<Locale>(() => {
    const saved = localStorage.getItem(storageKey)
    return saved === 'en' || saved === 'ru' ? saved : location.pathname.startsWith('/en') ? 'en' : 'ru'
  })
  const setLocale = (next: Locale) => {
    if (next === locale) return
    localStorage.setItem(storageKey, next)
    // Existing pages are Russian-first React components. Reloading from the
    // source markup makes changing back from English lossless as well.
    window.location.reload()
  }
  const value = useMemo(() => ({ locale, setLocale, toggleLocale: () => setLocale(locale === 'ru' ? 'en' : 'ru') }), [locale])
  return <I18nContext.Provider value={value}><DomLocalizer locale={locale} />{children}</I18nContext.Provider>
}

export function useI18n() {
  const value = useContext(I18nContext)
  if (!value) throw new Error('useI18n must be used inside I18nProvider')
  return value
}

export function LanguageToggle() {
  const { locale, setLocale } = useI18n()
  return <div role="group" aria-label="Language"><button type="button" aria-pressed={locale === 'ru'} onClick={() => setLocale('ru')}>RU</button><button type="button" aria-pressed={locale === 'en'} onClick={() => setLocale('en')}>EN</button></div>
}
