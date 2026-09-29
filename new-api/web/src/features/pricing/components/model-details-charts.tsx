/*
Copyright (C) 2023-2026 QuantumNous

This program is free software: you can redistribute it and/or modify
it under the terms of the GNU Affero General Public License as
published by the Free Software Foundation, either version 3 of the
License, or (at your option) any later version.

This program is distributed in the hope that it will be useful,
but WITHOUT ANY WARRANTY; without even the implied warranty of
MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
GNU Affero General Public License for more details.

You should have received a copy of the GNU Affero General Public License
along with this program. If not, see <https://www.gnu.org/licenses/>.

For commercial licensing, please contact support@quantumnous.com
*/
import { VChart } from '@visactor/react-vchart'
import { useMemo } from 'react'
import { useTranslation } from 'react-i18next'

import { useThemeCustomization } from '@/context/theme-customization-provider'
import { getSuccessRateColor } from '@/features/performance-metrics/lib/format'
import { useThemeRadiusPx } from '@/lib/theme-radius'
import { useChartTheme } from '@/lib/use-chart-theme'
import { cn } from '@/lib/utils'
import { VCHART_OPTION } from '@/lib/vchart'

import type { LatencyTimePoint, UptimeDayPoint } from '../lib/mock-stats'

function formatHourLabel(iso: string): string {
  const date = new Date(iso)
  const hours = date.getHours()
  return `${String(hours).padStart(2, '0')}:00`
}

function formatDayLabel(date: string): string {
  const parsed = new Date(date)
  if (date.includes('T')) {
    return parsed.toLocaleString(undefined, {
      month: 'short',
      day: 'numeric',
      hour: '2-digit',
    })
  }
  return parsed.toLocaleDateString(undefined, {
    month: 'short',
    day: 'numeric',
  })
}

function getChartThemeTokens(resolvedTheme: string) {
  return {
    textColor:
      resolvedTheme === 'dark'
        ? 'rgba(255, 255, 255, 0.68)'
        : 'rgba(15, 23, 42, 0.58)',
    gridColor:
      resolvedTheme === 'dark'
        ? 'rgba(255, 255, 255, 0.12)'
        : 'rgba(15, 23, 42, 0.12)',
  }
}

const UPTIME_AXIS_MAX = 100
const UPTIME_FOCUSED_AXIS_MIN = 95
const UPTIME_MINOR_OUTAGE_AXIS_MIN = 90

function toUptimeChartValue(value: number): number {
  if (!Number.isFinite(value)) return 0
  return Math.min(UPTIME_AXIS_MAX, Math.max(0, value))
}

function getUptimeAxisMin(values: number[]): number {
  const finiteValues = values.filter((value) => Number.isFinite(value))
  if (finiteValues.length === 0) return UPTIME_FOCUSED_AXIS_MIN

  const minValue = Math.max(0, Math.min(...finiteValues))
  if (minValue >= UPTIME_FOCUSED_AXIS_MIN) return UPTIME_FOCUSED_AXIS_MIN
  if (minValue >= UPTIME_MINOR_OUTAGE_AXIS_MIN) {
    return UPTIME_MINOR_OUTAGE_AXIS_MIN
  }

  return Math.max(0, Math.floor((minValue - 5) / 10) * 10)
}

function stripUptimePointSuffix(value: string): string {
  return value.replace(/__(start|end)$/, '')
}

// ---------------------------------------------------------------------------
// Latency trend chart (24h, multi-group point-line chart)
// ---------------------------------------------------------------------------

