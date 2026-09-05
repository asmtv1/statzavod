import { expect, test, type Page } from '@playwright/test'
import { readFileSync } from 'node:fs'
import { createServer } from 'node:http'
import { once } from 'node:events'
import { execFileSync } from 'node:child_process'
import { fileURLToPath } from 'node:url'

const uploadOrigin = 'https://storage.yandexcloud.net'
const allowedTestOrigin = 'http://127.0.0.1:5173'
const caddyfile = readFileSync(fileURLToPath(new URL('../../deploy/Caddyfile', import.meta.url)), 'utf8')
const productionValidator = fileURLToPath(new URL('../../deploy/validate-production-config.sh', import.meta.url))
const corsPolicy = JSON.parse(readFileSync(fileURLToPath(new URL('../../deploy/yandex-object-storage-cors.json', import.meta.url)), 'utf8')) as {
  CORSRules: Array<{ AllowedHeaders: string[]; AllowedMethods: string[]; AllowedOrigins: string[]; ExposeHeaders: string[]; MaxAgeSeconds: number }>
}
const rule = corsPolicy.CORSRules[0]

function harnessHTML() {
  return '<!doctype html><html><head><title>Production upload harness</title></head><body><main><h1>Production upload harness</h1><output id="state">ready</output></main></body></html>'
}

async function startFakeS3(allowedOrigin: string) {
  const calls: Array<{ method: string; url: string; origin: string; requestedHeaders: string }> = []
  let putStatus = 200

  const server = createServer((request, response) => {
    const origin = request.headers.origin ?? ''
    const requestedHeaders = request.headers['access-control-request-headers'] ?? ''
    calls.push({ method: request.method ?? '', url: request.url ?? '', origin, requestedHeaders })
    const requested = requestedHeaders.split(',').map(value => value.trim().toLowerCase()).filter(Boolean)
    const approvedHeaders = requested.every(header => rule.AllowedHeaders.includes(header))
    const requestedMethod = request.headers['access-control-request-method'] ?? request.method ?? ''
    const approvedMethod = rule.AllowedMethods.includes(requestedMethod)
    if (origin === allowedOrigin && approvedHeaders && approvedMethod) {
      response.setHeader('Access-Control-Allow-Origin', allowedOrigin)
      response.setHeader('Access-Control-Allow-Methods', rule.AllowedMethods.join(', '))
      response.setHeader('Access-Control-Allow-Headers', rule.AllowedHeaders.join(', '))
      response.setHeader('Access-Control-Expose-Headers', rule.ExposeHeaders.join(', '))
      response.setHeader('Access-Control-Max-Age', String(rule.MaxAgeSeconds))
    }
    if (request.method === 'OPTIONS') { response.writeHead(204); response.end(); return }
    if (request.method === 'PUT') { response.setHeader('ETag', '"fixture-etag"'); response.writeHead(putStatus); response.end(); return }
    response.writeHead(200); response.end(request.method === 'HEAD' ? undefined : 'fixture')
  })
  server.listen(0, '127.0.0.1')
  await once(server, 'listening')
  const address = server.address()
  if (!address || typeof address === 'string') throw new Error('fake S3 did not bind a TCP port')
  return {
    origin: `http://127.0.0.1:${address.port}`,
    calls,
    failNextPut: () => { putStatus = 503 },
    close: async () => { server.close(); await once(server, 'close') },
  }
}

async function installHarness(page: Page, targetOrigin: string, appHost = '127.0.0.1') {
  const calls: Array<{ method: string; url: string }> = []

  await page.route(`http://${appHost}:5173/production-upload-harness`, route => route.fulfill({
    status: 200,
    contentType: 'text/html',
    headers: { 'Content-Security-Policy': `default-src 'self'; connect-src 'self' ${targetOrigin}; script-src 'self'` },
    body: harnessHTML(),
  }))
  await page.route(`http://${appHost}:5173/api/v1/media/uploads/**`, async route => {
    calls.push({ method: route.request().method(), url: route.request().url() })
    await route.fulfill({ status: route.request().method() === 'DELETE' ? 204 : 200, contentType: 'application/json', body: route.request().method() === 'DELETE' ? '' : JSON.stringify({ status: 'VALIDATING' }) })
  })
  await page.goto(`http://${appHost}:5173/production-upload-harness`)
  return { calls }
}

