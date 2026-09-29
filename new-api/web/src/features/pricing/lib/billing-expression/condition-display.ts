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
import type { TOptions } from 'i18next'

import { toIntlLocale } from '@/i18n/languages'

import { flattenBinary } from './display'
import { compileBillingExpression } from './parser'
import { TIME_FUNCTIONS, type ExpressionNode, type TimeFunction } from './types'

type Translate = (key: string, options?: TOptions) => string
type Description = {
  text: string
  kind: 'calendar' | 'clock' | 'combined'
  timezone: string
  disjunction?: boolean
}
type TimeComparison = {
  name: TimeFunction
  timezone: string
  operator: string
  value: number
}
const DOMAINS: Record<TimeFunction, [number, number]> = {
  hour: [0, 24],
  minute: [0, 60],
  weekday: [0, 7],
  month: [1, 13],
  day: [1, 32],
}

function timeComparison(node: ExpressionNode): TimeComparison | null {
  if (
    node.kind !== 'binary' ||
    !['==', '>=', '>', '<=', '<'].includes(node.operator) ||
    node.left.kind !== 'call' ||
    !(TIME_FUNCTIONS as readonly string[]).includes(node.left.name) ||
    node.left.args[0].kind !== 'literal' ||
    typeof node.left.args[0].value !== 'string' ||
    node.right.kind !== 'literal' ||
    typeof node.right.value !== 'number' ||
    !Number.isInteger(node.right.value)
  ) {
    return null
  }
  return {
    name: node.left.name as TimeFunction,
    timezone: node.left.args[0].value.trim() || 'UTC',
    operator: node.operator,
    value: node.right.value,
  }
}

function describeTimeRange(
  comparisons: TimeComparison[],
  t: Translate,
  locale: string
): Description | null {
  const first = comparisons[0]
  let [start, end] = DOMAINS[first.name]
  for (const comparison of comparisons) {
    switch (comparison.operator) {
      case '>=':
        start = Math.max(start, comparison.value)
        break
      case '>':
        start = Math.max(start, comparison.value + 1)
        break
      case '<':
        end = Math.min(end, comparison.value)
        break
      case '<=':
        end = Math.min(end, comparison.value + 1)
        break
      case '==':
        start = Math.max(start, comparison.value)
        end = Math.min(end, comparison.value + 1)
        break
    }
  }
  if (start >= end) return null
  let text: string
  let kind: Description['kind'] = 'calendar'
  if (first.name === 'hour') {
    kind = 'clock'
    text = t('{{start}}–{{end}}', {
      start: `${String(start).padStart(2, '0')}:00`,
      end: `${String(end).padStart(2, '0')}:00`,
    })
  } else if (first.name === 'weekday') {
    const formatter = new Intl.DateTimeFormat(toIntlLocale(locale), {
      weekday: 'short',
      timeZone: 'UTC',
    })
    // 2026-01-04 is Sunday, matching Go's weekday numbering.
    const from = formatter.format(new Date(Date.UTC(2026, 0, 4 + start)))
    const to = formatter.format(new Date(Date.UTC(2026, 0, 4 + end - 1)))
    text =
      start === end - 1
        ? from
        : t('{{start}}–{{end}}', { start: from, end: to })
  } else {
    const labels: Record<string, string> = {
      minute: 'Minute',
      month: 'Month',
      day: 'Day',
    }
    const values =
      start === end - 1
        ? String(start)
        : t('{{start}}–{{end}}', { start, end: end - 1 })
    text = `${t(labels[first.name])}: ${values}`
  }
  return { text, kind, timezone: first.timezone }
}

function describeBillingCondition(
  node: ExpressionNode,
  t: Translate,
  locale: string
): Description | null {
  if (node.kind === 'unary' && node.operator === '!') {
    const description = describeBillingCondition(node.operand, t, locale)
    if (!description) return null
    return {
      ...description,
      text: t('Outside these times: {{condition}}', {
        condition: description.text,
      }),
      kind: 'combined',
    }
  }
  if (
    node.kind === 'binary' &&
    ['<', '<=', '>', '>=', '==', '!='].includes(node.operator) &&
    node.left.kind === 'variable' &&
    node.right.kind === 'literal' &&
    typeof node.right.value === 'number'
  ) {
    const labels: Record<string, string> = {
      p: 'Input',
      c: 'Output',
      len: 'Full input length',
    }
    const label = labels[node.left.name]
    if (!label) return null
    return {
      text: `${t(label)} ${node.operator} ${node.right.value.toLocaleString(toIntlLocale(locale))}`,
      kind: 'combined',
      timezone: '',
    }
  }
  const single = timeComparison(node)
  if (single) return describeTimeRange([single], t, locale)
  if (node.kind !== 'binary' || !['&&', '||'].includes(node.operator)) {
    return null
  }
  const nodes = flattenBinary(node, node.operator)
  const parts: Description[] = []
  if (node.operator === '&&') {
    const ranges = new Map<string, TimeComparison[]>()
    for (const part of nodes) {
      const comparison = timeComparison(part)
      if (comparison) {
        const key = `${comparison.name}:${comparison.timezone}`
        const range = ranges.get(key) ?? []
        range.push(comparison)
        ranges.set(key, range)
      } else {
        const description = describeBillingCondition(part, t, locale)
        if (!description) return null
        parts.push(description)
      }
    }
    for (const range of ranges.values()) {
      const description = describeTimeRange(range, t, locale)
      if (!description) return null
      parts.push(description)
    }
  } else {
    for (const part of nodes) {
      const description = describeBillingCondition(part, t, locale)
      if (!description) return null
      parts.push(description)
    }
  }
  const zones = [...new Set(parts.map((part) => part.timezone).filter(Boolean))]
  if (parts.length === 0 || zones.length > 1) return null
  const timezone = zones[0] ?? ''
  if (parts.length === 1) return parts[0]
  const calendars = parts.filter((part) => part.kind === 'calendar')
  const clocks = parts.filter((part) => part.kind === 'clock')
  if (
    node.operator === '&&' &&
    parts.length === 2 &&
    calendars.length === 1 &&
    clocks.length === 1
  ) {
    return {
      text: `${calendars[0].text} ${clocks[0].text}`,
      kind: 'combined',
      timezone: parts[0].timezone,
    }
  }
  const separator =
    node.operator === '&&'
      ? '{{first}} and {{second}}'
      : '{{first}} or {{second}}'
  let text =
    node.operator === '&&' && parts[0].disjunction
      ? `(${parts[0].text})`
      : parts[0].text
  for (const part of parts.slice(1)) {
    const second =
      node.operator === '&&' && part.disjunction ? `(${part.text})` : part.text
    text = t(separator, { first: text, second })
  }
  return {
    text,
    kind: clocks.length === parts.length ? 'clock' : 'combined',
    timezone,
    disjunction: node.operator === '||',
  }
}

/** Presentation only. Unknown conditions retain their source; this never changes tier selection. */
export function formatBillingCondition(
  source: string,
  t: Translate,
  locale = 'en'
): string | null {
  const compiled = compileBillingExpression(source)
  if (compiled.status !== 'ready') return null
  try {
    // The returned value is rendered as React text, never as HTML.
    const translate: Translate = (key, options) =>
      t(key, { ...options, interpolation: { escapeValue: false } })
    const description = describeBillingCondition(
      compiled.ast,
      translate,
      locale
    )
    if (!description) return null
    if (!description.timezone) return description.text
    return translate('{{condition}} ({{timezone}})', {
      condition: description.text,
      timezone: description.timezone,
    })
  } catch {
    return null
  }
}
