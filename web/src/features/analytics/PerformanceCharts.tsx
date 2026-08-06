import { useEffect, useRef, useState } from 'react'
import styles from './AnalyticsPage.module.scss'
import { useI18n } from '../../shared/i18n/I18nProvider'

export type DailyAnalyticsPoint = {
  date: string
  views: number
  likes: number
  publications: number
}

export type PlatformAnalyticsPoint = {
  platform: string
  views: number
  likes: number
  publications: number
}

type Metric = 'views' | 'likes' | 'publications'
type ChartInstance = { dispose(): void; resize(): void; setOption(option: object): void }

const platformNames: Record<string, string> = {
  YOUTUBE: 'YouTube',
  INSTAGRAM: 'Instagram',
  TIKTOK: 'TikTok',
  VK: 'VK',
}
const platformColors: Record<string, string> = {
  YOUTUBE: '#ef776d',
  INSTAGRAM: '#cf8cba',
  TIKTOK: '#9ce5bd',
  VK: '#78a9ef',
}
const fallbackColors = ['#e4b667', '#a69ce5', '#8fd7d1', '#d99b75']

const labels = (locale: 'ru'|'en'): Record<Metric, string> => locale === 'en'
  ? { views: 'Views', likes: 'Reactions', publications: 'Publications' }
  : { views: 'Просмотры', likes: 'Реакции', publications: 'Публикации' }

function parseDate(value: string) {
  return new Date(`${value}T00:00:00`)
}

function MetricSwitch({ value, onChange, locale, metrics = ['views', 'likes', 'publications'] }: {
  value: Metric
  onChange: (metric: Metric) => void
  locale: 'ru'|'en'
  metrics?: Metric[]
}) {
  const metricLabels = labels(locale)
  return <div className={styles.metricSwitch} aria-label={locale === 'en' ? 'Metric' : 'Показатель'}>
    {metrics.map(metric => <button
      aria-pressed={value === metric}
      className={value === metric ? styles.metricActive : undefined}
      key={metric}
      onClick={() => onChange(metric)}
      type="button"
    >{metricLabels[metric]}</button>)}
  </div>
}

export function DailyPerformanceChart({ data }: { data: DailyAnalyticsPoint[] }) {
  const { locale } = useI18n()
  const container = useRef<HTMLDivElement>(null)
  const [metric, setMetric] = useState<Metric>('views')
  const number = new Intl.NumberFormat(locale === 'en' ? 'en-US' : 'ru-RU')
  const shortDate = new Intl.DateTimeFormat(locale === 'en' ? 'en-US' : 'ru-RU', { day: 'numeric', month: 'short' })
  const metricLabels = labels(locale)

  useEffect(() => {
    if (!container.current || !data.length) return
    let chart: ChartInstance | undefined
    let cancelled = false
    void Promise.all([import('echarts/core'), import('echarts/charts'), import('echarts/components'), import('echarts/renderers')]).then(([core, charts, components, renderers]) => {
      if (cancelled || !container.current) return
      core.use([charts.LineChart, charts.BarChart, components.GridComponent, components.TooltipComponent, renderers.CanvasRenderer])
      chart = core.init(container.current)
      const color = metric === 'views' ? '#9ce5bd' : metric === 'likes' ? '#e4b667' : '#cf8b75'
      chart.setOption({
        animationDuration: 450,
        grid: { left: 12, right: 14, top: 20, bottom: 8, containLabel: true },
        tooltip: {
          trigger: 'axis',
          backgroundColor: '#202323',
          borderColor: 'rgba(243,238,229,.16)',
          padding: [9, 11],
          textStyle: { color: '#f3eee5', fontSize: 12 },
          valueFormatter: (value: number) => number.format(value),
        },
        xAxis: {
          type: 'category',
          boundaryGap: metric === 'publications',
          data: data.map(point => shortDate.format(parseDate(point.date))),
          axisLabel: { color: '#8f8b83', fontSize: 11, hideOverlap: true, margin: 12 },
          axisLine: { lineStyle: { color: 'rgba(243,238,229,.11)' } },
          axisTick: { show: false },
        },
        yAxis: {
          type: 'value',
          min: 0,
          minInterval: 1,
          axisLabel: { color: '#8f8b83', fontSize: 11, formatter: (value: number) => number.format(value) },
          splitLine: { lineStyle: { color: 'rgba(243,238,229,.07)' } },
        },
        series: [{
          name: metricLabels[metric],
          type: metric === 'publications' ? 'bar' : 'line',
          data: data.map(point => point[metric]),
          smooth: metric !== 'publications' ? 0.28 : undefined,
          showSymbol: data.length < 12,
          symbolSize: 7,
          lineStyle: { width: 3, color },
          itemStyle: { color, borderRadius: metric === 'publications' ? [5, 5, 0, 0] : undefined },
          areaStyle: metric === 'publications' ? undefined : { color, opacity: 0.1 },
          barMaxWidth: 24,
        }],
      })
    })
    const resize = () => chart?.resize()
    window.addEventListener('resize', resize)
    return () => { cancelled = true; window.removeEventListener('resize', resize); chart?.dispose() }
  }, [data, locale, metric])

  const total = data.reduce((sum, point) => sum + point[metric], 0)
  return <>
    <div className={styles.chartToolbar}>
      <MetricSwitch value={metric} onChange={setMetric} locale={locale}/>
      <span>{locale === 'en' ? 'For the period:' : 'За период:'} <strong>{number.format(total)}</strong></span>
    </div>
    <div ref={container} className={styles.dailyChart} role="img" aria-label={`${metricLabels[metric]} публикаций по дням`}/>
  </>
}

