# Статзавод

MVP системы статистики креаторов: Go API и worker, PostgreSQL, React/Vite dashboard с CSS Modules и SCSS.

## Быстрый старт

```bash
docker compose -f deploy/compose.development.yml up --build
```

Откройте `http://localhost:5173`. Начальные реквизиты для development: `admin@example.com` / `change-me-before-production`. Перед любым production-развёртыванием задайте уникальный `BOOTSTRAP_PASSWORD`.

Реализованы: миграция PostgreSQL, cookie-сессии/RBAC, bootstrap admin, CRUD-начало для креаторов, OAuth-подключение YouTube, Instagram, TikTok и VK, API статистики и синхронизации, автономный worker и responsive dashboard. Адаптеры намеренно не используют фиктивные секреты.

## Подключение платформ

Скопируйте `.env.example` в `.env`, задайте `TOKEN_ENCRYPTION_KEY` и OAuth Client ID/Client Secret нужных платформ. В кабинетах разработчика зарегистрируйте точные callback URL:

```text
https://statzavod.ru/api/v1/oauth/youtube/callback
https://statzavod.ru/api/v1/oauth/instagram/callback
https://statzavod.ru/api/v1/oauth/tiktok/callback
https://statzavod.ru/api/v1/oauth/vk/callback
```

YouTube требует включённых YouTube Data API и YouTube Analytics API. Для Instagram нужен продукт Instagram API with Instagram Login и разрешения `instagram_business_basic`, `instagram_business_manage_insights`. Для TikTok нужны Login Kit и Display API со scopes `user.info.basic`, `user.info.stats`, `video.list`. Для VK приложению нужны разрешения `video`, `stats`, `offline`.

После добавления реквизитов откройте «Креаторы», выберите карточку и нажмите «Подключить» у нужной платформы. Токены сохраняются только на сервере в зашифрованном виде.

## Production и CI/CD

Push в `main` запускает `.github/workflows/deploy.yml`:

1. Go-тесты, `go vet`, сборку бинарников и web-приложения.
2. Сборку immutable Docker-образов API/worker/migrate и web.
3. Публикацию образов в GitHub Container Registry с тегом commit SHA.
4. Деплой по SSH, миграцию БД, ожидание healthcheck и переключение текущего релиза.

При неуспешном healthcheck скрипт возвращает предыдущие образы. Миграции БД
автоматически не откатываются, поэтому production-миграции должны оставаться
обратно совместимыми с предыдущей версией приложения.

### Первичная подготовка сервера

На сервере должны быть установлены Docker Engine, Docker Compose v2 и `curl`.
Создайте каталог деплоя и production env:

```bash
sudo mkdir -p /opt/statzavod/shared
sudo cp deploy/.env.production.example /opt/statzavod/shared/.env
sudo chmod 600 /opt/statzavod/shared/.env
```

Заполните `/opt/statzavod/shared/.env` реальными значениями. Файл не
перезаписывается во время деплоя.

В GitHub Environment `production` добавьте secrets:

- `DEPLOY_HOST` — адрес сервера.
- `DEPLOY_USER` — пользователь с доступом к Docker.
- `DEPLOY_PORT` — SSH-порт, обычно `22`.
- `DEPLOY_PATH` — абсолютный путь, например `/opt/statzavod`.
- `DEPLOY_SSH_KEY` — приватный SSH-ключ deploy-пользователя.
- `DEPLOY_KNOWN_HOSTS` — строка сервера из заранее проверенного `known_hosts`.

В repository variables добавьте:

- `DEPLOY_ENABLED` — `true` после первичной подготовки сервера и secrets.
- `HEALTHCHECK_URL` — `https://statzavod.ru/readyz`.

Пока `DEPLOY_ENABLED` не установлен, push в `main` выполняет CI и публикует
образы, но безопасно пропускает подключение к серверу. После включения запустите
workflow вручную или отправьте следующий commit.

`DEPLOY_KNOWN_HOSTS` можно получить командой `ssh-keyscan`, но отпечаток ключа
нужно сверить с сервером по доверенному каналу до добавления в GitHub.