export function LatencyTrendChart(props: {
  series: LatencyTimePoint[]
  className?: string
}) {
  const { t } = useTranslation()
  const { resolvedTheme, themeReady } = useChartTheme()
  const { textColor, gridColor } = getChartThemeTokens(resolvedTheme)

  const spec = useMemo(() => {
    if (props.series.length === 0) return null
    const data = props.series.map((point) => ({
      time: formatHourLabel(point.timestamp),
      group: point.group,
      ttft: point.ttft_ms,
    }))
    return {
      type: 'line' as const,
      data: [{ id: 'latency', values: data }],
      xField: 'time',
      yField: 'ttft',
      seriesField: 'group',
      smooth: true,
      point: {
        visible: true,
        style: { size: 5, stroke: '#ffffff', lineWidth: 1.5 },
      },
      line: {
        style: { lineWidth: 2 },
      },
      legends: { visible: false },
      tooltip: {
        mark: {
          title: { value: (d: { time: string }) => d.time },
          content: [
            {
              key: t('Average TTFT'),
              value: (d: { ttft: number }) => `${Math.round(d.ttft)} ms`,
            },
          ],
        },
      },
      axes: [
        {
          orient: 'bottom',
          label: {
            style: { fill: textColor, fontSize: 10 },
          },
          tick: { visible: false },
        },
        {
          orient: 'left',
          label: {
            formatMethod: (val: number | string) => `${val} ms`,
            style: { fill: textColor, fontSize: 10 },
          },
          grid: {
            visible: true,
            style: { lineDash: [3, 3], stroke: gridColor },
          },
        },
      ],
    }
  }, [gridColor, props.series, t, textColor])

  if (props.series.length === 0) {
    return (
      <div
        className={cn(
          'text-muted-foreground flex h-48 items-center justify-center rounded-lg border text-xs',
          props.className
        )}
      >
        {t('No latency data available')}
      </div>
    )
  }

  return (
    <div className={cn('h-64 sm:h-72', props.className)}>
      {themeReady && spec && (
        <VChart
          key={`latency-${resolvedTheme}`}
          spec={{
            ...spec,
            theme: resolvedTheme === 'dark' ? 'dark' : 'light',
            background: 'transparent',
          }}
          option={VCHART_OPTION}
        />
      )}
    </div>
  )
}

// ---------------------------------------------------------------------------
// Uptime trend chart (24h, point-line chart)
// ---------------------------------------------------------------------------

export function UptimeTrendChart(props: {
  series: UptimeDayPoint[]
  className?: string
}) {
  const { t } = useTranslation()
  const { resolvedTheme, themeReady } = useChartTheme()
  const { textColor, gridColor } = getChartThemeTokens(resolvedTheme)

  const spec = useMemo(() => {
    if (props.series.length === 0) return null

    const rawData = props.series.map((point) => ({
      date: formatDayLabel(point.date),
      uptime: toUptimeChartValue(point.uptime_pct),
      incidents: point.incidents,
      outage: point.outage_minutes,
    }))
    const data =
      rawData.length === 1
        ? [
            { ...rawData[0], date: `${rawData[0].date}__start` },
            { ...rawData[0], date: `${rawData[0].date}__end` },
          ]
        : rawData
    const axisMin = getUptimeAxisMin(rawData.map((point) => point.uptime))

    return {
      type: 'line' as const,
      data: [{ id: 'uptime', values: data }],
      xField: 'date',
      yField: 'uptime',
      smooth: true,
      line: {
        style: { stroke: '#10b981', lineWidth: 2 },
      },
      point: {
        visible: true,
        style: {
          size: 5,
          stroke: '#ffffff',
          lineWidth: 1.5,
          fill: (datum: { uptime: number }) =>
            getSuccessRateColor(datum.uptime),
        },
      },
      tooltip: {
        mark: {
          title: {
            value: (d: { date: string }) => stripUptimePointSuffix(d.date),
          },
          content: [
            {
              key: t('Uptime'),
              value: (d: { uptime: number }) => `${d.uptime.toFixed(2)}%`,
            },
            {
              key: t('Incidents'),
              value: (d: { incidents: number }) => `${d.incidents}`,
            },
            {
              key: t('Outage'),
              value: (d: { outage: number }) => `${d.outage} ${t('minutes')}`,
            },
          ],
        },
      },
      axes: [
        {
          orient: 'bottom',
          label: {
            formatMethod: (val: number | string) =>
              stripUptimePointSuffix(String(val)),
            style: { fill: textColor, fontSize: 10 },
            autoLimit: true,
          },
          tick: { visible: false },
        },
        {
          orient: 'left',
          min: axisMin,
          max: UPTIME_AXIS_MAX,
          label: {
            formatMethod: (val: number | string) => `${val}%`,
            style: { fill: textColor, fontSize: 10 },
          },
          grid: {
            visible: true,
            style: { lineDash: [3, 3], stroke: gridColor },
          },
        },
      ],
    }
  }, [gridColor, props.series, t, textColor])

  if (props.series.length === 0) {
    return (
      <div
        className={cn(
          'text-muted-foreground flex h-48 items-center justify-center rounded-lg border text-xs',
          props.className
        )}
      >
        {t('No uptime data available')}
      </div>
    )
  }

  return (
    <div className={cn('h-56 sm:h-64', props.className)}>
      {themeReady && spec && (
        <VChart
          key={`uptime-trend-${resolvedTheme}`}
          spec={{
            ...spec,
            theme: resolvedTheme === 'dark' ? 'dark' : 'light',
            background: 'transparent',
          }}
          option={VCHART_OPTION}
        />
      )}
    </div>
  )
}

