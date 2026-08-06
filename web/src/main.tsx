import { StrictMode } from 'react'
import { createRoot } from 'react-dom/client'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { BrowserRouter } from 'react-router-dom'
import { App } from './app/App'
import { I18nProvider } from './shared/i18n/I18nProvider'
import './styles/globals.scss'
const client = new QueryClient({ defaultOptions: { queries: { staleTime: 30_000, retry: 1, refetchOnWindowFocus: false } } })
createRoot(document.getElementById('root')!).render(<StrictMode><QueryClientProvider client={client}><I18nProvider><BrowserRouter><App /></BrowserRouter></I18nProvider></QueryClientProvider></StrictMode>)
