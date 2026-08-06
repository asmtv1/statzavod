import { StrictMode, useEffect, useRef } from 'react'
import { createRoot } from 'react-dom/client'
import { QueryClient, QueryClientProvider, useQueryClient } from '@tanstack/react-query'
import { BrowserRouter } from 'react-router-dom'
import { App } from './app/App'
import { I18nProvider, useI18n } from './shared/i18n/I18nProvider'
import './styles/globals.scss'
const client = new QueryClient({ defaultOptions: { queries: { staleTime: 30_000, retry: 1, refetchOnWindowFocus: false } } })

function LocaleAwareApp() {
  const { locale } = useI18n()
  const queryClient = useQueryClient()
  const previousLocale = useRef(locale)

  useEffect(() => {
    if (previousLocale.current !== locale) {
      previousLocale.current = locale
      void queryClient.invalidateQueries()
    }
  }, [locale, queryClient])

  return <App />
}

createRoot(document.getElementById('root')!).render(<StrictMode><QueryClientProvider client={client}><BrowserRouter><I18nProvider><LocaleAwareApp /></I18nProvider></BrowserRouter></QueryClientProvider></StrictMode>)