// ---------------------------------------------------------------------------
// Throughput by group (horizontal bar)
// ---------------------------------------------------------------------------

export function ThroughputBarChart(props: {
  rows: { group: string; throughput_tps: number }[]
  className?: string
}) {
  const { t } = useTranslation()
  const { resolvedTheme, themeReady } = useChartTheme()
  const { textColor, gridColor } = getChartThemeTokens(resolvedTheme)
  const { customization } = useThemeCustomization()
  const barRadius = useThemeRadiusPx(
    '--radius-sm',
    `${customization.preset}:${customization.radius}`
  )

  const filtered = useMemo(
    () => props.rows.filter((r) => r.throughput_tps > 0),
    [props.rows]
  )

  const spec = useMemo(() => {
    if (filtered.length === 0) return null
    return {
      type: 'bar' as const,
      direction: 'horizontal' as const,
      data: [{ id: 'tput', values: filtered.map((r) => ({ ...r })) }],
      xField: 'throughput_tps',
      yField: 'group',
      bar: {
        style: {
          fill: '#6366f1',
          ...(barRadius == null ? {} : { cornerRadius: barRadius }),
        },
      },
      label: {
        visible: true,
        position: 'right',
        style: { fontSize: 11, fill: textColor },
        formatMethod: (text: string) => `${text} t/s`,
      },
      axes: [
        {
          orient: 'left',
          label: { style: { fill: textColor, fontSize: 10 } },
          tick: { visible: false },
        },
        {
          orient: 'bottom',
          label: { style: { fill: textColor, fontSize: 10 } },
          grid: {
            visible: true,
            style: { lineDash: [3, 3], stroke: gridColor },
          },
        },
      ],
      tooltip: {
        mark: {
          title: { value: (d: { group: string }) => d.group },
          content: [
            {
              key: t('Throughput'),
              value: (d: { throughput_tps: number }) =>
                `${d.throughput_tps.toFixed(1)} t/s`,
            },
          ],
        },
      },
    }
  }, [barRadius, filtered, gridColor, t, textColor])

  if (filtered.length === 0) {
    return null
  }

  return (
    <div className={cn('h-48 sm:h-56', props.className)}>
      {themeReady && spec && (
        <VChart
          key={`tput-${resolvedTheme}`}
          spec={{
            ...spec,
            theme: resolvedTheme === 'dark' ? 'dark' : 'light',
            background: 'transparent',
          }}
          option={VCHART_OPTION}
        />
      )}
    </div>
  )
}