test('production CSP and private bucket CORS permit multipart complete and abort', async ({ page, context }) => {
  const browserErrors: string[] = []
  page.on('console', message => { if (message.type() === 'error') browserErrors.push(message.text()) })
  expect(caddyfile).toContain(`connect-src 'self' https://open.tiktokapis.com {$MEDIA_UPLOAD_ORIGIN:${uploadOrigin}}`)
  expect(caddyfile).not.toMatch(/connect-src[^;]*\*/)
  expect(rule.AllowedOrigins).toEqual(['https://statzavod.ru', 'https://www.statzavod.ru'])
  expect(rule.AllowedMethods).toEqual(expect.arrayContaining(['PUT', 'HEAD', 'GET']))
  expect(rule.AllowedHeaders).not.toContain('*')
  expect(rule.ExposeHeaders).toEqual(['ETag'])
  expect(rule.MaxAgeSeconds).toBeLessThanOrEqual(900)

  const fakeS3 = await startFakeS3(allowedTestOrigin)
  const harness = await installHarness(page, fakeS3.origin)
  // Chromium protects loopback targets with a local-network permission. Real
  // production uploads use public HTTPS; grant it here so this test reaches
  // and evaluates the fake server's actual CORS response.
  await context.grantPermissions(['local-network-access'], { origin: allowedTestOrigin })
  const preflight = await fetch(`${fakeS3.origin}/private-bucket/temporary/object`, {
    method: 'OPTIONS',
    headers: {
      Origin: allowedTestOrigin,
      'Access-Control-Request-Method': 'PUT',
      'Access-Control-Request-Headers': 'content-type',
    },
  })
  expect(preflight.status).toBe(204)
  expect(preflight.headers.get('access-control-allow-origin')).toBe(allowedTestOrigin)
  expect(preflight.headers.get('access-control-allow-headers')).toContain('content-type')
  const completed = await page.evaluate(async target => {
    try {
      const put = await fetch(`${target}/private-bucket/temporary/object?partNumber=1&uploadId=fake`, { method: 'PUT', headers: { 'Content-Type': 'video/mp4' }, body: new Blob(['part'], { type: 'video/mp4' }) })
      const etag = put.headers.get('ETag')
      if (!put.ok || !etag) throw new Error('upload failed')
      const complete = await fetch('/api/v1/media/uploads/upload-1/complete', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ parts: [{ partNumber: 1, etag }] }) })
      const head = await fetch(`${target}/private-bucket/temporary/object`, { method: 'HEAD' })
      const get = await fetch(`${target}/private-bucket/temporary/object`)
      return { etag, complete: complete.ok, head: head.ok, get: get.ok }
    } catch (error) { return { error: String(error) } }
  }, fakeS3.origin)
  expect(completed, JSON.stringify({ calls: fakeS3.calls, browserErrors })).toEqual({ etag: '"fixture-etag"', complete: true, head: true, get: true })
  expect(fakeS3.calls.some(call => call.method === 'OPTIONS' && call.requestedHeaders.includes('content-type')), JSON.stringify(fakeS3.calls)).toBe(true)
  expect(fakeS3.calls.some(call => call.method === 'PUT')).toBe(true)
  expect(harness.calls.some(call => call.url.endsWith('/complete') && call.method === 'POST')).toBe(true)

  fakeS3.failNextPut()
  const aborted = await page.evaluate(async target => {
    try {
      const put = await fetch(`${target}/private-bucket/temporary/object?partNumber=2&uploadId=fake`, { method: 'PUT', headers: { 'Content-Type': 'video/mp4' }, body: new Blob(['part'], { type: 'video/mp4' }) })
      if (!put.ok) throw new Error('upload failed')
      return false
    } catch {
      const abort = await fetch('/api/v1/media/uploads/upload-1', { method: 'DELETE' })
      return abort.ok
    }
  }, fakeS3.origin)
  expect(aborted).toBe(true)
  expect(harness.calls.some(call => call.url.endsWith('/upload-1') && call.method === 'DELETE')).toBe(true)
  await fakeS3.close()
})

test('production startup rejects inconsistent application, CSP and storage origins', () => {
  const baseEnv = {
    ...process.env,
    APP_ENV: 'production',
    SITE_ADDRESS: 'statzavod.ru, www.statzavod.ru',
    CORS_ORIGIN: 'https://statzavod.ru',
    CONTENT_PUBLISHING_ENABLED: 'true',
    MEDIA_S3_ENDPOINT: uploadOrigin,
    MEDIA_UPLOAD_ORIGIN: uploadOrigin,
    MEDIA_S3_BUCKET: 'private-fixture-bucket',
  }
  expect(() => execFileSync('sh', [productionValidator], { env: baseEnv })).not.toThrow()
  expect(() => execFileSync('sh', [productionValidator], { env: { ...baseEnv, MEDIA_UPLOAD_ORIGIN: 'https://evil.example' }, stdio: 'ignore' })).toThrow()
  expect(() => execFileSync('sh', [productionValidator], { env: { ...baseEnv, CORS_ORIGIN: 'https://other.example' }, stdio: 'ignore' })).toThrow()
})

test('CORS denies an unapproved origin and request header', async ({ browser }) => {
  const disallowedOriginPage = await browser.newPage()
  const originServer = await startFakeS3(allowedTestOrigin)
  await installHarness(disallowedOriginPage, originServer.origin, 'localhost')
  await disallowedOriginPage.context().grantPermissions(['local-network-access'], { origin: 'http://localhost:5173' })
  await expect(disallowedOriginPage.evaluate(async target => {
    await fetch(`${target}/private-bucket/object`, { method: 'PUT', headers: { 'Content-Type': 'video/mp4' }, body: 'part' })
  }, originServer.origin)).rejects.toThrow()
  await disallowedOriginPage.close()
  await originServer.close()

  const headerServer = await startFakeS3(allowedTestOrigin)
  const forbiddenPreflight = await fetch(`${headerServer.origin}/private-bucket/object`, {
    method: 'OPTIONS',
    headers: {
      Origin: allowedTestOrigin,
      'Access-Control-Request-Method': 'PUT',
      'Access-Control-Request-Headers': 'x-forbidden-upload-header',
    },
  })
  expect(forbiddenPreflight.status).toBe(204)
  expect(forbiddenPreflight.headers.get('access-control-allow-origin')).toBeNull()
  expect(headerServer.calls.some(call => call.method === 'OPTIONS' && call.requestedHeaders.includes('x-forbidden-upload-header'))).toBe(true)
  expect(headerServer.calls.some(call => call.method === 'PUT')).toBe(false)
  await headerServer.close()
})
