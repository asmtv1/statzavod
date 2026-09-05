import { expect, test, type Page } from '@playwright/test'

const principal = { id: 'u1', email: 'qa@example.test', role: 'OWNER', capabilities: ['CONTENT_EDIT'], companies: [{ id: 'co1', name: 'Northwind Studio' }], creatorProfiles: [], activeContext: { companyId: null, creatorId: null, allCompanies: true } }
const item = { id: 'content-1', creatorName: 'Анна Петрова', revision: 3, status: 'PARTIAL', updatedAt: '2026-08-30T10:00:00Z', scheduledAt: '2026-09-01T12:00:00Z', platforms: ['TIKTOK', 'VK'], targetCount: 2, attentionCount: 2 }
const detail = { ...item, companyId: 'co1', description: 'Fixture caption', hashtags: ['#qa'], platformOptions: {}, approval: { status: 'APPROVED', requester: 'qa@example.test', decider: 'owner@example.test', note: 'Approved fixture', requestedAt: '2026-08-29T10:00:00Z', decidedAt: '2026-08-29T11:00:00Z' }, targets: [{ id: 'target-1', platform: 'TIKTOK', status: 'WAITING_FOR_REAUTH', scheduledAt: item.scheduledAt, errorCode: 'REAUTH_REQUIRED', errorMessage: 'Reconnect required' }, { id: 'target-2', platform: 'VK', status: 'FAILED', scheduledAt: item.scheduledAt, errorCode: 'PROVIDER_ERROR', errorMessage: 'Temporary fixture failure' }, { id: 'target-3', platform: 'YOUTUBE', status: 'SUCCEEDED', scheduledAt: item.scheduledAt, errorCode: '', errorMessage: '', externalId: 'AbCdEf_1234', externalUrl: 'https://www.youtube.com/shorts/AbCdEf_1234' }, { id: 'target-4', platform: 'YOUTUBE', status: 'SUCCEEDED', scheduledAt: item.scheduledAt, errorCode: '', errorMessage: '', externalUrl: 'javascript:alert(1)?X-Amz-Signature=secret' }], availableActions: ['retry', 'copy', 'schedule', 'publish', 'cancel'] }
let commandPayload: Record<string, unknown> | undefined

async function fixture(page: Page) {
  await page.route('**/api/v1/**', async route => {
    const url = new URL(route.request().url()); const path = url.pathname.replace('/api/v1', ''); const method = route.request().method()
    let body: unknown = {}
    if (path === '/auth/me') body = principal
    else if (path === '/notifications') body = { items: [], unreadCount: 0, nextBefore: '' }
    else if (path === '/content-items' && method === 'GET') body = { items: [item] }
    else if (path === '/content-items/content-1' && method === 'GET') body = detail
    else if (path.endsWith('/attempts')) body = { items: [{ id: 'attempt-1', status: 'FAILED', startedAt: item.updatedAt, providerOperationId: 'op-1', errorCode: 'PROVIDER_ERROR', errorMessage: 'Fixture failure' }], nextBefore: '' }
    else if (path === '/creators') body = { items: [{ id: 'creator-1', displayName: 'Анна Петрова' }] }
    else if (path === '/creators/creator-1/accounts') body = { items: [{ id: 'tt-1', platform: 'TIKTOK', username: 'anna', displayName: 'TikTok', status: 'ACTIVE' }, { id: 'yt-1', platform: 'YOUTUBE', username: 'anna', displayName: 'YouTube', status: 'ACTIVE' }, { id: 'vk-1', platform: 'VK', username: 'anna', displayName: 'VK Video', status: 'ACTIVE' }] }
    else if (path === '/content-preflight') body = { targets: [{ accountId: 'tt-1', platform: 'TIKTOK', preflight: { compatible: true, warnings: ['Creator info requires review'], requiredFields: [], allowedPrivacy: ['SELF_ONLY'], requiresReauth: false, tiktok: { compatible: true, privacy: ['SELF_ONLY'], commentsDisabled: false, duetDisabled: false, stitchDisabled: false, warnings: ['Creator info requires review'], requiredFields: [], reconnectAction: '' } } }] }
    else if (path === '/content-items' && method === 'POST') body = { id: 'content-1', revision: 1, status: 'DRAFT' }
    else if (path.includes('/media/uploads/upload-1/parts')) body = { url: 'http://127.0.0.1:5173/fixture-upload', headers: {} }
    else if (path.includes('/media/uploads') && method === 'POST') body = path.endsWith('/complete') ? { mediaAssetId: 'asset-1', status: 'READY' } : { uploadId: 'upload-1', partSize: 1024 * 1024, expiresAt: '2026-09-01T00:00:00Z' }
    else if (path.includes('/content-items/content-1/') && method === 'POST') { commandPayload = route.request().postDataJSON() as Record<string, unknown> | undefined; body = { id: 'content-1', revision: 4, status: 'RETRY_SCHEDULED' } }
    if (path.startsWith('/unknown')) throw new Error(`Unhandled fixture API route: ${method} ${path}`)
    await route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify(body) })
  })
  await page.route('**/fixture-upload', route => route.fulfill({ status: 200, headers: { ETag: 'fixture-etag', 'Access-Control-Expose-Headers': 'ETag' }, body: '' }))
}

