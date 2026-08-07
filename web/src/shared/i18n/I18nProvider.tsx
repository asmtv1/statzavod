import { createContext, useCallback, useContext, useLayoutEffect, useMemo, useState, type ReactNode } from 'react'
import { useLocation } from 'react-router-dom'
import { publicPathLocale, readStoredLocale, storeLocale, type Locale } from './locale'

export type { Locale } from './locale'

type TranslationValues = Record<string, string | number>
type I18n = {
  locale: Locale
  setLocale: (locale: Locale) => void
  toggleLocale: () => void
  t: (key: string, values?: TranslationValues) => string
}

const I18nContext = createContext<I18n | null>(null)
const cyrillicPattern = /[\u0400-\u052f\u2de0-\u2dff\ua640-\ua69f]/

// Russian source strings are stable translation keys during the incremental i18n
// migration. UI components must render them through t() instead of editing the DOM.
const en: Record<string, string> = {
  'Загрузка…': 'Loading…', 'Загружаем статистику…': 'Loading statistics…', 'Загружаем список…': 'Loading list…',
  'Проверяем сессию…': 'Checking session…', 'Проверяем подключения…': 'Checking connections…',
  'ОБЗОР': 'OVERVIEW', 'Статистика креаторов': 'Creator analytics', 'Показатели по подключённым платформам и публикациям.': 'Metrics for connected platforms and publications.',
  'Данные частичные': 'Partial data', 'Динамика просмотров': 'Views trend', 'По снимкам': 'By snapshots',
  'Данные временно недоступны.': 'Data is temporarily unavailable.', 'Креаторы': 'Creators', 'Активен': 'Active',
  'Создайте первого креатора, чтобы подключить аккаунты.': 'Create your first creator to connect accounts.',
  'Синхронизация': 'Synchronization', 'В норме': 'Healthy', 'Проверка': 'Checking', 'Задач ожидает:': 'Pending tasks:',
  'Сбор запустится автоматически после подключения платформы.': 'Collection starts automatically after a platform is connected.',
  'Подключите платформенные аккаунты для сбора данных.': 'Connect platform accounts to start collecting data.',
  'Синхронизация работает': 'Synchronization is working', 'Аккаунт требует повторного подключения': 'The account must be reconnected',
  'Нет активного OAuth-подключения': 'There is no active OAuth connection', 'Авторизация недействительна': 'Authorization is invalid',
  'Токен истёк': 'The token has expired', 'Срок действия токена скоро закончится': 'The token will expire soon',
  'Ожидает первой синхронизации': 'Waiting for the first synchronization', 'Instagram через Facebook': 'Instagram via Facebook',
  'Линейный график появится, когда будут собраны минимум две точки данных.': 'A line chart will appear after at least two data points are collected.',
  'Вход в систему': 'Sign in', 'Email': 'Email', 'Пароль': 'Password', 'Войти': 'Sign in', 'Не удалось войти': 'Could not sign in',
	'СТАТЗАВОД': 'STATZAVOD', 'Обзор': 'Overview', 'Компании': 'Companies', 'Креативы': 'Creative assets', 'Выйти': 'Sign out', 'В РАБОТЕ': 'IN PROGRESS',
  'Регистрация': 'Registration', 'Создайте рабочий аккаунт для команды.': 'Create a workspace account for your team.',
  'Рабочий email': 'Work email', 'Повторите пароль': 'Confirm password', 'Не менее 12 символов': 'At least 12 characters', 'Зарегистрироваться': 'Register',
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
  'Ссылка-приглашение не содержит токен.': 'The invitation link does not contain a token.', 'Аккаунт {email} активирован. Теперь можно войти.': 'Account {email} has been activated. You can now sign in.', 'Не удалось принять приглашение': 'Could not accept the invitation',
  'Раздел готов к подключению': 'This section is ready to be connected', 'Базовый API и навигация уже подготовлены. Следующий шаг — подключить данные платформы и наполнить экран.': 'The core API and navigation are ready. Next, connect platform data and populate this screen.',
  'Система': 'System', 'АДМИНИСТРИРОВАНИЕ': 'ADMINISTRATION', 'Состояние синхронизации и очередей.': 'Synchronization and queue status.', 'Статус scheduler': 'Scheduler status',
  'КОНТЕНТ': 'CONTENT', 'Все обнаруженные видео и их последние показатели.': 'All discovered videos and their latest metrics.', 'Публикаций:': 'Publications:',
  'Все креаторы': 'All creators', 'Все площадки': 'All platforms', 'Площадка': 'Platform', 'Превью': 'Preview', 'Текст': 'Text', 'Лайки': 'Likes', 'Комментарии': 'Comments', 'Дата': 'Date', 'Выбрать все публикации': 'Select all publications', 'Выбрать публикацию': 'Select publication',
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
  'Отвязать VK ID': 'Disconnect VK ID', 'Отвязать VK ID?': 'Disconnect VK ID?', 'Доступ VK будет отозван, а новые синхронизации сообществ остановятся. Уже собранные публикации и метрики сохранятся.': 'VK access will be revoked and new community synchronizations will stop. Previously collected publications and metrics will be kept.', 'VK ID отвязан. Собранные данные сохранены.': 'VK ID was disconnected. Collected data was kept.',
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
  'Аккаунтов:': 'Accounts:', 'синхронизация': 'synchronized', 'Удалить подключение': 'Delete connection', 'Отвязать аккаунт': 'Disconnect account', 'Карточка не показывается в рабочих списках и на дашборде': 'This profile is hidden from active lists and the dashboard',
  'Восстанавливаем…': 'Restoring…', 'Восстановить': 'Restore', 'В архив': 'Archive', 'Открыть Telegram': 'Open Telegram', 'Перенести креатора в архив?': 'Move creator to archive?',
  'Карточка исчезнет из рабочих списков и дашборда. Профиль, доступы, статистика и история изменений сохранятся — креатора можно восстановить в любой момент.': 'The profile will disappear from active lists and the dashboard. The profile, access, statistics, and change history will be kept and can be restored at any time.',
  'Переносим…': 'Moving…', 'Перенести в архив': 'Move to archive',
  'Удалить креатора навсегда?': 'Delete creator permanently?', 'Это действие необратимо. Карточка креатора, история изменений и все связанные данные будут удалены навсегда. Все привязанные аккаунты будут отвязаны.': 'This action cannot be undone. The creator profile, change history, and all related data will be permanently deleted. All linked accounts will be unlinked.',
  'Не удалось удалить креатора:': 'Could not delete the creator:',
  'Подключения платформ': 'Platform connections', 'Подключить': 'Connect', 'Аккаунты ещё не подключены.': 'No accounts are connected yet.',
  'Добавьте аккаунты': 'Add accounts', 'Мы нашли профессиональные Instagram-аккаунты, связанные с доступными вам Facebook Pages.': 'We found professional Instagram accounts linked to the Facebook Pages available to you.',
  'Выберите аккаунты': 'Select accounts', 'из': 'of', 'Проверяем найденные аккаунты…': 'Checking discovered accounts…', 'Уже подключён — доступ будет обновлён': 'Already connected — access will be refreshed',
  'Подключён к другому креатору:': 'Connected to another creator:', 'Доступен для подключения': 'Available to connect', 'Выбрать аккаунт': 'Select account', 'Доступных аккаунтов не найдено.': 'No available accounts were found.',
  'Подключаем…': 'Connecting…', 'Добавить аккаунты': 'Add accounts', 'Не видите свой аккаунт?': 'Can’t see your account?',
  'Удалить подключение?': 'Delete connection?', 'Доступ платформы будет отозван. Аккаунт, публикации, метрики и задания синхронизации будут удалены без возможности восстановления.': 'Platform access will be revoked. The account, publications, metrics, and synchronization tasks will be permanently deleted.',
  'Отвязать аккаунт?': 'Disconnect account?', 'Доступ платформы будет отозван, а новые синхронизации остановятся. Уже собранные публикации и метрики сохранятся.': 'Platform access will be revoked and new synchronizations will stop. Previously collected publications and metrics will be kept.', 'Отвязываем…': 'Disconnecting…', 'Отвязать': 'Disconnect', 'отвязан': 'disconnected', 'Подключение отозвано; собранные данные сохранены': 'Access was revoked; collected data was kept', 'Не удалось отвязать аккаунт.': 'Could not disconnect the account.',
  'Удаляем…': 'Deleting…', 'Удалить всё': 'Delete all', 'подключён': 'connected', 'Доступ к данным добавлен': 'Data access added', 'удалён': 'deleted', 'Подключение и собранные данные удалены': 'The connection and collected data were deleted',
  'ОТЧЁТЫ И СРАВНЕНИЯ': 'REPORTS AND COMPARISONS', 'Платформа': 'Platform', 'Показатель': 'Metric', 'За период:': 'For the period:',
  'Сравнение показателей креаторов': 'Creator metric comparison', 'публикаций по дням': 'publications by day', 'по платформам': 'by platform', 'Нет данных по показателю': 'No data for this metric', 'публикац.': 'publications',
  'креатор': 'creator', 'креаторов': 'creators', 'с истекающим токеном': 'with an expiring token', 'Токен до': 'Token until', 'Задач ожидает': 'Pending tasks',
  'ER — коэффициент вовлечённости: (реакции + комментарии + репосты) ÷ просмотры × 100%. Чем выше показатель, тем активнее аудитория взаимодействует с контентом.': 'ER is the engagement rate: (reactions + comments + shares) ÷ views × 100%. A higher rate means the audience engages more actively with the content.',
  'Всё ок': 'All good', 'В работе': 'In progress', 'Подключены:': 'Connected:', 'Не удалось получить креаторов:': 'Could not load creators:', 'Не удалось загрузить дашборд:': 'Could not load the dashboard:',
  'Удалить': 'Delete', 'Архивируем…': 'Archiving…', 'Удалить навсегда': 'Delete permanently', 'Открываем…': 'Opening…', 'Переподключить VK ID': 'Reconnect VK ID',
  'Компания и её настройки VK будут удалены навсегда. Креаторы перейдут в категорию «Без компании», а их карточки и статистика сохранятся.': 'The company and its VK settings will be permanently deleted. Creators will move to “No company”, while their profiles and statistics will be preserved.',
  'Доступ подтверждён': 'Access confirmed', 'разрешений:': 'permissions:', 'Доступ:': 'Access:', 'ещё': 'more', 'Общий': 'Shared',
  'АРХИВ ИЗМЕНЕНИЙ': 'CHANGE HISTORY', 'История': 'History', 'Закрыть историю': 'Close history', 'Загружаем историю…': 'Loading history…', 'Не удалось загрузить историю:': 'Could not load history:',
  'Изменений пока нет': 'No changes yet', 'Здесь появятся прежние и новые значения после следующего редактирования.': 'Previous and new values will appear here after the next edit.', 'Было': 'Before', 'Стало': 'After',
  'Профиль креатора': 'Creator profile', 'Основные данные и быстрые контакты.': 'Core details and quick contacts.', 'Полное имя': 'Full name', 'Не назначена': 'Not assigned', 'Редактировать': 'Edit', 'Сохранить': 'Save',
  'Работы по креатору': 'Creator work', 'Текущее состояние карточки и задачи, которые требуют внимания.': 'Current profile status and tasks that need attention.', 'По креатору сейчас ничего делать не нужно.': 'No action is currently needed for this creator.',
  'Что нужно исправить': 'What needs to be fixed', 'Опишите, что не так и какие работы нужны': 'Describe the issue and the work required', 'Что сейчас в работе': 'What is currently in progress', 'Опишите задачу, которую вы взяли в работу': 'Describe the task you are working on', 'Комментарий': 'Comment', 'Нет комментария': 'No comment',
  'Корпоративный VK не выбран': 'Corporate VK is not selected', 'Выберите общий аккаунт фирмы и вставьте ссылку на сообщество этого креатора.': 'Select the shared company account and paste the link to this creator’s community.', 'Настроить VK у компании →': 'Set up VK for the company →',
  'Сначала': 'First', 'настройте общий VK-аккаунт у компании': 'set up the shared VK account for the company', 'Аккаунт, которому выдан доступ': 'Account with granted access', 'Сообщество': 'Community', 'Способ входа': 'Sign-in method', 'По номеру телефона': 'By phone number', 'Скопировать': 'Copy',
  'АРХИВ': 'ARCHIVE', 'Аккаунт': 'Account', 'Доступы и аккаунты': 'Access and accounts', 'Подключение': 'Connection', 'аккаунта': 'account', 'не завершено:': 'was not completed:',
  'Все данные, доступы и история сохранены.': 'All data, access details, and history are preserved.', 'Данные из рабочей таблицы. Секреты зашифрованы и раскрываются только администратору.': 'Data from the working table. Secrets are encrypted and can only be revealed by an administrator.',
  'Загружаем доступы…': 'Loading access details…', 'Загружаем карточку креатора…': 'Loading creator profile…', 'Загружаем подключения…': 'Loading connections…', 'Закрыть уведомление': 'Close notification',
  'Не удалось восстановить карточку:': 'Could not restore the profile:', 'Не удалось перенести карточку:': 'Could not archive the profile:', 'Перенесена': 'Moved', 'Редактировать доступы': 'Edit access details',
  'Нужна Facebook Page, связанная с профессиональным Instagram': 'A Facebook Page linked to the professional Instagram account is required',
  'VK · старые данные': 'VK · legacy data', 'Связь Instagram со страницей Facebook': 'Instagram connection to a Facebook Page', 'Связанный Instagram-аккаунт': 'Linked Instagram account', 'Доступ к страницам бизнес-портфолио': 'Business portfolio page access', 'Профиль': 'Profile',
  'Корпоративный аккаунт': 'Corporate account', 'Сообщество креатора': 'Creator community', 'Аккаунт с доступом': 'Account with access', 'логин': 'login', 'пароль': 'password', 'телефон': 'phone',
  'Не удалось создать креатора': 'Could not create the creator', 'Не удалось удалить данные платформы.': 'Could not delete the platform data.',
}

