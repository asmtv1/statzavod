import { useEffect, useRef } from 'react'
import type { Timeseries } from '../../shared/api/client'
import styles from './ViewsChart.module.scss'
import { useI18n } from '../../shared/i18n/I18nProvider'

export function ViewsChart({ items }: Timeseries) {
  const { locale, t } = useI18n()
  const container = useRef<HTMLDivElement>(null)
  useEffect(() => {
    if (!container.current || items.length < 2) return
    let chart: { dispose(): void; resize(): void; setOption(option: object): void } | undefined
    let cancelled = false
    void Promise.all([import('echarts/core'), import('echarts/charts'), import('echarts/components'), import('echarts/renderers')]).then(([core, charts, components, renderers]) => {
      if (cancelled || !container.current) return
      core.use([charts.LineChart, components.GridComponent, components.TooltipComponent, renderers.CanvasRenderer])
      chart = core.init(container.current)
      chart.setOption({ grid: { left: 44, right: 16, top: 20, bottom: 28 }, tooltip: { trigger: 'axis', backgroundColor: '#242727', borderColor: 'rgba(243,238,229,.15)', textStyle: { color: '#f3eee5' } }, xAxis: { type: 'category', data: items.map((item) => item.date), axisLabel: { color: '#928f87' }, axisLine: { lineStyle: { color: 'rgba(243,238,229,.1)' } } }, yAxis: { type: 'value', min: 0, axisLabel: { color: '#928f87' }, splitLine: { lineStyle: { color: 'rgba(243,238,229,.08)' } } }, series: [{ type: 'line', data: items.map((item) => item.views), smooth: true, symbol: 'none', lineStyle: { color: '#9ce5bd', width: 2.5 }, areaStyle: { color: 'rgba(156,229,189,.16)' } }] })
    })
    const resize = () => chart?.resize()
    window.addEventListener('resize', resize)
    return () => { cancelled = true; window.removeEventListener('resize', resize); chart?.dispose() }
  }, [items, locale])
  if (items.length < 2) return <p className={styles.empty}>{t('Линейный график появится, когда будут собраны минимум две точки данных.')}</p>
  return <div ref={container} className={styles.chart} aria-label={t('Динамика просмотров')} />
}
