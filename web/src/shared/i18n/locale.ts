export type Locale = 'ru' | 'en'

const storageKey = 'statzavod.locale.v1'
const russianPublicPaths = new Set([
  '/',
  '/features',
  '/security',
  '/support',
  '/request-access',
  '/terms',
  '/privacy',
  '/security-policy',
  '/cookies',
  '/personal-data-consent',
  '/data-deletion',
])

function normalizePath(pathname: string) {
  return pathname.length > 1 ? pathname.replace(/\/+$/, '') : pathname
}

export function publicPathLocale(pathname: string): Locale | null {
  const normalized = normalizePath(pathname)
  if (normalized === '/en' || normalized.startsWith('/en/')) return 'en'
  return russianPublicPaths.has(normalized) ? 'ru' : null
}

export function readStoredLocale(): Locale | null {
  try {
    const saved = localStorage.getItem(storageKey)
    return saved === 'en' || saved === 'ru' ? saved : null
  } catch {
    return null
  }
}

export function storeLocale(locale: Locale) {
  try {
    localStorage.setItem(storageKey, locale)
  } catch {
    // Locale still works for the current page when storage is unavailable.
  }
}

export function getRequestLocale(): Locale {
  return publicPathLocale(window.location.pathname) ?? readStoredLocale() ?? 'ru'
}
