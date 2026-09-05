# Content publishing: data handling disclosure

This document is the implementation-facing supplement to the public [Privacy
Policy](/privacy), [Terms](/terms), [Data deletion](/data-deletion), and
[Information Security Policy](/security-policy). The public pages are the
source of the user-facing legal text; this supplement keeps the publishing
workflow and the retention boundaries explicit for operators and platform
reviewers.

The operator and support contact are `Смотров Андрей Александрович` and
`asmtv1@yandex.ru`. Replace the contact only after updating the public pages
and the review packages together.

## Русское раскрытие

Statzavod принимает загруженный пользователем короткий вертикальный ролик,
проверяет его контейнер, MIME, размер, checksum, длительность и вертикальное
соотношение сторон, а затем по явной команде пользователя отправляет его на
выбранные подключённые аккаунты TikTok, Instagram, YouTube или VK Video.
Публикация выполняется автоматически worker-ом после подтверждения или
расписания пользователя. Пользователь видит выбранный аккаунт, платформу,
параметры privacy и результат каждого target.

Видеофайл хранится в приватном бакете Yandex Object Storage. Бакет не имеет
public-read; провайдер получает только временный HTTPS delivery URL. Процесс
загрузки или проверки может жить около 24 часов. Невостребованный,
отклонённый или временный объект удаляется после семи дней по fenced cleanup
worker-у. Объект, на который ссылается ревизия или успешная публикация, не
удаляется этой уборкой. После отмены/архивирования будущие jobs отменяются,
выполняющиеся jobs кооперативно останавливаются, внешние публикации не
удаляются, а удаляются только временные media-объекты согласно этим правилам.

Для публикации используются только выбранные пользователем разрешения
подключённого аккаунта: TikTok `video.publish`, Instagram
`instagram_business_content_publish` для Instagram Login или
`instagram_content_publish` для Facebook Login for Business, YouTube
`youtube.upload`, VK Video `video` с дополнительными `wall`/`groups` только
для соответствующего документированного сценария. Read-only подключение не
становится publish-ready автоматически. Statzavod не запрашивает пароль
платформы и не хранит его.

Statzavod не выполняет автоматическую модерацию видео и не обещает, что
платформа одобрит или покажет публикацию. Контент может проверяться самой
платформой по её правилам. Пользователь отвечает за права на ролик, музыку,
описание и раскрытие коммерческого характера. Для TikTok перед отправкой
должно быть явно показано согласие с Music Usage Confirmation.

Отключение аккаунта или запрос на удаление отзывает локальную связь и
удаляет сохранённые токены и импортированные данные после проверки полномочий.
Отзыв разрешения на стороне платформы останавливает будущие API-вызовы, но
сам по себе не доказывает удаление уже импортированных данных из Statzavod;
для этого используется `/data-deletion` или письмо на
`asmtv1@yandex.ru`. Минимальная запись аудита удаления может храниться до
трёх лет; изолированные резервные копии удаляются обычным циклом ротации.

Провайдерские политики и правила могут измениться: [TikTok Privacy
Policy](https://www.tiktok.com/legal/privacy-policy), [Meta Privacy
Policy](https://www.facebook.com/privacy/policies/), [Google Privacy
Policy](https://policies.google.com/privacy), [VK Privacy
Policy](https://vk.com/privacy). Statzavod не является TikTok, Meta,
YouTube или VK и не обещает функцию VK Clips: первый релиз поддерживает VK
Video через документированный video upload/post workflow.

## English disclosure

Statzavod accepts a short vertical video uploaded by an authorized user,
validates its container, MIME type, size, checksum, duration, and vertical
dimensions, and sends it on the user’s explicit command to selected connected
TikTok, Instagram, YouTube, or VK Video accounts. A worker performs the post
after the user confirms or schedules it. The UI shows the selected account,
privacy settings, and the result of every target.

The video is stored in a private Yandex Object Storage bucket. The bucket is
not public-read; a provider receives only a time-limited HTTPS delivery URL.
An upload or validation session may remain for about 24 hours. An unreferenced,
rejected, or temporary object is removed after seven days by the fenced cleanup
worker. Media referenced by a revision or successful publication is not removed
by that cleanup. When a company is archived or a revision is cancelled, future
jobs are cancelled, running jobs stop cooperatively, external posts are kept,
and only temporary media is eligible for cleanup under these rules.

Publishing uses only the selected account scopes: TikTok `video.publish`,
Instagram `instagram_business_content_publish` for Instagram Login or
`instagram_content_publish` for Facebook Login for Business, YouTube
`youtube.upload`, and VK Video `video`, with `wall`/`groups` only for the
documented VK workflow. A read-only connection is not silently upgraded to
publishing. Statzavod never asks for or stores a platform password.

Statzavod does not automatically moderate videos and does not promise that a
platform will approve or display a post. The platform may apply its own review
and policy enforcement. The user is responsible for rights to the video,
music, caption, and commercial disclosure. TikTok publishing must show the
required Music Usage Confirmation before sending the video.

Disconnecting an account or an authorized deletion request removes the local
connection, stored tokens, and imported data after an authorization check.
Revoking access on the platform stops future API calls but does not by itself
confirm deletion of data already imported into Statzavod; use `/en/data-deletion`
or email `asmtv1@yandex.ru`. A minimal deletion audit record may be kept for up
to three years; isolated backups follow their normal rotation.

Provider policies and rules may change: [TikTok Privacy
Policy](https://www.tiktok.com/legal/privacy-policy), [Meta Privacy
Policy](https://www.facebook.com/privacy/policies/), [Google Privacy
Policy](https://policies.google.com/privacy), and [VK Privacy
Policy](https://vk.com/privacy). Statzavod is not TikTok, Meta, YouTube, or VK
and does not promise VK Clips: the first release supports VK Video through the
documented video upload/post workflow.