async function screenshotAt(page: Page, path: string, width: number, height: number) {
  await page.setViewportSize({ width, height }); const shot = await page.screenshot({ path, fullPage: false });
  expect(shot.readUInt32BE(16)).toBe(width); expect(shot.readUInt32BE(20)).toBe(height)
}

test('publishing fixture covers composer, warnings, schedule, partial retry/reauth, modal focus and mobile agenda', async ({ page }) => {
  const errors: string[] = []; page.on('console', message => { if (message.type() === 'error') errors.push(message.text()) }); page.on('pageerror', error => errors.push(error.message))
  await fixture(page); await page.goto('/app/publishing'); await expect(page.getByRole('heading', { name: /Планирование публикаций|Publishing planning/ })).toBeVisible()
  await screenshotAt(page, '/tmp/statzavod-playwright/publishing-1440x900.png', 1440, 900)
  await page.getByRole('button', { name: /Новая публикация|New publication/ }).click(); await expect(page.getByLabel(/Вертикальное видео|Vertical video/)).toBeVisible()
  await page.getByLabel(/Вертикальное видео|Vertical video/).setInputFiles({ name: 'tiny.mp4', mimeType: 'video/mp4', buffer: Buffer.from('fixture') })
  await page.getByLabel(/Креатор|Creator/).selectOption('creator-1')
  await page.getByLabel(/Общее описание|Common description/).fill('Fixture caption'); await page.getByLabel(/Хештеги|Hashtags/).fill('#qa')
  await page.getByRole('checkbox', { name: /TikTok anna/ }).check(); await expect(page.getByText(/Creator info requires review/)).toBeVisible(); await page.getByLabel(/TikTok privacy/).selectOption('SELF_ONLY'); await page.getByRole('group', { name: /Allow comments/ }).getByText('Yes').click(); await page.getByRole('group', { name: /Allow Duet/ }).getByText('Yes').click(); await page.getByRole('group', { name: /Allow Stitch/ }).getByText('Yes').click(); await page.getByText(/I have explicit publishing consent/).click(); await page.getByText(/Music usage confirmed/).click(); await page.getByRole('button', { name: /Сохранить черновик|Save draft/ }).click(); await expect(page.getByRole('status')).toContainText(/Черновик сохранён|Draft saved/)
  await page.getByLabel(/Опубликовать по московскому времени|Publish at Moscow time/).fill('2099-01-01T12:00'); await page.getByRole('button', { name: /Запланировать|Schedule/ }).click(); await expect.poll(() => commandPayload).toMatchObject({ scheduledAt: '2099-01-01T09:00:00.000Z' })
  page.once('dialog', dialog => dialog.accept()); await page.getByRole('tab', { name: /Планирование|Planning/ }).click(); await page.getByRole('button', { name: /Анна Петрова/ }).click(); await expect(page.getByRole('dialog')).toContainText('APPROVED'); const publicationLink = page.getByRole('dialog').getByRole('link', { name: /Открыть публикацию|Open publication/ }); await expect(publicationLink).toHaveCount(1); await expect(publicationLink).toHaveAttribute('href', 'https://www.youtube.com/shorts/AbCdEf_1234'); await expect(publicationLink).toHaveAttribute('target', '_blank'); await expect(publicationLink).toHaveAttribute('rel', 'noopener noreferrer'); await expect(page.getByRole('dialog')).not.toContainText('javascript:'); await expect(page.getByRole('dialog')).not.toContainText('X-Amz')
  await screenshotAt(page, '/tmp/statzavod-playwright/publishing-1024x768.png', 1024, 768); await page.getByRole('button', { name: /История попыток|Attempt history/ }).click(); await expect(page.getByRole('dialog')).toContainText('attempt-1'); await expect(page.getByRole('dialog')).not.toContainText('op-1'); await page.getByRole('dialog').getByRole('checkbox', { name: /Выбрать для повтора/ }).nth(1).check(); await expect(page.getByRole('dialog').getByRole('button', { name: /Повторить выбранные ошибки|Retry selected errors/ })).toBeEnabled(); await page.getByRole('dialog').getByRole('button', { name: /Повторить выбранные ошибки|Retry selected errors/ }).click(); await expect.poll(() => commandPayload).toMatchObject({ targetIds: ['target-2'] }); await page.getByRole('dialog').press('Tab'); await page.getByRole('dialog').press('Escape'); await expect(page.getByRole('dialog')).toHaveCount(0)
  await page.setViewportSize({ width: 390, height: 844 }); await page.getByRole('button', { name: /Календарь|Calendar/ }).click(); await screenshotAt(page, '/tmp/statzavod-playwright/publishing-390x844.png', 390, 844)
  expect(errors, errors.join('\n')).toEqual([])
})