export function PlatformBreakdownChart({ data }: { data: PlatformAnalyticsPoint[] }) {
  const { locale } = useI18n()
  const container = useRef<HTMLDivElement>(null)
  const [metric, setMetric] = useState<Metric>('views')
  const total = data.reduce((sum, point) => sum + point[metric], 0)
  const number = new Intl.NumberFormat(locale === 'en' ? 'en-US' : 'ru-RU')
  const metricLabels = labels(locale)

  useEffect(() => {
    if (!container.current || !data.length || !total) return
    let chart: ChartInstance | undefined
    let cancelled = false
    void Promise.all([import('echarts/core'), import('echarts/charts'), import('echarts/components'), import('echarts/renderers')]).then(([core, charts, components, renderers]) => {
      if (cancelled || !container.current) return
      core.use([charts.PieChart, components.TooltipComponent, renderers.CanvasRenderer])
      chart = core.init(container.current)
      chart.setOption({
        animationDuration: 450,
        color: data.map((point, index) => platformColors[point.platform] ?? fallbackColors[index % fallbackColors.length]),
        tooltip: {
          trigger: 'item',
          backgroundColor: '#202323',
          borderColor: 'rgba(243,238,229,.16)',
          textStyle: { color: '#f3eee5', fontSize: 12 },
          formatter: (params: { name: string; value: number; percent: number }) => `${params.name}<br><b>${number.format(params.value)}</b> · ${params.percent}%`,
        },
        series: [{
          name: metricLabels[metric],
          type: 'pie',
          radius: ['63%', '84%'],
          center: ['50%', '50%'],
          avoidLabelOverlap: true,
          itemStyle: { borderColor: '#282b2b', borderWidth: 4, borderRadius: 6 },
          label: { show: false },
          emphasis: { scaleSize: 5 },
          data: data.map(point => ({ name: platformNames[point.platform] ?? point.platform, value: point[metric] })),
        }],
      })
    })
    const resize = () => chart?.resize()
    window.addEventListener('resize', resize)
    return () => { cancelled = true; window.removeEventListener('resize', resize); chart?.dispose() }
  }, [data, locale, metric, total])

  return <>
    <MetricSwitch value={metric} onChange={setMetric} locale={locale}/>
    {total ? <div className={styles.platformChartWrap}>
      <div ref={container} className={styles.platformChart} role="img" aria-label={`${metricLabels[metric]} по платформам`}/>
      <div className={styles.donutTotal} aria-hidden="true"><strong>{number.format(total)}</strong><span>{metricLabels[metric].toLowerCase()}</span></div>
    </div> : <div className={styles.chartEmpty}>{locale === 'en' ? 'No data for this metric' : 'Нет данных по показателю'}</div>}
    <div className={styles.platformLegend}>
      {data.map((point, index) => {
        const value = point[metric]
        const share = total ? Math.round(value / total * 100) : 0
        const color = platformColors[point.platform] ?? fallbackColors[index % fallbackColors.length]
        return <div key={point.platform}>
          <i style={{ backgroundColor: color }}/>
          <span><b>{platformNames[point.platform] ?? point.platform}</b><small>{point.publications} {locale === 'en' ? 'publications' : 'публикац.'}</small></span>
          <strong>{number.format(value)} <small>{share}%</small></strong>
        </div>
      })}
    </div>
  </>
}