function interpolate(template: string, values?: TranslationValues) {
  if (!values) return template
  return template.replace(/\{(\w+)\}/g, (match, key: string) => key in values ? String(values[key]) : match)
}

export function I18nProvider({ children }: { children: ReactNode }) {
  const { pathname } = useLocation()
  const [locale, setLocaleState] = useState<Locale>(() => publicPathLocale(pathname) ?? readStoredLocale() ?? 'ru')

  const setLocale = useCallback((next: Locale) => {
    storeLocale(next)
    setLocaleState(next)
  }, [])

  useLayoutEffect(() => {
    const routeLocale = publicPathLocale(pathname)
    if (routeLocale) setLocale(routeLocale)
  }, [pathname, setLocale])

  useLayoutEffect(() => {
    document.documentElement.lang = locale
    document.title = locale === 'en' ? 'Statzavod' : 'Статзавод'
  }, [locale])

  const t = useCallback((key: string, values?: TranslationValues) => {
    let template = key
    if (locale === 'en') {
      template = en[key] ?? key
      if (template === key && key.startsWith('Ошибок синхронизации подряд:')) {
        template = key.replace('Ошибок синхронизации подряд:', 'Consecutive synchronization failures:')
      } else if (template === key && cyrillicPattern.test(key)) {
        template = 'Information is unavailable in English.'
      }
    }
    return interpolate(template, values)
  }, [locale])

  const toggleLocale = useCallback(() => setLocale(locale === 'ru' ? 'en' : 'ru'), [locale, setLocale])
  const value = useMemo(() => ({ locale, setLocale, toggleLocale, t }), [locale, setLocale, t, toggleLocale])
  return <I18nContext.Provider value={value}>{children}</I18nContext.Provider>
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
