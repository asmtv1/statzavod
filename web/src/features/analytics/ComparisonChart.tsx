import { useEffect, useRef } from 'react'
import type { CreatorAnalytics } from '../../shared/api/client'
import styles from './AnalyticsPage.module.scss'

export function ComparisonChart({ reports }: { reports: CreatorAnalytics[] }) {
  const container = useRef<HTMLDivElement>(null)

  useEffect(() => {
    if (!container.current || !reports.length) return
    let chart: { dispose():void; resize():void; setOption(option:object):void } | undefined
    let cancelled = false
    void Promise.all([import('echarts/core'), import('echarts/charts'), import('echarts/components'), import('echarts/renderers')]).then(([core, charts, components, renderers]) => {
      if (cancelled || !container.current) return
      core.use([charts.BarChart, components.GridComponent, components.TooltipComponent, components.LegendComponent, renderers.CanvasRenderer])
      chart = core.init(container.current)
      const metric = (report:CreatorAnalytics, key:string) => report.kpis.find(item => item.key === key)?.value ?? 0
      chart.setOption({
        grid:{ left:44, right:18, top:45, bottom:42 },
        tooltip:{ trigger:'axis', backgroundColor:'#242727', borderColor:'rgba(243,238,229,.15)', textStyle:{ color:'#f3eee5' } },
        legend:{ top:8, right:8, textStyle:{ color:'#99958e' } },
        xAxis:{ type:'category', data:reports.map(report => report.creatorName), axisLabel:{ color:'#928f87', overflow:'truncate', width:110 }, axisLine:{ lineStyle:{ color:'rgba(243,238,229,.1)' } } },
        yAxis:{ type:'value', min:0, axisLabel:{ color:'#928f87' }, splitLine:{ lineStyle:{ color:'rgba(243,238,229,.08)' } } },
        series:[
          { name:'Просмотры', type:'bar', data:reports.map(report => metric(report,'views')), itemStyle:{ color:'#9ce5bd', borderRadius:[5,5,0,0] }, barMaxWidth:34 },
          { name:'Реакции', type:'bar', data:reports.map(report => metric(report,'likes')), itemStyle:{ color:'#e4b667', borderRadius:[5,5,0,0] }, barMaxWidth:34 },
        ],
      })
    })
    const resize = () => chart?.resize()
    window.addEventListener('resize', resize)
    return () => { cancelled = true; window.removeEventListener('resize', resize); chart?.dispose() }
  }, [reports])

  return <div ref={container} className={styles.chart} aria-label="Сравнение показателей креаторов"/>
}
